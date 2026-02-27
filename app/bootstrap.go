package app

import (
	"context"
	"cosmos/config"
	"cosmos/core"
	"cosmos/core/provider"
	"cosmos/engine/maintenance"
	"cosmos/engine/policy"
	"cosmos/engine/tools"
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

	// 6. Create core session (executor, tools, adapter)
	session, tools := setupSession(ctx, cfg, llmProvider, tracker, notifier)

	// 7. Configure UI pages
	if err := configureUI(scaffold, session, tools, cfg.DefaultModel); err != nil {
		return nil, fmt.Errorf("configuring UI: %w", err)
	}

	// 8. Create Bubble Tea program
	program := setupProgram(scaffold, notifier)

	return &Application{
		Config:            cfg,
		Session:           session,
		Scaffold:          scaffold,
		Program:           program,
		CurrencyFormatter: currencyFormatter,
		Tracker:           tracker,
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
func setupCurrencyFormatter(ctx context.Context, cfg config.Config) (*core.CurrencyFormatter, error) {
	if cfg.Currency == "USD" {
		return core.DefaultCurrencyFormatter(), nil
	}

	engine := core.NewCurrencyEngine(&http.Client{})
	rate, err := engine.FetchRate(ctx, "USD", cfg.Currency)
	if err != nil {
		return nil, err
	}

	symbol := core.CurrencySymbol(cfg.Currency)
	return core.NewCurrencyFormatter(cfg.Currency, symbol, rate), nil
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

// setupSession creates the core session with executor, tools, and event adapter.
// The ctx parameter is currently unused but kept for future extensibility
// (e.g., when loading tools from disk, initializing V8 runtime).
func setupSession(
	ctx context.Context,
	cfg config.Config,
	llmProvider provider.Provider,
	tracker *core.Tracker,
	notifier *ui.Notifier,
) (*core.Session, []provider.ToolDefinition) {
	_ = ctx // Reserved for future use (tool loading, V8 initialization)

	executor := tools.NewStubExecutor()
	toolDefs := tools.StubToolDefinitions()
	adapter := &coreNotifierAdapter{ui: notifier}

	// Create audit logger with session ID
	sessionID := uuid.New().String()
	cosmosDir := ".cosmos" // Project-local directory
	auditLogger, err := policy.NewAuditLogger(sessionID, cosmosDir)
	if err != nil {
		// Log warning but continue (audit is non-critical for core functionality)
		fmt.Fprintf(os.Stderr, "cosmos: warning: audit logger init failed: %v\n", err)
		auditLogger = nil
	}

	// Pass the same sessionID to both audit logger and session
	session := core.NewSession(
		sessionID,
		llmProvider,
		tracker,
		adapter,
		cfg.DefaultModel,
		"You are a helpful coding assistant with access to tools.",
		4096, // MaxTokens
		executor,
		toolDefs,
		auditLogger,
	)

	return session, toolDefs
}

// configureUI sets up scaffold pages and status bar items.
func configureUI(scaffold *ui.Scaffold, session *core.Session, tools []provider.ToolDefinition, model string) error {
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

	ui.AddDefaultPages(scaffold, session, uiTools)
	return nil
}

// setupProgram creates the Bubble Tea program with correct screen mode.
func setupProgram(scaffold *ui.Scaffold, notifier *ui.Notifier) *tea.Program {
	app := ui.NewApp(scaffold, ui.AppConfig{
		Placeholder: "Type your message here...",
		CharLimit:   0, // unlimited
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
