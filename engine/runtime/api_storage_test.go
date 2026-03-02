package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cosmos/engine/manifest"
)

func TestStorageGetSet(t *testing.T) {
	storageDir := t.TempDir()

	permissions := map[string]manifest.PermissionMode{
		"storage:read":  manifest.PermissionAllow,
		"storage:write": manifest.PermissionAllow,
	}
	toolCtx := testToolContext(t, "storage-agent", permissions)
	toolCtx.StorageDir = storageDir

	e := NewV8Executor(toolCtx.Evaluator, storageDir, nil, nil)
	defer e.Close()

	tmpDir := t.TempDir()

	// Register storage set function.
	setPath := filepath.Join(tmpDir, "set.js")
	if err := os.WriteFile(setPath, []byte(`function storageSet(input) { storage.set(input.key, input.value); return { ok: true }; }`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := e.RegisterTool(ToolSpec{
		AgentName: "storage-agent", FunctionName: "storageSet",
		SourcePath: setPath, Manifest: toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register set: %v", err)
	}

	// Register storage get function.
	getPath := filepath.Join(tmpDir, "get.js")
	if err := os.WriteFile(getPath, []byte(`function storageGet(input) { return { value: storage.get(input.key) }; }`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := e.RegisterTool(ToolSpec{
		AgentName: "storage-agent", FunctionName: "storageGet",
		SourcePath: getPath, Manifest: toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register get: %v", err)
	}

	// Set a value.
	_, err := e.Execute(context.Background(), "storageSet", map[string]any{
		"key":   "name",
		"value": "cosmos",
	})
	if err != nil {
		t.Fatalf("storage set: %v", err)
	}

	// Get the value back.
	result, err := e.Execute(context.Background(), "storageGet", map[string]any{
		"key": "name",
	})
	if err != nil {
		t.Fatalf("storage get: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, result)
	}

	if parsed["value"] != "cosmos" {
		t.Errorf("value = %v, want %q", parsed["value"], "cosmos")
	}

	// Verify the storage file was created on disk.
	storagePath := filepath.Join(storageDir, "storage-agent.json")
	data, err := os.ReadFile(storagePath)
	if err != nil {
		t.Fatalf("read storage file: %v", err)
	}
	if !strings.Contains(string(data), `"name"`) || !strings.Contains(string(data), `"cosmos"`) {
		t.Errorf("storage file content = %s, expected name:cosmos", string(data))
	}
}

func TestStorageGet_Missing(t *testing.T) {
	storageDir := t.TempDir()

	permissions := map[string]manifest.PermissionMode{
		"storage:read": manifest.PermissionAllow,
	}
	toolCtx := testToolContext(t, "storage-agent", permissions)
	toolCtx.StorageDir = storageDir

	e := NewV8Executor(toolCtx.Evaluator, storageDir, nil, nil)
	defer e.Close()

	tmpDir := t.TempDir()
	getPath := filepath.Join(tmpDir, "get.js")
	if err := os.WriteFile(getPath, []byte(`function storageGetMissing(input) { return { value: storage.get(input.key) }; }`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := e.RegisterTool(ToolSpec{
		AgentName: "storage-agent", FunctionName: "storageGetMissing",
		SourcePath: getPath, Manifest: toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	result, err := e.Execute(context.Background(), "storageGetMissing", map[string]any{
		"key": "nonexistent",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed["value"] != nil {
		t.Errorf("value = %v, want nil", parsed["value"])
	}
}

func TestStorage_PermissionDenied(t *testing.T) {
	storageDir := t.TempDir()

	permissions := map[string]manifest.PermissionMode{
		"storage:read":  manifest.PermissionDeny,
		"storage:write": manifest.PermissionDeny,
	}
	toolCtx := testToolContext(t, "storage-agent", permissions)
	toolCtx.StorageDir = storageDir

	e := NewV8Executor(toolCtx.Evaluator, storageDir, nil, nil)
	defer e.Close()

	tmpDir := t.TempDir()

	// Test read denied.
	getPath := filepath.Join(tmpDir, "get.js")
	if err := os.WriteFile(getPath, []byte(`function storageDeniedGet(input) { return { value: storage.get(input.key) }; }`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := e.RegisterTool(ToolSpec{
		AgentName: "storage-agent", FunctionName: "storageDeniedGet",
		SourcePath: getPath, Manifest: toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err := e.Execute(context.Background(), "storageDeniedGet", map[string]any{"key": "test"})
	if err == nil {
		t.Fatal("expected permission denied for storage.get")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want permission denied", err.Error())
	}

	// Test write denied.
	setPath := filepath.Join(tmpDir, "set.js")
	if err := os.WriteFile(setPath, []byte(`function storageDeniedSet(input) { storage.set(input.key, input.value); return { ok: true }; }`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := e.RegisterTool(ToolSpec{
		AgentName: "storage-agent", FunctionName: "storageDeniedSet",
		SourcePath: setPath, Manifest: toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	_, err = e.Execute(context.Background(), "storageDeniedSet", map[string]any{"key": "test", "value": "val"})
	if err == nil {
		t.Fatal("expected permission denied for storage.set")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want permission denied", err.Error())
	}
}
