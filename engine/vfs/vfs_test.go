package vfs

import (
	"os"
	"path/filepath"
	"testing"
)

// mustMkdir creates a directory or fails the test.
func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
}

// mustWrite creates a file or fails the test.
func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func TestSnapshotExistingFile(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")
	workDir := filepath.Join(dir, "work")
	mustMkdir(t, workDir)

	filePath := filepath.Join(workDir, "hello.txt")
	mustWrite(t, filePath, "original content")

	snap, err := NewSnapshotter(cosmosDir, "session-1")
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}

	snap.SetSnapshotContext("interaction-1", "tool-call-1")
	rec, err := snap.Snapshot(filePath, "write", "test-agent")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if rec.WasNewFile {
		t.Error("expected WasNewFile=false for existing file")
	}
	if rec.ContentHash == "" {
		t.Error("expected non-empty ContentHash")
	}
	if rec.InteractionID != "interaction-1" {
		t.Errorf("expected interaction-1, got %q", rec.InteractionID)
	}
	if rec.ToolCallID != "tool-call-1" {
		t.Errorf("expected tool-call-1, got %q", rec.ToolCallID)
	}
	if rec.FileMode != 0o644 {
		t.Errorf("expected FileMode 0644, got %o", rec.FileMode)
	}

	// Verify blob exists.
	blobPath := filepath.Join(cosmosDir, "snapshots", "session-1", rec.ContentHash)
	data, err := os.ReadFile(blobPath)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if string(data) != "original content" {
		t.Errorf("blob content mismatch: got %q", string(data))
	}

	// Verify records.
	recs := snap.Records()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs))
	}
}

func TestSnapshotNewFile(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")

	snap, err := NewSnapshotter(cosmosDir, "session-2")
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}

	snap.SetSnapshotContext("i1", "tc1")
	filePath := filepath.Join(dir, "nonexistent.txt")
	rec, err := snap.Snapshot(filePath, "write", "test-agent")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if !rec.WasNewFile {
		t.Error("expected WasNewFile=true for non-existent file")
	}
	if rec.ContentHash != "" {
		t.Errorf("expected empty ContentHash for new file, got %q", rec.ContentHash)
	}
}

func TestContentDeduplication(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")
	workDir := filepath.Join(dir, "work")
	mustMkdir(t, workDir)

	file1 := filepath.Join(workDir, "a.txt")
	file2 := filepath.Join(workDir, "b.txt")
	mustWrite(t, file1, "same content")
	mustWrite(t, file2, "same content")

	snap, err := NewSnapshotter(cosmosDir, "session-3")
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}

	snap.SetSnapshotContext("i1", "tc1")
	rec1, err := snap.Snapshot(file1, "write", "agent")
	if err != nil {
		t.Fatalf("Snapshot file1: %v", err)
	}
	rec2, err := snap.Snapshot(file2, "write", "agent")
	if err != nil {
		t.Fatalf("Snapshot file2: %v", err)
	}

	if rec1.ContentHash != rec2.ContentHash {
		t.Error("expected same content hash for identical files")
	}

	// Only one blob should exist (deduplication).
	sessionDir := filepath.Join(cosmosDir, "snapshots", "session-3")
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	blobCount := 0
	for _, e := range entries {
		if e.Name() != "manifest.jsonl" {
			blobCount++
		}
	}
	if blobCount != 1 {
		t.Errorf("expected 1 blob (dedup), got %d", blobCount)
	}
}

func TestRestoreInteraction(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")
	workDir := filepath.Join(dir, "work")
	mustMkdir(t, workDir)

	filePath := filepath.Join(workDir, "restore-me.txt")
	mustWrite(t, filePath, "original")

	snap, err := NewSnapshotter(cosmosDir, "session-4")
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}

	snap.SetSnapshotContext("i1", "tc1")
	if _, err := snap.Snapshot(filePath, "write", "agent"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	mustWrite(t, filePath, "modified")

	data, _ := os.ReadFile(filePath)
	if string(data) != "modified" {
		t.Fatalf("expected modified content, got %q", string(data))
	}

	restored, err := snap.RestoreInteraction("i1")
	if err != nil {
		t.Fatalf("RestoreInteraction: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}

	data, _ = os.ReadFile(filePath)
	if string(data) != "original" {
		t.Errorf("expected 'original', got %q", string(data))
	}
}

func TestRestoreNewFile(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")
	workDir := filepath.Join(dir, "work")
	mustMkdir(t, workDir)

	filePath := filepath.Join(workDir, "new-file.txt")

	snap, err := NewSnapshotter(cosmosDir, "session-5")
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}

	snap.SetSnapshotContext("i1", "tc1")
	if _, err := snap.Snapshot(filePath, "write", "agent"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	mustWrite(t, filePath, "new content")

	restored, err := snap.RestoreInteraction("i1")
	if err != nil {
		t.Fatalf("RestoreInteraction: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}

	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Error("expected file to be deleted after restore")
	}
}

func TestManifestPersistence(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")
	workDir := filepath.Join(dir, "work")
	mustMkdir(t, workDir)

	filePath := filepath.Join(workDir, "persist.txt")
	mustWrite(t, filePath, "content")

	snap1, err := NewSnapshotter(cosmosDir, "session-6")
	if err != nil {
		t.Fatalf("NewSnapshotter 1: %v", err)
	}

	snap1.SetSnapshotContext("i1", "tc1")
	if _, err := snap1.Snapshot(filePath, "write", "agent"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	snap2, err := NewSnapshotter(cosmosDir, "session-6")
	if err != nil {
		t.Fatalf("NewSnapshotter 2: %v", err)
	}

	recs := snap2.Records()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record from manifest, got %d", len(recs))
	}
	if recs[0].Path != filePath {
		t.Errorf("expected path %q, got %q", filePath, recs[0].Path)
	}
}

func TestRestoreInteraction_NotFound(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")

	snap, err := NewSnapshotter(cosmosDir, "session-7")
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}

	_, err = snap.RestoreInteraction("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent interaction")
	}
}

func TestReadSnapshotContent(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")
	workDir := filepath.Join(dir, "work")
	mustMkdir(t, workDir)

	filePath := filepath.Join(workDir, "readable.txt")
	mustWrite(t, filePath, "read me")

	snap, err := NewSnapshotter(cosmosDir, "session-8")
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}

	snap.SetSnapshotContext("i1", "tc1")
	rec, err := snap.Snapshot(filePath, "write", "agent")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	data, err := snap.ReadSnapshotContent(rec.ContentHash)
	if err != nil {
		t.Fatalf("ReadSnapshotContent: %v", err)
	}
	if string(data) != "read me" {
		t.Errorf("expected 'read me', got %q", string(data))
	}
}

