package runtime

// Lock ordering: e.mu → ti.mu (never hold e.mu while acquiring ti.mu in the
// opposite direction). Close() and Execute() both release e.mu before or
// immediately after acquiring ti.mu to maintain consistent ordering.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"cosmos/engine/manifest"
	"cosmos/engine/policy"

	v8 "rogchap.com/v8go"
)

// jsIdentifierRe matches valid JavaScript identifiers (ASCII subset).
// Rejects names that could cause script injection when interpolated.
var jsIdentifierRe = regexp.MustCompile(`^[a-zA-Z_$][a-zA-Z0-9_$]*$`)

// agentNameRe matches valid agent names: lowercase alphanumeric with hyphens/underscores.
var agentNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

const (
	// MaxToolTimeout is the absolute ceiling for any tool execution.
	MaxToolTimeout = 5 * time.Minute

	// DefaultToolTimeout is used when the manifest has no timeout.
	DefaultToolTimeout = 30 * time.Second

	// isolateGracePeriod is how long to wait for a terminated V8 isolate
	// goroutine to exit before deciding whether to leak it.
	isolateGracePeriod = 5 * time.Second
)

// ToolSpec contains everything needed to compile one tool.
// Phase 3.3 (agent loader) will populate these from disk discovery.
type ToolSpec struct {
	AgentName    string
	FunctionName string
	SourcePath   string
	Manifest     manifest.Manifest
}

// toolIsolate holds the V8 resources for a loaded tool.
type toolIsolate struct {
	mu       sync.Mutex
	iso      *v8.Isolate
	ctx      *v8.Context
	modTime  time.Time
	compiled bool
	leaked   bool // true if isolate timed out and goroutine may still be running
}

// toolEntry pairs a spec with its lazy-loaded isolate.
type toolEntry struct {
	spec    ToolSpec
	isolate *toolIsolate
}

// V8Executor runs JavaScript tools in sandboxed V8 isolates.
// It implements core.ToolExecutor.
type V8Executor struct {
	mu             sync.Mutex
	tools          map[string]*toolEntry // keyed by function name
	registry       *APIRegistry
	evaluator      *policy.Evaluator
	storageDir     string
	uiEmit         UIEmitFunc
	allowLoopback  bool // skip loopback/private IP check in HTTP (for testing)
}

// NewV8Executor creates an executor with a default API registry and optional
// per-tool context dependencies. Pass nil evaluator to skip permission checks
// (test/stub mode). Pass nil uiEmit for silent ui.emit (no-op).
func NewV8Executor(evaluator *policy.Evaluator, storageDir string, uiEmit UIEmitFunc) *V8Executor {
	return &V8Executor{
		tools:      make(map[string]*toolEntry),
		registry:   NewAPIRegistry(),
		evaluator:  evaluator,
		storageDir: storageDir,
		uiEmit:     uiEmit,
	}
}

// ToolPermissionRules returns the agent name and parsed permission rules
// for a registered tool. Returns false if the tool is not registered.
// This enables the core loop to evaluate manifest permissions without
// importing the engine/runtime package directly.
func (e *V8Executor) ToolPermissionRules(name string) (agentName string, rules []manifest.PermissionRule, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	entry, found := e.tools[name]
	if !found {
		return "", nil, false
	}
	return entry.spec.AgentName, entry.spec.Manifest.ParsedPermissions, true
}

// RegisterTool adds a tool spec. The V8 isolate is NOT created until
// the first Execute call (lazy loading). Returns an error if the
// function name is already registered.
func (e *V8Executor) RegisterTool(spec ToolSpec) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if spec.AgentName != "" && !agentNameRe.MatchString(spec.AgentName) {
		return fmt.Errorf("invalid agent name %q: must match [a-z0-9][a-z0-9_-]*", spec.AgentName)
	}

	if !jsIdentifierRe.MatchString(spec.FunctionName) {
		return fmt.Errorf("tool function name %q is not a valid JS identifier", spec.FunctionName)
	}

	if _, exists := e.tools[spec.FunctionName]; exists {
		return fmt.Errorf("tool %q already registered", spec.FunctionName)
	}

	e.tools[spec.FunctionName] = &toolEntry{
		spec:    spec,
		isolate: &toolIsolate{},
	}
	return nil
}

