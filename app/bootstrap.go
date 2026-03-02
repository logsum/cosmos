package app

import (
	"context"
	"cosmos/config"
	"cosmos/core"
	"cosmos/core/provider"
	"cosmos/engine/loader"
	"cosmos/engine/maintenance"
	"cosmos/engine/policy"
	"cosmos/engine/runtime"
	"cosmos/engine/vfs"
	"cosmos/providers/bedrock"
	"cosmos/ui"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

// Bootstrap creates and wires all application dependencies.
// Each phase is separate for testability.
func Bootstrap(ctx context.Context) (*Application, error) {
	// 1. Load configuration
	cfg, warnings, err := loadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "cosmos: warning: %s\n", w)
	}

	// 1.5. Clean up old session data
	cleanupOpts := maintenance.CleanupOptions{
		CosmosDir:   ".cosmos",
		SessionsDir: cfg.SessionsDir,
		MaxAge:      30 * 24 * time.Hour,
		DryRun:      false,
	}
	cleanupResult, err := maintenance.CleanupSessionData(cleanupOpts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cosmos: warning: session cleanup failed: %v\n", err)
	} else if len(cleanupResult.Errors) > 0 {
		for _, e := range cleanupResult.Errors {
			fmt.Fprintf(os.Stderr, "cosmos: warning: cleanup: %s\n", e)
		}
	} else if cleanupResult.DeletedAuditFiles > 0 || cleanupResult.DeletedSnapshotDirs > 0 || cleanupResult.DeletedSessionFiles > 0 {
		// Only log if something was actually deleted (reduce noise)
		totalDeleted := cleanupResult.DeletedAuditFiles + cleanupResult.DeletedSnapshotDirs + cleanupResult.DeletedSessionFiles
		fmt.Fprintf(os.Stderr, "cosmos: cleaned up old session data: %d files\n", totalDeleted)
	}

	// 2. Initialize currency formatter
	currencyFormatter, err := setupCurrencyFormatter(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cosmos: warning: currency setup failed: %v\n", err)
		currencyFormatter = core.DefaultCurrencyFormatter()
	}

	// 3. Initialize LLM provider
	llmProvider, err := setupProvider(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("initializing provider: %w", err)
	}

	// 4. Set up UI and notifier
	scaffold := ui.NewScaffold()
	notifier := scaffold.GetNotifier()

	// 5. Create pricing tracker with UI callbacks
	tracker := setupTracker(notifier, currencyFormatter)

	// 6. Create core session (executor, tools, adapter, snapshotter)
	sr, err := setupSession(ctx, cfg, llmProvider, tracker, notifier)
	if err != nil {
		return nil, fmt.Errorf("initializing session: %w", err)
	}
	// From here, failures must clean up the executor (V8 isolates).
	cleanup := func() {
		if sr.executor != nil {
			sr.executor.Close()
		}
	}

	// Build restore function for Changelog UI.
	var restoreFunc ui.RestoreFunc
	if sr.snapshotter != nil {
		snap := sr.snapshotter
		restoreFunc = func(interactionID string) tea.Cmd {
			return func() tea.Msg {
				paths, err := snap.RestoreInteraction(interactionID)
				if err != nil {
					return ui.ChangelogRestoreResultMsg{
						InteractionID: interactionID,
						Success:       false,
						Message:       err.Error(),
					}
				}
				return ui.ChangelogRestoreResultMsg{
					InteractionID: interactionID,
					Success:       true,
					Message:       fmt.Sprintf("Restored %d file(s)", len(paths)),
				}
			}
		}
	}

	// 7. Configure UI pages
	if err := configureUI(scaffold, sr.session, sr.tools, cfg.DefaultModel, restoreFunc); err != nil {
		cleanup()
		return nil, fmt.Errorf("configuring UI: %w", err)
	}

	// 8. Create Bubble Tea program
	program := setupProgram(scaffold, notifier, sr.session)

	return &Application{
		Config:            cfg,
		Session:           sr.session,
		Scaffold:          scaffold,
		Program:           program,
		CurrencyFormatter: currencyFormatter,
		Tracker:           tracker,
		Executor:          sr.executor,
	}, nil
}

