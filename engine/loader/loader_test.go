package loader

import (
	"context"
	"cosmos/engine/manifest"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_ValidAgent(t *testing.T) {
	result, err := Load("testdata", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// bad-manifest should be in errors, no-manifest silently skipped.
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(result.Errors), result.Errors)
	}

	// valid-agent has 2 functions: greet and add.
	if len(result.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(result.Tools))
	}

	// Tools should be sorted by agent name then function order.
	toolNames := make(map[string]bool)
	for _, td := range result.Tools {
		toolNames[td.Name] = true
	}
	if !toolNames["greet"] || !toolNames["add"] {
		t.Errorf("expected greet and add tools, got %v", result.Tools)
	}

	// Check AgentInfo.
	if len(result.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(result.Agents))
	}
	ai := result.Agents[0]
	if ai.Name != "valid-agent" {
		t.Errorf("expected agent name 'valid-agent', got %q", ai.Name)
	}
	if ai.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", ai.Version)
	}
	if ai.Source != "builtin" {
		t.Errorf("expected source 'builtin', got %q", ai.Source)
	}
	if len(ai.Functions) != 2 {
		t.Errorf("expected 2 functions, got %d", len(ai.Functions))
	}

	// Executor should be non-nil.
	if result.Executor == nil {
		t.Fatal("expected non-nil Executor")
	}
	defer result.Executor.Close()
}

func TestLoad_UserOverridesBuiltin(t *testing.T) {
	// Both dirs contain valid-agent; user should win.
	result, err := Load("testdata", "testdata", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	defer result.Executor.Close()

	// Should still have exactly 1 agent (user overrides builtin).
	if len(result.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(result.Agents))
	}
	if result.Agents[0].Source != "user" {
		t.Errorf("expected source 'user', got %q", result.Agents[0].Source)
	}
}

func TestLoad_BadManifest(t *testing.T) {
	// Create a temp dir with only the bad-manifest agent.
	tmpDir := t.TempDir()
	badDir := filepath.Join(tmpDir, "bad-agent")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "cosmo.manifest.json"), []byte(`{"invalid": true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "index.js"), []byte(`function x() {}`), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := Load(tmpDir, "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	defer result.Executor.Close()

	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d", len(result.Errors))
	}
	if len(result.Agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(result.Agents))
	}
	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(result.Tools))
	}
}

func TestLoad_EmptyDirs(t *testing.T) {
	result, err := Load("/nonexistent/builtin", "/nonexistent/user", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	defer result.Executor.Close()

	if len(result.Agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(result.Agents))
	}
	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(result.Tools))
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected 0 errors, got %d", len(result.Errors))
	}
}

func TestFunctionToToolDef(t *testing.T) {
	fn := manifest.FunctionDef{
		Name:        "search",
		Description: "Search for files",
		Params: map[string]manifest.ParamDef{
			"query":    {Type: "string", Required: true, Description: "Search query"},
			"maxItems": {Type: "number", Required: false, Description: "Max results"},
		},
		Returns: manifest.ReturnDef{Type: "array", Description: "Matched files"},
	}

	td := functionToToolDef(fn)

	if td.Name != "search" {
		t.Errorf("expected name 'search', got %q", td.Name)
	}
	if td.Description != "Search for files" {
		t.Errorf("expected description 'Search for files', got %q", td.Description)
	}

	schema := td.InputSchema
	if schema["type"] != "object" {
		t.Errorf("expected schema type 'object', got %v", schema["type"])
	}

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties to be map[string]any")
	}
	if len(props) != 2 {
		t.Errorf("expected 2 properties, got %d", len(props))
	}

	queryProp, ok := props["query"].(map[string]any)
	if !ok {
		t.Fatalf("expected query property to be map[string]any")
	}
	if queryProp["type"] != "string" {
		t.Errorf("expected query type 'string', got %v", queryProp["type"])
	}
	if queryProp["description"] != "Search query" {
		t.Errorf("expected query description 'Search query', got %v", queryProp["description"])
	}

	// Check required array: only "query" is required, sorted.
	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("expected required to be []string")
	}
	if len(required) != 1 || required[0] != "query" {
		t.Errorf("expected required=['query'], got %v", required)
	}
}

func TestFunctionToToolDef_NoParams(t *testing.T) {
	fn := manifest.FunctionDef{
		Name:        "status",
		Description: "Get status",
		Params:      nil,
		Returns:     manifest.ReturnDef{Type: "object"},
	}

	td := functionToToolDef(fn)

	schema := td.InputSchema
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties to be map[string]any")
	}
	if len(props) != 0 {
		t.Errorf("expected 0 properties, got %d", len(props))
	}

	// No required field when no params are required.
	if _, exists := schema["required"]; exists {
		t.Error("expected no 'required' key when no params are required")
	}
}

func TestExecuteLoadedTool(t *testing.T) {
	result, err := Load("testdata", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	defer result.Executor.Close()

	// Execute the greet function.
	ctx := context.Background()
	output, err := result.Executor.Execute(ctx, "greet", map[string]any{"name": "World"})
	if err != nil {
		t.Fatalf("Execute greet failed: %v", err)
	}

	expected := `"Hello, World!"`
	if output != expected {
		t.Errorf("expected %q, got %q", expected, output)
	}

	// Execute the add function.
	output, err = result.Executor.Execute(ctx, "add", map[string]any{"a": 3.0, "b": 4.0})
	if err != nil {
		t.Fatalf("Execute add failed: %v", err)
	}

	if output != "7" {
		t.Errorf("expected '7', got %q", output)
	}
}

func TestLoad_MissingEntryFile(t *testing.T) {
	// Agent with valid manifest but entry file does not exist.
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "missing-entry")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{
		"name": "missing-entry",
		"version": "1.0.0",
		"entry": "index.js",
		"functions": [{"name": "foo", "description": "test", "params": {}, "returns": {"type": "string"}}],
		"permissions": {"storage:read": "allow"}
	}`
	if err := os.WriteFile(filepath.Join(agentDir, "cosmo.manifest.json"), []byte(manifestJSON), 0644); err != nil {
		t.Fatal(err)
	}
	// Intentionally do NOT create index.js.

	result, err := Load(tmpDir, "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	defer result.Executor.Close()

	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(result.Tools))
	}
	if len(result.Agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(result.Agents))
	}
}

