package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cosmos/engine/manifest"
)

// testFixturePath returns the absolute path to a testdata fixture.
func testFixturePath(t *testing.T, agent, file string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", agent, file))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	return abs
}

// loadTestManifest parses a fixture's manifest.
func loadTestManifest(t *testing.T, agent string) manifest.Manifest {
	t.Helper()
	path := testFixturePath(t, agent, "cosmo.manifest.json")
	m, err := manifest.ParseManifestFile(path, manifest.VerifyConfig{})
	if err != nil {
		t.Fatalf("parse manifest %s: %v", path, err)
	}
	return m
}

// echoSpec returns a ToolSpec for the echo fixture.
func echoSpec(t *testing.T) ToolSpec {
	t.Helper()
	return ToolSpec{
		AgentName:    "echo-agent",
		FunctionName: "echo",
		SourcePath:   testFixturePath(t, "echo", "index.js"),
		Manifest:     loadTestManifest(t, "echo"),
	}
}

// --- Tests ---

func TestEffectiveTimeout(t *testing.T) {
	tests := []struct {
		name     string
		timeout  string
		expected time.Duration
	}{
		{"default when unset", "", DefaultToolTimeout},
		{"manifest value", "10s", 10 * time.Second},
		{"capped at max", "10m", MaxToolTimeout},
		{"small value", "100ms", 100 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := manifest.Manifest{}
			if tt.timeout != "" {
				d, err := time.ParseDuration(tt.timeout)
				if err != nil {
					t.Fatalf("parse duration: %v", err)
				}
				m.TimeoutDuration = d
			}
			got := effectiveTimeout(m)
			if got != tt.expected {
				t.Errorf("effectiveTimeout() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestToolRegistration(t *testing.T) {
	e := NewV8Executor(nil, "", nil, nil)
	defer e.Close()

	spec := echoSpec(t)
	if err := e.RegisterTool(spec); err != nil {
		t.Fatalf("register echo: %v", err)
	}

	// Duplicate should fail.
	if err := e.RegisterTool(spec); err == nil {
		t.Fatal("expected error on duplicate registration")
	}

	// Different function name should work.
	spec2 := spec
	spec2.FunctionName = "echo2"
	if err := e.RegisterTool(spec2); err != nil {
		t.Fatalf("register echo2: %v", err)
	}

	// Invalid JS identifiers should be rejected.
	invalid := []string{"my-func", "123abc", "a b", "; evil()", ""}
	for _, name := range invalid {
		bad := spec
		bad.FunctionName = name
		if err := e.RegisterTool(bad); err == nil {
			t.Errorf("expected error for invalid function name %q", name)
		}
	}
}

func TestConsoleLogDefault(t *testing.T) {
	// console.log should be available by default and not throw.
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "index.js")
	src := `function logtest(input) { console.log("debug:", input.msg); return { ok: true }; }`
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	e := NewV8Executor(nil, "", nil, nil)
	defer e.Close()

	if err := e.RegisterTool(ToolSpec{
		AgentName: "test", FunctionName: "logtest",
		SourcePath: srcPath, Manifest: manifest.Manifest{},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	result, err := e.Execute(context.Background(), "logtest", map[string]any{"msg": "hello"})
	if err != nil {
		t.Fatalf("execute: %v (console.log should be a no-op, not throw)", err)
	}
	if !strings.Contains(result, `"ok":true`) {
		t.Errorf("result = %s, want ok:true", result)
	}
}

func TestEscapeJSString(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{"simple", `hello`, `'hello'`},
		{"single quotes", `it's`, `'it\'s'`},
		{"backslash", `a\b`, `'a\\b'`},
		{"newline", "line1\nline2", `'line1\nline2'`},
		{"tab", "col1\tcol2", `'col1\tcol2'`},
		{"carriage return", "a\rb", `'a\rb'`},
		{"empty", "", `''`},
		{"json", `{"key":"value"}`, `'{"key":"value"}'`},
		{"nested quotes", `{"k":"it's"}`, `'{"k":"it\'s"}'`},
		{"U+2028 line separator", "a\u2028b", `'a\u2028b'`},
		{"U+2029 paragraph separator", "a\u2029b", `'a\u2029b'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapeJSString(tt.input)
			if got != tt.expect {
				t.Errorf("escapeJSString(%q) = %q, want %q", tt.input, got, tt.expect)
			}
		})
	}
}

func TestExecuteEcho(t *testing.T) {
	e := NewV8Executor(nil, "", nil, nil)
	defer e.Close()

	if err := e.RegisterTool(echoSpec(t)); err != nil {
		t.Fatalf("register: %v", err)
	}

	result, err := e.Execute(context.Background(), "echo", map[string]any{
		"message": "hello world",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal result: %v (raw: %s)", err, result)
	}

	echoed, ok := parsed["echoed"].(string)
	if !ok || echoed != "hello world" {
		t.Errorf("echoed = %q, want %q", echoed, "hello world")
	}

	if _, ok := parsed["timestamp"]; !ok {
		t.Error("missing timestamp in result")
	}
}

func TestExecuteUnknown(t *testing.T) {
	e := NewV8Executor(nil, "", nil, nil)
	defer e.Close()

	_, err := e.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error = %q, want 'unknown tool'", err.Error())
	}
}

func TestLazyLoading(t *testing.T) {
	e := NewV8Executor(nil, "", nil, nil)
	defer e.Close()

	spec := echoSpec(t)
	if err := e.RegisterTool(spec); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Before execution, isolate should not be compiled.
	e.mu.Lock()
	entry := e.tools["echo"]
	e.mu.Unlock()

	entry.isolate.mu.Lock()
	compiled := entry.isolate.compiled
	entry.isolate.mu.Unlock()

	if compiled {
		t.Error("isolate should not be compiled before first Execute")
	}

	// Execute triggers compilation.
	if _, err := e.Execute(context.Background(), "echo", map[string]any{"message": "test"}); err != nil {
		t.Fatalf("execute: %v", err)
	}

	entry.isolate.mu.Lock()
	compiled = entry.isolate.compiled
	entry.isolate.mu.Unlock()

	if !compiled {
		t.Error("isolate should be compiled after Execute")
	}
}

func TestTimeout(t *testing.T) {
	e := NewV8Executor(nil, "", nil, nil)
	defer e.Close()

	m := loadTestManifest(t, "timeout")
	spec := ToolSpec{
		AgentName:    "timeout-agent",
		FunctionName: "loop",
		SourcePath:   testFixturePath(t, "timeout", "index.js"),
		Manifest:     m,
	}
	if err := e.RegisterTool(spec); err != nil {
		t.Fatalf("register: %v", err)
	}

	start := time.Now()
	_, err := e.Execute(context.Background(), "loop", map[string]any{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error = %q, want 'timed out'", err.Error())
	}
	// Should complete within a reasonable margin of the 100ms timeout.
	if elapsed > 5*time.Second {
		t.Errorf("timeout took %v, expected ~100ms", elapsed)
	}
}

func TestJSException(t *testing.T) {
	e := NewV8Executor(nil, "", nil, nil)
	defer e.Close()

	m := loadTestManifest(t, "error")
	spec := ToolSpec{
		AgentName:    "error-agent",
		FunctionName: "fail",
		SourcePath:   testFixturePath(t, "error", "index.js"),
		Manifest:     m,
	}
	if err := e.RegisterTool(spec); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := e.Execute(context.Background(), "fail", map[string]any{
		"reason": "testing errors",
	})
	if err == nil {
		t.Fatal("expected JS error")
	}
	if !strings.Contains(err.Error(), "deliberate failure: testing errors") {
		t.Errorf("error = %q, want to contain 'deliberate failure: testing errors'", err.Error())
	}
}

func TestHotReload(t *testing.T) {
	// Copy the echo fixture to a temp dir so we can modify it.
	tmpDir := t.TempDir()
	srcPath := filepath.Join(tmpDir, "index.js")

	// Write initial version.
	initial := `function echo(input) { return { echoed: input.message, version: 1 }; }`
	if err := os.WriteFile(srcPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	e := NewV8Executor(nil, "", nil, nil)
	defer e.Close()

	spec := ToolSpec{
		AgentName:    "echo-agent",
		FunctionName: "echo",
		SourcePath:   srcPath,
		Manifest:     loadTestManifest(t, "echo"),
	}
	if err := e.RegisterTool(spec); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Execute v1.
	result1, err := e.Execute(context.Background(), "echo", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatalf("execute v1: %v", err)
	}
	if !strings.Contains(result1, `"version":1`) {
		t.Errorf("v1 result = %s, want version:1", result1)
	}

	// Ensure filesystem timestamp granularity is respected.
	time.Sleep(50 * time.Millisecond)

	// Write updated version.
	updated := `function echo(input) { return { echoed: input.message, version: 2 }; }`
	if err := os.WriteFile(srcPath, []byte(updated), 0o644); err != nil {
		t.Fatalf("write updated: %v", err)
	}

	// Execute v2 — should hot reload.
	result2, err := e.Execute(context.Background(), "echo", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatalf("execute v2: %v", err)
	}
	if !strings.Contains(result2, `"version":2`) {
		t.Errorf("v2 result = %s, want version:2", result2)
	}
}

func TestClose(t *testing.T) {
	e := NewV8Executor(nil, "", nil, nil)

	spec := echoSpec(t)
	if err := e.RegisterTool(spec); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Compile the tool.
	if _, err := e.Execute(context.Background(), "echo", map[string]any{"message": "test"}); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Close should not panic.
	e.Close()

	// Double close should not panic.
	e.Close()
}

func TestContextCancellation(t *testing.T) {
	e := NewV8Executor(nil, "", nil, nil)
	defer e.Close()

	// Use timeout fixture (infinite loop) but cancel via context.
	m := loadTestManifest(t, "timeout")
	// Override timeout to be long — we want context cancel to win.
	m.TimeoutDuration = 10 * time.Second
	spec := ToolSpec{
		AgentName:    "timeout-agent",
		FunctionName: "loop",
		SourcePath:   testFixturePath(t, "timeout", "index.js"),
		Manifest:     m,
	}
	if err := e.RegisterTool(spec); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := e.Execute(ctx, "loop", map[string]any{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("error = %q, want 'cancelled'", err.Error())
	}
	if elapsed > 5*time.Second {
		t.Errorf("cancellation took %v, expected ~200ms", elapsed)
	}
}

func TestIsolateIsolation(t *testing.T) {
	e := NewV8Executor(nil, "", nil, nil)
	defer e.Close()

	// Create two tools in separate temp files, each setting a global.
	tmpDir := t.TempDir()

	src1 := `var sharedGlobal = "tool1"; function check1(input) { return { value: sharedGlobal }; }`
	path1 := filepath.Join(tmpDir, "tool1.js")
	if err := os.WriteFile(path1, []byte(src1), 0o644); err != nil {
		t.Fatalf("write tool1: %v", err)
	}

	src2 := `function check2(input) { return { value: typeof sharedGlobal }; }`
	path2 := filepath.Join(tmpDir, "tool2.js")
	if err := os.WriteFile(path2, []byte(src2), 0o644); err != nil {
		t.Fatalf("write tool2: %v", err)
	}

	if err := e.RegisterTool(ToolSpec{
		AgentName: "agent1", FunctionName: "check1",
		SourcePath: path1, Manifest: manifest.Manifest{},
	}); err != nil {
		t.Fatalf("register check1: %v", err)
	}
	if err := e.RegisterTool(ToolSpec{
		AgentName: "agent2", FunctionName: "check2",
		SourcePath: path2, Manifest: manifest.Manifest{},
	}); err != nil {
		t.Fatalf("register check2: %v", err)
	}

	// Execute both.
	r1, err := e.Execute(context.Background(), "check1", map[string]any{})
	if err != nil {
		t.Fatalf("execute check1: %v", err)
	}
	r2, err := e.Execute(context.Background(), "check2", map[string]any{})
	if err != nil {
		t.Fatalf("execute check2: %v", err)
	}

	// Tool1 should see its own global.
	var parsed1 map[string]any
	if err := json.Unmarshal([]byte(r1), &parsed1); err != nil {
		t.Fatalf("unmarshal r1: %v", err)
	}
	if parsed1["value"] != "tool1" {
		t.Errorf("tool1 value = %v, want 'tool1'", parsed1["value"])
	}

	// Tool2 should NOT see tool1's global.
	var parsed2 map[string]any
	if err := json.Unmarshal([]byte(r2), &parsed2); err != nil {
		t.Fatalf("unmarshal r2: %v", err)
	}
	if parsed2["value"] != "undefined" {
		t.Errorf("tool2 value = %v, want 'undefined' (isolation breach!)", parsed2["value"])
	}
}
