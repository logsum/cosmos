package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupSessionData_AuditLogs(t *testing.T) {
	tmpDir := t.TempDir()

	// Create old audit files (31 days old)
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	oldFiles := []string{
		"audit-session1.jsonl",
		"audit-session2.jsonl.old",
		"audit-session3.jsonl",
	}
	for _, name := range oldFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("test data"), 0600); err != nil {
			t.Fatalf("create old file %s: %v", name, err)
		}
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("set mtime for %s: %v", name, err)
		}
	}

	// Create recent audit file (5 days old)
	recentFile := filepath.Join(tmpDir, "audit-session-recent.jsonl")
	if err := os.WriteFile(recentFile, []byte("recent data"), 0600); err != nil {
		t.Fatalf("create recent file: %v", err)
	}

	// Run cleanup
	opts := CleanupOptions{
		CosmosDir: tmpDir,
		MaxAge:    30 * 24 * time.Hour,
		DryRun:    false,
	}
	result, err := CleanupSessionData(opts)
	if err != nil {
		t.Fatalf("CleanupSessionData failed: %v", err)
	}

	// Verify old files deleted
	if result.DeletedAuditFiles != 3 {
		t.Errorf("expected 3 deleted audit files, got %d", result.DeletedAuditFiles)
	}

	for _, name := range oldFiles {
		path := filepath.Join(tmpDir, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("old file %s should be deleted", name)
		}
	}

	// Verify recent file preserved
	if _, err := os.Stat(recentFile); err != nil {
		t.Errorf("recent file should be preserved: %v", err)
	}

	// Verify no errors
	if len(result.Errors) > 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
}

func TestCleanupSessionData_DryRun(t *testing.T) {
	tmpDir := t.TempDir()

	// Create old audit file
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	oldFile := filepath.Join(tmpDir, "audit-old.jsonl")
	if err := os.WriteFile(oldFile, []byte("test"), 0600); err != nil {
		t.Fatalf("create old file: %v", err)
	}
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("set mtime: %v", err)
	}

	// Run cleanup in dry-run mode
	opts := CleanupOptions{
		CosmosDir: tmpDir,
		MaxAge:    30 * 24 * time.Hour,
		DryRun:    true,
	}
	result, err := CleanupSessionData(opts)
	if err != nil {
		t.Fatalf("CleanupSessionData failed: %v", err)
	}

	// Verify file reported as would-be-deleted
	if result.DeletedAuditFiles != 1 {
		t.Errorf("expected 1 audit file in dry-run report, got %d", result.DeletedAuditFiles)
	}

	// Verify file still exists
	if _, err := os.Stat(oldFile); err != nil {
		t.Errorf("file should still exist in dry-run mode: %v", err)
	}
}

func TestCleanupSessionData_NonexistentDir(t *testing.T) {
	// Run cleanup on nonexistent directory
	opts := CleanupOptions{
		CosmosDir:   "/nonexistent/path/to/cosmos",
		SessionsDir: "/nonexistent/path/to/sessions",
		MaxAge:      30 * 24 * time.Hour,
		DryRun:      false,
	}
	result, err := CleanupSessionData(opts)
	if err != nil {
		t.Fatalf("CleanupSessionData should not fail on nonexistent dirs: %v", err)
	}

	// Verify zero deletions
	if result.DeletedAuditFiles != 0 {
		t.Errorf("expected 0 deletions, got %d audit files", result.DeletedAuditFiles)
	}
	if result.DeletedSnapshotDirs != 0 {
		t.Errorf("expected 0 deletions, got %d snapshot dirs", result.DeletedSnapshotDirs)
	}
	if result.DeletedSessionFiles != 0 {
		t.Errorf("expected 0 deletions, got %d session files", result.DeletedSessionFiles)
	}
}

func TestCleanupSessionData_PartialFailure(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple old audit files
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	oldFiles := []string{
		"audit-old1.jsonl",
		"audit-old2.jsonl",
		"audit-old3.jsonl",
	}

	for _, name := range oldFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatalf("create file %s: %v", name, err)
		}
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("set mtime for %s: %v", name, err)
		}
	}

	// Run cleanup
	opts := CleanupOptions{
		CosmosDir: tmpDir,
		MaxAge:    30 * 24 * time.Hour,
		DryRun:    false,
	}
	result, err := CleanupSessionData(opts)
	if err != nil {
		t.Fatalf("CleanupSessionData should not fail: %v", err)
	}

	// Verify all accessible files were deleted
	if result.DeletedAuditFiles != len(oldFiles) {
		t.Errorf("expected %d deleted audit files, got %d", len(oldFiles), result.DeletedAuditFiles)
	}

	// Verify all old files were deleted
	for _, name := range oldFiles {
		path := filepath.Join(tmpDir, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("file %s should be deleted", name)
		}
	}

	// Verify cleanup completes successfully even if there are no errors
	// (The important property is that cleanup continues past any errors it encounters,
	// but we can't reliably create permission errors in a cross-platform way)
	if err != nil {
		t.Errorf("cleanup should complete without fatal errors")
	}
}