func TestLoad_PathTraversalEntry(t *testing.T) {
	// Agent with entry file that tries to escape agent directory.
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, "evil-agent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifestJSON := `{
		"name": "evil-agent",
		"version": "1.0.0",
		"entry": "../../../etc/passwd",
		"functions": [{"name": "hack", "description": "bad", "params": {}, "returns": {"type": "string"}}],
		"permissions": {"storage:read": "allow"}
	}`
	if err := os.WriteFile(filepath.Join(agentDir, "cosmo.manifest.json"), []byte(manifestJSON), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := Load(tmpDir, "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	defer result.Executor.Close()

	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 error, got %d: %v", len(result.Errors), result.Errors)
	}
	if len(result.Tools) != 0 {
		t.Errorf("expected 0 tools, got %d", len(result.Tools))
	}
}

func TestDiscoverAgents(t *testing.T) {
	agents := discoverAgents("testdata", "builtin")

	// Should find valid-agent and bad-manifest (both have cosmo.manifest.json).
	// no-manifest should be skipped (no manifest file).
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d: %v", len(agents), agents)
	}

	if _, ok := agents["valid-agent"]; !ok {
		t.Error("expected valid-agent to be discovered")
	}
	if _, ok := agents["bad-manifest"]; !ok {
		t.Error("expected bad-manifest to be discovered")
	}
	if _, ok := agents["no-manifest"]; ok {
		t.Error("expected no-manifest to NOT be discovered")
	}

	for _, entry := range agents {
		if entry.source != "builtin" {
			t.Errorf("expected source 'builtin', got %q", entry.source)
		}
	}
}

func TestDiscoverAgents_EmptyDir(t *testing.T) {
	agents := discoverAgents("/nonexistent", "builtin")
	if len(agents) != 0 {
		t.Errorf("expected 0 agents from nonexistent dir, got %d", len(agents))
	}

	agents = discoverAgents("", "builtin")
	if len(agents) != 0 {
		t.Errorf("expected 0 agents from empty dir, got %d", len(agents))
	}
}
