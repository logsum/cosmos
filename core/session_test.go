package core

import (
	"cosmos/core/provider"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveSession_EmptyHistoryIsNoop(t *testing.T) {
	dir := t.TempDir()
	session := newTestSession(&mockProvider{}, nil, &mockNotifier{})
	err := SaveSession(session, nil, dir, "/projects/cosmos")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("expected no files for empty history, got %d", len(entries))
	}
}

func TestSaveAndLoadSession_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	prov := &mockProvider{calls: [][]provider.StreamChunk{textChunks("Reply!")}}
	notifier := &mockNotifier{}
	session := newTestSession(prov, nil, notifier)

	// Simulate a conversation
	err := session.processUserMessage(t.Context(), "Hello, world!")
	if err != nil {
		t.Fatalf("processUserMessage failed: %v", err)
	}

	tracker := NewTracker(nil, nil)
	err = SaveSession(session, tracker, dir, "/projects/cosmos")
	if err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// Verify file was created
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 session file, got %d", len(entries))
	}

	// Load it back
	filename := entries[0].Name()
	loaded, err := LoadSavedSession(dir, filename)
	if err != nil {
		t.Fatalf("LoadSavedSession failed: %v", err)
	}

	if loaded.Version != 1 {
		t.Errorf("expected version 1, got %d", loaded.Version)
	}
	if loaded.SessionID != "test-session-id" {
		t.Errorf("expected session ID 'test-session-id', got %q", loaded.SessionID)
	}
	if loaded.Model != "test-model" {
		t.Errorf("expected model 'test-model', got %q", loaded.Model)
	}
	if loaded.WorkDir != "/projects/cosmos" {
		t.Errorf("expected workDir '/projects/cosmos', got %q", loaded.WorkDir)
	}
	if len(loaded.History) != 2 {
		t.Fatalf("expected 2 history messages, got %d", len(loaded.History))
	}
	if loaded.Description != "Hello, world!" {
		t.Errorf("expected description 'Hello, world!', got %q", loaded.Description)
	}
}

func TestSaveSession_DescriptionTruncation(t *testing.T) {
	dir := t.TempDir()
	prov := &mockProvider{calls: [][]provider.StreamChunk{textChunks("OK")}}
	notifier := &mockNotifier{}
	session := newTestSession(prov, nil, notifier)

	// Long message > 100 runes
	longMsg := ""
	for range 110 {
		longMsg += "a"
	}
	err := session.processUserMessage(t.Context(), longMsg)
	if err != nil {
		t.Fatalf("processUserMessage failed: %v", err)
	}

	err = SaveSession(session, nil, dir, "/tmp/test")
	if err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	loaded, _ := LoadSavedSession(dir, entries[0].Name())
	if len([]rune(loaded.Description)) > 100 {
		t.Errorf("description should be ≤100 runes, got %d", len([]rune(loaded.Description)))
	}
}

func TestSaveSession_FilenameFromWorkDir(t *testing.T) {
	dir := t.TempDir()
	prov := &mockProvider{calls: [][]provider.StreamChunk{textChunks("OK")}}
	notifier := &mockNotifier{}
	session := newTestSession(prov, nil, notifier)

	err := session.processUserMessage(t.Context(), "hi")
	if err != nil {
		t.Fatalf("processUserMessage failed: %v", err)
	}

	err = SaveSession(session, nil, dir, "/home/user/my-project")
	if err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}

	name := entries[0].Name()
	if name[:11] != "my-project-" {
		t.Errorf("expected filename to start with 'my-project-', got %q", name)
	}
}

func TestSaveSession_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	prov := &mockProvider{calls: [][]provider.StreamChunk{textChunks("OK")}}
	notifier := &mockNotifier{}
	session := newTestSession(prov, nil, notifier)

	err := session.processUserMessage(t.Context(), "hi")
	if err != nil {
		t.Fatalf("processUserMessage failed: %v", err)
	}

	err = SaveSession(session, nil, dir, "/tmp/test")
	if err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	// No .tmp files should remain
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("stale temp file found: %s", e.Name())
		}
	}
}

func TestLoadSavedSession_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSavedSession(dir, "../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
	if got := err.Error(); got != "invalid session filename: path escapes sessions directory" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestLoadSavedSession_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{invalid"), 0600); err != nil {
		t.Fatalf("writing test fixture: %v", err)
	}
	_, err := LoadSavedSession(dir, "bad.json")
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestLoadSavedSession_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSavedSession(dir, "nonexistent.json")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestListSavedSessions_Empty(t *testing.T) {
	dir := t.TempDir()
	sessions, err := ListSavedSessions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestListSavedSessions_NonExistentDir(t *testing.T) {
	sessions, err := ListSavedSessions("/nonexistent/dir/12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sessions != nil {
		t.Errorf("expected nil for nonexistent dir, got %v", sessions)
	}
}

func TestListSavedSessions_SortedNewestFirst(t *testing.T) {
	dir := t.TempDir()

	// Write two session files with different timestamps
	old := SavedSession{
		Version:   1,
		SessionID: "old",
		SavedAt:   time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		History:   []provider.Message{{Role: provider.RoleUser, Content: "old"}},
	}
	recent := SavedSession{
		Version:   1,
		SessionID: "recent",
		SavedAt:   time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
		History:   []provider.Message{{Role: provider.RoleUser, Content: "new"}},
	}

	writeSession(t, dir, "old.json", old)
	writeSession(t, dir, "recent.json", recent)

	sessions, err := ListSavedSessions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	if sessions[0].Filename != "recent.json" {
		t.Errorf("expected newest first, got %q", sessions[0].Filename)
	}
}

func TestListSavedSessions_SkipsMalformed(t *testing.T) {
	dir := t.TempDir()

	// Write one valid and one malformed
	valid := SavedSession{Version: 1, SessionID: "ok", History: []provider.Message{}}
	writeSession(t, dir, "valid.json", valid)
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{garbage"), 0600); err != nil {
		t.Fatalf("writing test fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notjson.txt"), []byte("hello"), 0600); err != nil {
		t.Fatalf("writing test fixture: %v", err)
	}

	sessions, err := ListSavedSessions(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 valid session, got %d", len(sessions))
	}
}

// writeSession is a test helper that writes a SavedSession to a JSON file.
func writeSession(t *testing.T, dir, filename string, s SavedSession) {
	t.Helper()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		t.Fatalf("marshaling test session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), data, 0600); err != nil {
		t.Fatalf("writing test session: %v", err)
	}
}
