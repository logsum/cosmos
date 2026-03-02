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

// fsTestExecutor creates a V8Executor with a registered fs-agent tool
// that has permissions scoped to the given directory.
func fsTestExecutor(t *testing.T, funcName, allowDir string) (*V8Executor, *ToolContext) {
	t.Helper()

	// Resolve symlinks in allowDir so permissions match the resolved paths
	// that the fs API now checks (e.g., /var → /private/var on macOS).
	resolved, err := filepath.EvalSymlinks(allowDir)
	if err == nil {
		allowDir = resolved
	}

	permissions := map[string]manifest.PermissionMode{
		"fs:read:" + allowDir + "/**":  manifest.PermissionAllow,
		"fs:write:" + allowDir + "/**": manifest.PermissionAllow,
		"fs:write":                     manifest.PermissionDeny,
		"fs:read":                      manifest.PermissionDeny,
	}

	toolCtx := testToolContext(t, "fs-agent", permissions)

	e := NewV8Executor(toolCtx.Evaluator, toolCtx.StorageDir, nil, nil)
	t.Cleanup(func() { e.Close() })

	srcPath := testFixturePath(t, "fs-agent", "index.js")

	if err := e.RegisterTool(ToolSpec{
		AgentName:    "fs-agent",
		FunctionName: funcName,
		SourcePath:   srcPath,
		Manifest:     toolCtx.Manifest,
	}); err != nil {
		t.Fatalf("register %s: %v", funcName, err)
	}

	return e, toolCtx
}

func TestFsRead(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("hello from cosmos"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	e, _ := fsTestExecutor(t, "readFile", tmpDir)

	result, err := e.Execute(context.Background(), "readFile", map[string]any{
		"path": testFile,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal: %v (raw: %s)", err, result)
	}

	content, ok := parsed["content"].(string)
	if !ok || content != "hello from cosmos" {
		t.Errorf("content = %q, want %q", content, "hello from cosmos")
	}
}

func TestFsRead_PermissionDenied(t *testing.T) {
	tmpDir := t.TempDir()
	outsideFile := filepath.Join(os.TempDir(), "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	defer os.Remove(outsideFile)

	e, _ := fsTestExecutor(t, "readFile", tmpDir)

	_, err := e.Execute(context.Background(), "readFile", map[string]any{
		"path": outsideFile,
	})
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want permission denied", err.Error())
	}
}

func TestFsWrite(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "output.txt")

	e, _ := fsTestExecutor(t, "writeFile", tmpDir)

	result, err := e.Execute(context.Background(), "writeFile", map[string]any{
		"path":    testFile,
		"content": "written by v8",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !strings.Contains(result, `"ok":true`) {
		t.Errorf("result = %s, want ok:true", result)
	}

	// Verify file was written.
	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "written by v8" {
		t.Errorf("file content = %q, want %q", string(data), "written by v8")
	}
}

func TestFsWrite_PermissionDenied(t *testing.T) {
	tmpDir := t.TempDir()
	outsideFile := filepath.Join(os.TempDir(), "outside-write.txt")
	defer os.Remove(outsideFile)

	e, _ := fsTestExecutor(t, "writeFile", tmpDir)

	_, err := e.Execute(context.Background(), "writeFile", map[string]any{
		"path":    outsideFile,
		"content": "should not be written",
	})
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want permission denied", err.Error())
	}

	// File should not exist.
	if _, err := os.Stat(outsideFile); err == nil {
		t.Error("file should not have been created outside allowed directory")
	}
}

func TestFsList(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Mkdir(filepath.Join(tmpDir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	e, _ := fsTestExecutor(t, "listDir", tmpDir)

	result, err := e.Execute(context.Background(), "listDir", map[string]any{
		"path": tmpDir,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	entries, ok := parsed["entries"].([]any)
	if !ok {
		t.Fatalf("entries is not an array: %T", parsed["entries"])
	}
	if len(entries) != 2 {
		t.Errorf("entries count = %d, want 2", len(entries))
	}

	// Check that one is a dir and one is a file.
	hasFile, hasDir := false, false
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if entry["name"] == "a.txt" && entry["isDir"] == false {
			hasFile = true
		}
		if entry["name"] == "subdir" && entry["isDir"] == true {
			hasDir = true
		}
	}
	if !hasFile {
		t.Error("missing file entry a.txt")
	}
	if !hasDir {
		t.Error("missing dir entry subdir")
	}
}

func TestFsStat(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "stat-target.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	e, _ := fsTestExecutor(t, "statFile", tmpDir)

	result, err := e.Execute(context.Background(), "statFile", map[string]any{
		"path": testFile,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if parsed["name"] != "stat-target.txt" {
		t.Errorf("name = %v, want %q", parsed["name"], "stat-target.txt")
	}
	if size, ok := parsed["size"].(float64); !ok || size != 5 {
		t.Errorf("size = %v, want 5", parsed["size"])
	}
	if parsed["isDir"] != false {
		t.Errorf("isDir = %v, want false", parsed["isDir"])
	}
	if _, ok := parsed["modTime"].(string); !ok {
		t.Error("modTime should be a string")
	}
}

func TestFsRead_SymlinkEscape(t *testing.T) {
	allowedDir := t.TempDir()
	outsideDir := t.TempDir()

	// Create a secret file outside the allowed scope.
	secretFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("top secret"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	// Create a symlink inside the allowed directory pointing outside.
	symlinkPath := filepath.Join(allowedDir, "escape.txt")
	if err := os.Symlink(secretFile, symlinkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	e, _ := fsTestExecutor(t, "readFile", allowedDir)

	// Reading via the symlink should be denied — the resolved path is outside scope.
	_, err := e.Execute(context.Background(), "readFile", map[string]any{
		"path": symlinkPath,
	})
	if err == nil {
		t.Fatal("expected permission denied for symlink escape")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want permission denied", err.Error())
	}
}

func TestFsWrite_SymlinkEscape(t *testing.T) {
	allowedDir := t.TempDir()
	outsideDir := t.TempDir()

	// Create a symlink inside the allowed directory pointing to an outside file.
	outsideFile := filepath.Join(outsideDir, "target.txt")
	if err := os.WriteFile(outsideFile, []byte("original"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	symlinkPath := filepath.Join(allowedDir, "escape.txt")
	if err := os.Symlink(outsideFile, symlinkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	e, _ := fsTestExecutor(t, "writeFile", allowedDir)

	_, err := e.Execute(context.Background(), "writeFile", map[string]any{
		"path":    symlinkPath,
		"content": "overwritten",
	})
	if err == nil {
		t.Fatal("expected permission denied for symlink write escape")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want permission denied", err.Error())
	}

	// Verify the outside file was NOT modified.
	data, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatalf("read outside file: %v", err)
	}
	if string(data) != "original" {
		t.Errorf("outside file was modified to %q", string(data))
	}
}

func TestFsUnlink(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "to-delete.txt")
	if err := os.WriteFile(testFile, []byte("bye"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	e, _ := fsTestExecutor(t, "deleteFile", tmpDir)

	result, err := e.Execute(context.Background(), "deleteFile", map[string]any{
		"path": testFile,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result, `"ok":true`) {
		t.Errorf("result = %s, want ok:true", result)
	}

	// Verify file was deleted.
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("file should have been deleted")
	}
}