// Execute runs a tool by function name. It satisfies core.ToolExecutor.
//
//	func (e *V8Executor) Execute(ctx context.Context, name string, input map[string]any) (string, error)
func (e *V8Executor) Execute(ctx context.Context, name string, input map[string]any) (string, error) {
	// Hold executor lock until isolate lock is acquired to prevent a race
	// where Close() disposes the isolate between map lookup and ti.mu.Lock().
	e.mu.Lock()
	entry, ok := e.tools[name]
	if !ok {
		e.mu.Unlock()
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	ti := entry.isolate
	ti.mu.Lock()
	e.mu.Unlock() // Release after isolate lock acquired
	defer ti.mu.Unlock()

	// Refuse to execute if isolate leaked (timed-out write agent still running).
	if ti.leaked {
		return "", fmt.Errorf("tool %s isolate leaked (previous execution timed out and did not terminate)", name)
	}

	// Hot reload: check for source file changes.
	if err := e.maybeReload(entry); err != nil {
		return "", fmt.Errorf("reload check: %w", err)
	}

	// Lazy compile on first invocation.
	if !ti.compiled {
		if err := e.compile(entry); err != nil {
			return "", fmt.Errorf("compile %s: %w", name, err)
		}
	}

	return e.executeWithTimeout(ctx, entry, input)
}

// compile reads JS source from disk, creates a V8 isolate, injects
// APIs (shared + per-tool), and runs the script.
func (e *V8Executor) compile(entry *toolEntry) error {
	spec := entry.spec
	ti := entry.isolate

	source, err := os.ReadFile(spec.SourcePath)
	if err != nil {
		return fmt.Errorf("read source %s: %w", spec.SourcePath, err)
	}

	info, err := os.Stat(spec.SourcePath)
	if err != nil {
		return fmt.Errorf("stat source %s: %w", spec.SourcePath, err)
	}

	iso := v8.NewIsolate()
	global := v8.NewObjectTemplate(iso)

	// Shared bindings (console.log, etc.)
	if err := e.registry.inject(iso, global); err != nil {
		iso.Dispose()
		return fmt.Errorf("inject shared APIs: %w", err)
	}

	// Per-tool bindings (fs, http, storage, ui) with captured context.
	toolCtx := &ToolContext{
		AgentName:     spec.AgentName,
		Manifest:      spec.Manifest,
		Evaluator:     e.evaluator,
		StorageDir:    e.storageDir,
		UIEmit:        e.uiEmit,
		AllowLoopback: e.allowLoopback,
	}
	if err := injectToolAPIs(iso, global, toolCtx); err != nil {
		iso.Dispose()
		return fmt.Errorf("inject tool APIs: %w", err)
	}

	v8ctx := v8.NewContext(iso, global)

	if _, err := v8ctx.RunScript(string(source), spec.SourcePath); err != nil {
		v8ctx.Close()
		iso.Dispose()
		return wrapJSError(err, spec.SourcePath)
	}

	ti.iso = iso
	ti.ctx = v8ctx
	ti.modTime = info.ModTime()
	ti.compiled = true
	return nil
}

// maybeReload disposes the isolate if the source file has been modified,
// forcing recompilation on the next Execute. Caller must hold ti.mu.
func (e *V8Executor) maybeReload(entry *toolEntry) error {
	ti := entry.isolate
	if !ti.compiled {
		return nil // Not yet compiled; nothing to reload.
	}

	info, err := os.Stat(entry.spec.SourcePath)
	if err != nil {
		return fmt.Errorf("stat for reload: %w", err)
	}

	if !info.ModTime().Equal(ti.modTime) {
		e.disposeIsolate(ti)
	}
	return nil
}

// executeWithTimeout runs the tool function inside V8 with a timeout
// derived from the manifest. Caller must hold ti.mu.
func (e *V8Executor) executeWithTimeout(ctx context.Context, entry *toolEntry, input map[string]any) (string, error) {
	ti := entry.isolate
	timeout := effectiveTimeout(entry.spec.Manifest)

	inputJSON, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("marshal input: %w", err)
	}

	// Build invocation script: JSON.stringify(functionName(JSON.parse('...')))
	script := fmt.Sprintf(
		`JSON.stringify(%s(JSON.parse(%s)))`,
		entry.spec.FunctionName,
		escapeJSString(string(inputJSON)),
	)

	type result struct {
		val string
		err error
	}
	resultCh := make(chan result, 1)

	go func() {
		val, err := ti.ctx.RunScript(script, entry.spec.SourcePath)
		if err != nil {
			resultCh <- result{err: err}
			return
		}
		resultCh <- result{val: val.String()}
	}()

	select {
	case r := <-resultCh:
		if r.err != nil {
			// Any JS error invalidates the isolate — force recompile on next call.
			e.disposeIsolate(ti)
			return "", wrapJSError(r.err, entry.spec.SourcePath)
		}
		return r.val, nil

	case <-time.After(timeout):
		ti.iso.TerminateExecution()
		// Wait a grace period for the goroutine to exit before disposing.
		select {
		case <-resultCh:
			// Goroutine completed within grace period, safe to dispose.
			e.disposeIsolate(ti)
		case <-time.After(isolateGracePeriod):
			if hasWritePermissions(&entry.spec.Manifest) {
				log.Printf("WARNING: leaking V8 isolate for %s (write agent did not terminate within grace period)", entry.spec.FunctionName)
				// Mark isolate as leaked so subsequent Execute calls will fail.
				ti.leaked = true
			} else {
				e.disposeIsolate(ti) // Force dispose for read-only agents.
			}
		}
		return "", fmt.Errorf("tool %s timed out after %s", entry.spec.FunctionName, timeout)

	case <-ctx.Done():
		ti.iso.TerminateExecution()
		// Wait a grace period for the goroutine to exit before disposing.
		select {
		case <-resultCh:
			e.disposeIsolate(ti)
		case <-time.After(isolateGracePeriod):
			if hasWritePermissions(&entry.spec.Manifest) {
				log.Printf("WARNING: leaking V8 isolate for %s (write agent did not terminate within grace period)", entry.spec.FunctionName)
				// Mark isolate as leaked so subsequent Execute calls will fail.
				ti.leaked = true
			} else {
				e.disposeIsolate(ti)
			}
		}
		return "", fmt.Errorf("tool %s cancelled: %w", entry.spec.FunctionName, ctx.Err())
	}
}

