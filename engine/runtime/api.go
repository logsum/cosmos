package runtime

import (
	"fmt"

	"cosmos/engine/manifest"
	"cosmos/engine/policy"

	v8 "rogchap.com/v8go"
)

// UIEmitFunc allows engine to send messages to UI without importing core.
// The caller (bootstrap) provides the actual implementation that routes to
// core's notifier.
type UIEmitFunc func(agentName, message string)

// ToolContext provides per-tool state to API callbacks.
// Each isolate gets its own ToolContext — no shared mutable state.
type ToolContext struct {
	AgentName      string
	Manifest       manifest.Manifest
	Evaluator      *policy.Evaluator
	StorageDir     string     // e.g., .cosmos/storage/
	UIEmit         UIEmitFunc // callback to send messages to chat
	AllowLoopback  bool       // skip loopback/private IP check in HTTP (for testing)
}

// APIBinding describes a single Go function exposed to JavaScript.
// Namespace groups related bindings (e.g., "console" for console.log).
type APIBinding struct {
	Namespace string
	Name      string
	Callback  v8.FunctionCallback
}

// APIRegistry collects bindings and injects them into V8 isolates.
// It holds shared, non-permission-gated bindings (like console.log).
// Per-tool permission-gated APIs (fs, http, storage, ui) are injected
// separately via injectToolAPIs.
type APIRegistry struct {
	bindings []APIBinding
}

// NewAPIRegistry creates a registry pre-loaded with default bindings
// (console.log as a no-op so JS code doesn't throw ReferenceErrors).
func NewAPIRegistry() *APIRegistry {
	r := &APIRegistry{}
	r.registerDefaults()
	return r
}

// registerDefaults adds built-in bindings that every isolate should have.
func (r *APIRegistry) registerDefaults() {
	r.Register(APIBinding{
		Namespace: "console",
		Name:      "log",
		Callback: func(info *v8.FunctionCallbackInfo) *v8.Value {
			// No-op: swallow console.log calls silently.
			return v8.Undefined(info.Context().Isolate())
		},
	})
}

// Register adds a binding to the registry. Must be called before any
// isolate is created — the registry is not safe for concurrent mutation
// after isolates start using it.
func (r *APIRegistry) Register(b APIBinding) {
	r.bindings = append(r.bindings, b)
}

// inject creates namespace ObjectTemplates on the global template and
// attaches FunctionTemplates for each binding. Bindings with the same
// namespace share a single ObjectTemplate.
func (r *APIRegistry) inject(iso *v8.Isolate, global *v8.ObjectTemplate) error {
	// Group bindings by namespace.
	namespaces := make(map[string]*v8.ObjectTemplate)

	for _, b := range r.bindings {
		ns, ok := namespaces[b.Namespace]
		if !ok {
			ns = v8.NewObjectTemplate(iso)
			namespaces[b.Namespace] = ns
		}

		fn := v8.NewFunctionTemplate(iso, b.Callback)
		if err := ns.Set(b.Name, fn, v8.ReadOnly); err != nil {
			return fmt.Errorf("set %s.%s: %w", b.Namespace, b.Name, err)
		}
	}

	for name, ns := range namespaces {
		if err := global.Set(name, ns, v8.ReadOnly); err != nil {
			return fmt.Errorf("set namespace %s: %w", name, err)
		}
	}

	return nil
}

// injectToolAPIs creates per-tool API bindings with captured ToolContext.
// Each callback is a closure over ctx, giving it access to the agent's
// manifest, evaluator, and storage directory for permission checks.
func injectToolAPIs(iso *v8.Isolate, global *v8.ObjectTemplate, ctx *ToolContext) error {
	if err := injectFsAPI(iso, global, ctx); err != nil {
		return fmt.Errorf("inject fs API: %w", err)
	}
	if err := injectHttpAPI(iso, global, ctx); err != nil {
		return fmt.Errorf("inject http API: %w", err)
	}
	if err := injectStorageAPI(iso, global, ctx); err != nil {
		return fmt.Errorf("inject storage API: %w", err)
	}
	if err := injectUiAPI(iso, global, ctx); err != nil {
		return fmt.Errorf("inject ui API: %w", err)
	}
	return nil
}