// loadConfig loads configuration from disk and ensures directories exist.
func loadConfig() (config.Config, []string, error) {
	cfg, warnings, err := config.Load()
	if err != nil {
		return config.Config{}, nil, err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return config.Config{}, nil, err
	}
	return cfg, warnings, nil
}

// setupCurrencyFormatter initializes currency conversion if needed.
// Retries up to 3 times with exponential backoff (1s, 2s, 4s) before
// returning an error that triggers fallback to USD.
func setupCurrencyFormatter(ctx context.Context, cfg config.Config) (*core.CurrencyFormatter, error) {
	if cfg.Currency == "USD" {
		return core.DefaultCurrencyFormatter(), nil
	}

	engine := core.NewCurrencyEngine(&http.Client{})

	var lastErr error
	for attempt := range 3 {
		rate, err := engine.FetchRate(ctx, "USD", cfg.Currency)
		if err == nil {
			symbol := core.CurrencySymbol(cfg.Currency)
			return core.NewCurrencyFormatter(cfg.Currency, symbol, rate), nil
		}
		lastErr = err

		// Exponential backoff: 1s, 2s, 4s
		backoff := time.Duration(1<<attempt) * time.Second
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, fmt.Errorf("currency fetch cancelled: %w", ctx.Err())
		}
	}

	return nil, fmt.Errorf("currency fetch failed after 3 attempts: %w", lastErr)
}

// setupProvider initializes the LLM provider (currently Bedrock).
func setupProvider(ctx context.Context, cfg config.Config) (provider.Provider, error) {
	pricingCfg := provider.PricingConfig{
		Enabled:  cfg.PricingEnabled,
		CacheDir: cfg.PricingCacheDir,
		CacheTTL: cfg.PricingCacheTTL,
	}
	return bedrock.NewBedrock(ctx, cfg.AWSRegion, cfg.AWSProfile, pricingCfg)
}

// setupTracker creates a pricing tracker with UI update callbacks.
func setupTracker(notifier *ui.Notifier, formatter *core.CurrencyFormatter) *core.Tracker {
	return core.NewTracker(
		func(snap core.CostSnapshot) {
			notifier.Send(ui.StatusItemUpdateMsg{
				Key:   "tokens",
				Value: snap.FormatTokens(),
			})
			notifier.Send(ui.StatusItemUpdateMsg{
				Key:   "cost",
				Value: snap.FormatCost(),
			})
		},
		formatter,
	)
}

// setupSessionResult contains everything produced by setupSession.
type setupSessionResult struct {
	session     *core.Session
	tools       []provider.ToolDefinition
	executor    *runtime.V8Executor
	snapshotter *vfs.Snapshotter
}