func TestMultipleFilesInInteraction(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")
	workDir := filepath.Join(dir, "work")
	mustMkdir(t, workDir)

	file1 := filepath.Join(workDir, "a.txt")
	file2 := filepath.Join(workDir, "b.txt")
	mustWrite(t, file1, "original-a")
	mustWrite(t, file2, "original-b")

	snap, err := NewSnapshotter(cosmosDir, "session-9")
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}

	snap.SetSnapshotContext("i1", "tc1")
	if _, err := snap.Snapshot(file1, "write", "agent"); err != nil {
		t.Fatalf("Snapshot file1: %v", err)
	}
	snap.SetSnapshotContext("i1", "tc2")
	if _, err := snap.Snapshot(file2, "write", "agent"); err != nil {
		t.Fatalf("Snapshot file2: %v", err)
	}

	mustWrite(t, file1, "modified-a")
	mustWrite(t, file2, "modified-b")

	restored, err := snap.RestoreInteraction("i1")
	if err != nil {
		t.Fatalf("RestoreInteraction: %v", err)
	}
	if len(restored) != 2 {
		t.Fatalf("expected 2 restored files, got %d", len(restored))
	}

	data1, _ := os.ReadFile(file1)
	data2, _ := os.ReadFile(file2)
	if string(data1) != "original-a" {
		t.Errorf("expected 'original-a', got %q", string(data1))
	}
	if string(data2) != "original-b" {
		t.Errorf("expected 'original-b', got %q", string(data2))
	}
}

func TestMultipleSnapshotsSameFile(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")
	workDir := filepath.Join(dir, "work")
	mustMkdir(t, workDir)

	filePath := filepath.Join(workDir, "multi.txt")
	mustWrite(t, filePath, "v1")

	snap, err := NewSnapshotter(cosmosDir, "session-10")
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}

	// First snapshot (captures v1).
	snap.SetSnapshotContext("i1", "tc1")
	if _, err := snap.Snapshot(filePath, "write", "agent"); err != nil {
		t.Fatalf("Snapshot v1: %v", err)
	}

	mustWrite(t, filePath, "v2")

	// Second snapshot (captures v2) — same interaction, same file.
	snap.SetSnapshotContext("i1", "tc2")
	if _, err := snap.Snapshot(filePath, "write", "agent"); err != nil {
		t.Fatalf("Snapshot v2: %v", err)
	}

	mustWrite(t, filePath, "v3")

	// Restore should bring back v1 (the first/earliest snapshot).
	restored, err := snap.RestoreInteraction("i1")
	if err != nil {
		t.Fatalf("RestoreInteraction: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file (deduplicated), got %d", len(restored))
	}

	data, _ := os.ReadFile(filePath)
	if string(data) != "v1" {
		t.Errorf("expected 'v1' (original), got %q", string(data))
	}
}

func TestRestorePreservesFileMode(t *testing.T) {
	dir := t.TempDir()
	cosmosDir := filepath.Join(dir, ".cosmos")
	workDir := filepath.Join(dir, "work")
	mustMkdir(t, workDir)

	filePath := filepath.Join(workDir, "script.sh")
	if err := os.WriteFile(filePath, []byte("#!/bin/sh\necho hi"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	snap, err := NewSnapshotter(cosmosDir, "session-11")
	if err != nil {
		t.Fatalf("NewSnapshotter: %v", err)
	}

	snap.SetSnapshotContext("i1", "tc1")
	rec, err := snap.Snapshot(filePath, "write", "agent")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if rec.FileMode != 0o755 {
		t.Errorf("expected FileMode 0755, got %o", rec.FileMode)
	}

	// Overwrite with different perms.
	if err := os.WriteFile(filePath, []byte("modified"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	restored, err := snap.RestoreInteraction("i1")
	if err != nil {
		t.Fatalf("RestoreInteraction: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("expected 1 restored file, got %d", len(restored))
	}

	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("expected restored mode 0755, got %o", info.Mode().Perm())
	}

	data, _ := os.ReadFile(filePath)
	if string(data) != "#!/bin/sh\necho hi" {
		t.Errorf("expected original content, got %q", string(data))
	}
}