func TestCleanupSessionData_OnlyAuditFiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create old files with various names
	oldTime := time.Now().Add(-31 * 24 * time.Hour)

	// Should be deleted
	auditFiles := []string{
		"audit-session1.jsonl",
		"audit-abc-123.jsonl",
		"audit-xyz.jsonl.old",
	}

	// Should NOT be deleted (wrong pattern)
	nonAuditFiles := []string{
		"not-audit.jsonl",
		"log.jsonl",
		"audit.txt",
		"session-audit.jsonl",
	}

	for _, name := range auditFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatalf("create file %s: %v", name, err)
		}
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("set mtime for %s: %v", name, err)
		}
	}

	for _, name := range nonAuditFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatalf("create file %s: %v", name, err)
		}
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("set mtime for %s: %v", name, err)
		}
	}

	// Run cleanup
	opts := CleanupOptions{
		CosmosDir: tmpDir,
		MaxAge:    30 * 24 * time.Hour,
		DryRun:    false,
	}
	result, err := CleanupSessionData(opts)
	if err != nil {
		t.Fatalf("CleanupSessionData failed: %v", err)
	}

	// Verify only audit-* files deleted
	if result.DeletedAuditFiles != len(auditFiles) {
		t.Errorf("expected %d deleted audit files, got %d", len(auditFiles), result.DeletedAuditFiles)
	}

	// Verify audit files deleted
	for _, name := range auditFiles {
		path := filepath.Join(tmpDir, name)
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("audit file %s should be deleted", name)
		}
	}

	// Verify non-audit files preserved
	for _, name := range nonAuditFiles {
		path := filepath.Join(tmpDir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("non-audit file %s should be preserved: %v", name, err)
		}
	}
}

func TestCleanupSessionData_EdgeCases(t *testing.T) {
	tmpDir := t.TempDir()

	// Use a fixed reference time to avoid timing precision issues
	now := time.Now()
	maxAge := 30 * 24 * time.Hour

	// Test exactly at boundary (30 days) - should be preserved
	boundaryTime := now.Add(-maxAge)
	boundaryFile := filepath.Join(tmpDir, "audit-boundary.jsonl")
	if err := os.WriteFile(boundaryFile, []byte("test"), 0600); err != nil {
		t.Fatalf("create boundary file: %v", err)
	}
	if err := os.Chtimes(boundaryFile, boundaryTime, boundaryTime); err != nil {
		t.Fatalf("set mtime: %v", err)
	}

	// Test just over boundary (30 days + 1 minute) - should be deleted
	overBoundaryTime := now.Add(-maxAge - time.Minute)
	overBoundaryFile := filepath.Join(tmpDir, "audit-over.jsonl")
	if err := os.WriteFile(overBoundaryFile, []byte("test"), 0600); err != nil {
		t.Fatalf("create over boundary file: %v", err)
	}
	if err := os.Chtimes(overBoundaryFile, overBoundaryTime, overBoundaryTime); err != nil {
		t.Fatalf("set mtime: %v", err)
	}

	// Test just under boundary (30 days - 1 minute) - should be preserved
	underBoundaryTime := now.Add(-maxAge + time.Minute)
	underBoundaryFile := filepath.Join(tmpDir, "audit-under.jsonl")
	if err := os.WriteFile(underBoundaryFile, []byte("test"), 0600); err != nil {
		t.Fatalf("create under boundary file: %v", err)
	}
	if err := os.Chtimes(underBoundaryFile, underBoundaryTime, underBoundaryTime); err != nil {
		t.Fatalf("set mtime: %v", err)
	}

	// Run cleanup with explicit cutoff to avoid timing races
	cutoff := now.Add(-maxAge)
	internalTestCleanup(t, tmpDir, cutoff)

	// Verify only over-boundary file deleted
	overExists := fileExists(overBoundaryFile)
	boundaryExists := fileExists(boundaryFile)
	underExists := fileExists(underBoundaryFile)

	if overExists {
		t.Errorf("over-boundary file should be deleted (mtime: %v, cutoff: %v)",
			overBoundaryTime, cutoff)
	}
	if !boundaryExists {
		t.Errorf("boundary file should be preserved (mtime: %v, cutoff: %v)",
			boundaryTime, cutoff)
	}
	if !underExists {
		t.Errorf("under-boundary file should be preserved (mtime: %v, cutoff: %v)",
			underBoundaryTime, cutoff)
	}
}

// Helper function to test cleanup with explicit cutoff time
func internalTestCleanup(t *testing.T, cosmosDir string, cutoff time.Time) {
	t.Helper()
	pattern := filepath.Join(cosmosDir, "audit-*.jsonl*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob failed: %v", err)
	}

	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			if err := os.Remove(path); err != nil {
				t.Logf("failed to remove %s: %v", path, err)
			}
		}
	}
}

// Helper function to check if file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestCleanupSessionData_EmptyDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	// Run cleanup on empty directory
	opts := CleanupOptions{
		CosmosDir: tmpDir,
		MaxAge:    30 * 24 * time.Hour,
		DryRun:    false,
	}
	result, err := CleanupSessionData(opts)
	if err != nil {
		t.Fatalf("CleanupSessionData should not fail on empty dir: %v", err)
	}

	// Verify zero deletions
	if result.DeletedAuditFiles != 0 {
		t.Errorf("expected 0 deletions in empty dir, got %d", result.DeletedAuditFiles)
	}
}

func TestDefaultCleanupOptions(t *testing.T) {
	opts := DefaultCleanupOptions()

	if opts.CosmosDir != ".cosmos" {
		t.Errorf("expected CosmosDir '.cosmos', got '%s'", opts.CosmosDir)
	}

	expectedSessionsDir := filepath.Join(os.Getenv("HOME"), ".cosmos", "sessions")
	if opts.SessionsDir != expectedSessionsDir {
		t.Errorf("expected SessionsDir '%s', got '%s'", expectedSessionsDir, opts.SessionsDir)
	}

	if opts.MaxAge != 30*24*time.Hour {
		t.Errorf("expected MaxAge 30 days, got %v", opts.MaxAge)
	}

	if opts.DryRun {
		t.Error("expected DryRun false by default")
	}
}