// Close disposes all V8 isolates. Safe to call multiple times.
func (e *V8Executor) Close() {
	// Snapshot the tools map under executor lock, then release it before
	// acquiring individual isolate locks (maintains e.mu → ti.mu ordering).
	e.mu.Lock()
	isolates := make([]*toolIsolate, 0, len(e.tools))
	for _, entry := range e.tools {
		isolates = append(isolates, entry.isolate)
	}
	e.mu.Unlock()

	for _, ti := range isolates {
		ti.mu.Lock()
		e.disposeIsolate(ti)
		ti.mu.Unlock()
	}
}

// disposeIsolate releases V8 resources and marks the entry for recompilation.
// Caller must hold ti.mu.
func (e *V8Executor) disposeIsolate(ti *toolIsolate) {
	if ti.ctx != nil {
		ti.ctx.Close()
		ti.ctx = nil
	}
	if ti.iso != nil {
		ti.iso.Dispose()
		ti.iso = nil
	}
	ti.compiled = false
}

// hasWritePermissions returns true if the manifest declares any non-deny
// write or docker permissions, indicating in-flight operations that could
// corrupt state if the isolate is forcefully disposed.
func hasWritePermissions(m *manifest.Manifest) bool {
	if m == nil {
		return false
	}
	for key, mode := range m.Permissions {
		if mode == manifest.PermissionDeny {
			continue
		}
		if strings.HasPrefix(key, "fs:write") || strings.HasPrefix(key, "docker:") {
			return true
		}
	}
	return false
}

// effectiveTimeout returns the timeout for a tool, clamped to MaxToolTimeout.
func effectiveTimeout(m manifest.Manifest) time.Duration {
	t := m.TimeoutDuration
	if t <= 0 {
		t = DefaultToolTimeout
	}
	if t > MaxToolTimeout {
		t = MaxToolTimeout
	}
	return t
}

// escapeJSString wraps s in single quotes with proper escaping for
// embedding in a JavaScript expression.
func escapeJSString(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		switch r {
		case '\'':
			b.WriteString(`\'`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\u2028':
			b.WriteString(`\u2028`)
		case '\u2029':
			b.WriteString(`\u2029`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// wrapJSError converts a v8go error into a descriptive Go error.
// If the error is a *v8.JSError, the message, location, and stack trace
// are included.
func wrapJSError(err error, origin string) error {
	if jsErr, ok := err.(*v8.JSError); ok {
		msg := jsErr.Message
		if jsErr.Location != "" {
			msg = jsErr.Location + ": " + msg
		}
		if jsErr.StackTrace != "" {
			msg += "\n" + jsErr.StackTrace
		}
		return fmt.Errorf("js error in %s: %s", origin, msg)
	}
	return fmt.Errorf("js error in %s: %w", origin, err)
}