// setupSession creates the core session with executor, tools, and event adapter.
func setupSession(
	_ context.Context,
	cfg config.Config,
	llmProvider provider.Provider,
	tracker *core.Tracker,
	notifier *ui.Notifier,
) (*setupSessionResult, error) {
	adapter := &coreNotifierAdapter{ui: notifier}

	// Create audit logger with session ID
	sessionID := uuid.New().String()
	cosmosDir := ".cosmos" // Project-local directory
	auditLogger, err := policy.NewAuditLogger(sessionID, cosmosDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cosmos: warning: audit logger init failed: %v\n", err)
		auditLogger = nil
	}

	// Create policy evaluator
	// Note: If policy.json doesn't exist, evaluator still succeeds with empty overrides (stub mode OK)
	// If policy.json exists but is malformed/unreadable, this is an error - fail explicitly
	policyPath := filepath.Join(cosmosDir, "policy.json")
	evaluator, err := policy.NewEvaluator(policyPath)
	if err != nil {
		// Policy file exists but is malformed or unreadable - this is a fatal error
		// (if file doesn't exist, NewEvaluator succeeds with empty overrides)
		return nil, fmt.Errorf("policy evaluator init failed: %w", err)
	}

	// Create VFS snapshotter for file rollback.
	snapshotter, err := vfs.NewSnapshotter(cosmosDir, sessionID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cosmos: warning: snapshotter init failed: %v\n", err)
		snapshotter = nil
	}

	// Build snapshot function closure that bridges vfs → session.
	// session is assigned below; we capture a pointer so the closure
	// picks up the value once it's set.
	var session *core.Session
	var snapshotFunc runtime.SnapshotFunc
	if snapshotter != nil {
		snapshotFunc = func(path, operation, agentName string) error {
			rec, err := snapshotter.Snapshot(path, operation, agentName)
			if err != nil {
				return err
			}
			if session != nil {
				session.RecordFileChange(rec.Path, rec.Operation, rec.WasNewFile)
			}
			return nil
		}
	}

	// Load agents from disk (builtin + user dirs) and wire V8 executor.
	storageDir := filepath.Join(cosmosDir, "storage")
	result, err := loader.Load("engine/agents", cfg.AgentsDir, storageDir, evaluator, nil, snapshotFunc)
	if err != nil {
		return nil, fmt.Errorf("loading agents: %w", err)
	}
	for _, agentErr := range result.Errors {
		fmt.Fprintf(os.Stderr, "cosmos: warning: agent %s: %v\n", agentErr.Dir, agentErr.Err)
	}

	// Pass the same sessionID to both audit logger and session
	session = core.NewSession(
		sessionID,
		llmProvider,
		tracker,
		adapter,
		cfg.DefaultModel,
		"You are a helpful coding assistant with access to tools.",
		4096, // MaxTokens
		result.Executor,
		result.Tools,
		auditLogger,
		evaluator,
	)

	// Wire VFS snapshot context updater.
	if snapshotter != nil {
		session.SetSnapshotContextUpdater(snapshotter)
	}

	// Wire configurable permission timeout if set.
	if cfg.PermissionTimeout > 0 {
		session.SetPermissionTimeout(time.Duration(cfg.PermissionTimeout) * time.Second)
	}

	// Wire sessions directory for /restore completions.
	session.SetSessionsDir(cfg.SessionsDir)

	return &setupSessionResult{
		session:     session,
		tools:       result.Tools,
		executor:    result.Executor,
		snapshotter: snapshotter,
	}, nil
}

// configureUI sets up scaffold pages and status bar items.
func configureUI(scaffold *ui.Scaffold, session *core.Session, tools []provider.ToolDefinition, model string, restoreFunc ui.RestoreFunc) error {
	// Get current directory for status bar
	currentDir, err := os.Getwd()
	if err != nil {
		currentDir = "unknown"
	} else {
		currentDir = filepath.Base(currentDir)
	}

	ui.ConfigureDefaultScaffold(scaffold, currentDir, model)

	// Convert core tools to UI tools
	uiTools := make([]ui.Tool, len(tools))
	for i, t := range tools {
		uiTools[i] = ui.Tool{Name: t.Name, Description: t.Description}
	}

	ui.AddDefaultPages(scaffold, session, uiTools, restoreFunc)
	return nil
}

// setupProgram creates the Bubble Tea program with correct screen mode.
func setupProgram(scaffold *ui.Scaffold, notifier *ui.Notifier, session *core.Session) *tea.Program {
	app := ui.NewApp(scaffold, ui.AppConfig{
		Placeholder:        "Type your message here...",
		CharLimit:          0, // unlimited
		CompletionProvider: session,
	})

	// IMPORTANT: DO NOT use tea.WithAltScreen()!
	// We intentionally run in the primary screen buffer (not alternate screen) so that:
	// 1. All output (splash, messages, responses) goes to stdout and persists in terminal history
	// 2. Users can scroll the terminal (iTerm, etc.) to see past messages, the welcome logo, etc.
	// 3. The chat history is preserved in the terminal's scrollback buffer
	// Using tea.WithAltScreen() would put the app in an isolated alternate screen buffer
	// with no scrollback history, blocking access to previous content.
	program := tea.NewProgram(app, tea.WithMouseCellMotion())
	notifier.SetProgram(program)

	return program
}
