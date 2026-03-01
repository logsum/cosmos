package runtime

import (
	"context"
	"encoding/json"
	"fmt"
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

const (
	// MaxToolTimeout is the absolute ceiling for any tool execution.
	MaxToolTimeout = 5 * time.Minute

	// DefaultToolTimeout is used when the manifest has no timeout.
	DefaultToolTimeout = 30 * time.Second
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

// NewV8Executor creates an executor with the given API registry and optional
// per-tool context dependencies. Pass nil evaluator to skip permission checks
// (test/stub mode). Pass nil uiEmit for silent ui.emit (no-op).
func NewV8Executor(registry *APIRegistry, evaluator *policy.Evaluator, storageDir string, uiEmit UIEmitFunc) *V8Executor {
	if registry == nil {
		registry = NewAPIRegistry()
	}
	return &V8Executor{
		tools:      make(map[string]*toolEntry),
		registry:   registry,
		evaluator:  evaluator,
		storageDir: storageDir,
		uiEmit:     uiEmit,
	}
}

// RegisterTool adds a tool spec. The V8 isolate is NOT created until
// the first Execute call (lazy loading). Returns an error if the
// function name is already registered.
func (e *V8Executor) RegisterTool(spec ToolSpec) error {
	e.mu.Lock()
	defer e.mu.Unlock()

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
	e.mu.Lock()
	entry, ok := e.tools[name]
	e.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}

	ti := entry.isolate
	ti.mu.Lock()
	defer ti.mu.Unlock()

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
			return "", wrapJSError(r.err, entry.spec.SourcePath)
		}
		return r.val, nil

	case <-time.After(timeout):
		ti.iso.TerminateExecution()
		// Drain the result channel so the goroutine can exit.
		<-resultCh
		// Terminated isolate is unusable; dispose and force recompile.
		e.disposeIsolate(ti)
		return "", fmt.Errorf("tool %s timed out after %s", entry.spec.FunctionName, timeout)

	case <-ctx.Done():
		ti.iso.TerminateExecution()
		<-resultCh
		e.disposeIsolate(ti)
		return "", fmt.Errorf("tool %s cancelled: %w", entry.spec.FunctionName, ctx.Err())
	}
}

// Close disposes all V8 isolates. Safe to call multiple times.
func (e *V8Executor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, entry := range e.tools {
		ti := entry.isolate
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
