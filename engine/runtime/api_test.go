package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cosmos/engine/manifest"
	"cosmos/engine/policy"
)

// newTestEvaluator creates an evaluator for testing with no policy file.
func newTestEvaluator(t *testing.T) *policy.Evaluator {
	t.Helper()
	tmp := t.TempDir()
	policyPath := filepath.Join(tmp, "policy.json")
	ev, err := policy.NewEvaluator(policyPath)
	if err != nil {
		t.Fatalf("create evaluator: %v", err)
	}
	return ev
}

// testToolContext creates a ToolContext for testing with the given rules.
func testToolContext(t *testing.T, agentName string, permissions map[string]manifest.PermissionMode) *ToolContext {
	t.Helper()
	m := manifest.Manifest{
		Name:        agentName,
		Version:     "1.0.0",
		Entry:       "index.js",
		Permissions: permissions,
	}

	rules := make([]manifest.PermissionRule, 0, len(permissions))
	for key, mode := range permissions {
		parsed, err := manifest.ParsePermissionKey(key)
		if err != nil {
			t.Fatalf("parse permission key %q: %v", key, err)
		}
		rules = append(rules, manifest.PermissionRule{Key: parsed, Mode: mode})
	}
	m.ParsedPermissions = rules

	return &ToolContext{
		AgentName:  agentName,
		Manifest:   m,
		Evaluator:  newTestEvaluator(t),
		StorageDir: t.TempDir(),
	}
}

func TestCheckPermission_Allow(t *testing.T) {
	ctx := testToolContext(t, "test-agent", map[string]manifest.PermissionMode{
		"fs:read": manifest.PermissionAllow,
	})

	if err := checkPermission(ctx, "fs:read"); err != nil {
		t.Errorf("expected allow, got: %v", err)
	}
}

func TestCheckPermission_Deny(t *testing.T) {
	ctx := testToolContext(t, "test-agent", map[string]manifest.PermissionMode{
		"fs:write": manifest.PermissionDeny,
	})

	err := checkPermission(ctx, "fs:write")
	if err == nil {
		t.Fatal("expected deny error")
	}
	if got := err.Error(); got != "permission denied: fs:write" {
		t.Errorf("error = %q, want 'permission denied: fs:write'", got)
	}
}

func TestCheckPermission_PromptAsDeny(t *testing.T) {
	tests := []struct {
		name string
		mode manifest.PermissionMode
	}{
		{"request_once", manifest.PermissionRequestOnce},
		{"request_always", manifest.PermissionRequestAlways},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testToolContext(t, "test-agent", map[string]manifest.PermissionMode{
				"net:http": tt.mode,
			})

			err := checkPermission(ctx, "net:http")
			if err == nil {
				t.Fatal("expected deny for prompt mode at V8 layer")
			}
			if got := err.Error(); got != "permission denied: net:http (requires user approval)" {
				t.Errorf("error = %q", got)
			}
		})
	}
}

func TestCheckPermission_DefaultDeny(t *testing.T) {
	ctx := testToolContext(t, "test-agent", map[string]manifest.PermissionMode{
		"fs:read": manifest.PermissionAllow,
	})

	err := checkPermission(ctx, "fs:write")
	if err == nil {
		t.Fatal("expected default deny for undeclared permission")
	}
}

func TestCheckPermission_ScopedHTTP(t *testing.T) {
	ctx := testToolContext(t, "test-agent", map[string]manifest.PermissionMode{
		"net:http:https://*.google.com/**": manifest.PermissionAllow,
	})

	if err := checkPermission(ctx, "net:http:https://www.google.com/search"); err != nil {
		t.Errorf("expected allow for scoped URL, got: %v", err)
	}

	if err := checkPermission(ctx, "net:http:https://evil.com/hack"); err == nil {
		t.Error("expected deny for out-of-scope URL")
	}
}

func TestCheckPermission_NilEvaluator(t *testing.T) {
	ctx := &ToolContext{
		AgentName: "test",
		Evaluator: nil,
	}

	if err := checkPermission(ctx, "fs:write:./anything"); err != nil {
		t.Errorf("nil evaluator should allow all, got: %v", err)
	}
}

func TestUIEmit(t *testing.T) {
	var capturedAgent, capturedMsg string

	uiEmit := func(agentName, message string) {
		capturedAgent = agentName
		capturedMsg = message
	}

	e := NewV8Executor(nil, nil, "", uiEmit)
	defer e.Close()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "index.js")
	if err := os.WriteFile(srcPath, []byte(`function emitTest(input) { ui.emit(input.message); return { ok: true }; }`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := e.RegisterTool(ToolSpec{
		AgentName: "test-agent", FunctionName: "emitTest",
		SourcePath: srcPath, Manifest: manifest.Manifest{},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := e.Execute(context.Background(), "emitTest", map[string]any{"message": "progress update"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if capturedAgent != "test-agent" {
		t.Errorf("agent = %q, want %q", capturedAgent, "test-agent")
	}
	if capturedMsg != "progress update" {
		t.Errorf("message = %q, want %q", capturedMsg, "progress update")
	}
}

func TestUIEmit_NilCallback(t *testing.T) {
	e := NewV8Executor(nil, nil, "", nil)
	defer e.Close()

	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "index.js")
	if err := os.WriteFile(srcPath, []byte(`function emitNil(input) { ui.emit("test"); return { ok: true }; }`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := e.RegisterTool(ToolSpec{
		AgentName: "test-agent", FunctionName: "emitNil",
		SourcePath: srcPath, Manifest: manifest.Manifest{},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := e.Execute(context.Background(), "emitNil", map[string]any{})
	if err != nil {
		t.Fatalf("execute should not fail with nil callback: %v", err)
	}
}
