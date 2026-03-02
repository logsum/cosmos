package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newTestChangelog(restoreFunc RestoreFunc) *ChangelogModel {
	return NewChangelogModel(nil, restoreFunc)
}

func sendEntry(m *ChangelogModel, msg ChangelogEntryMsg) *ChangelogModel {
	updated, _ := m.Update(msg)
	return updated.(*ChangelogModel)
}

func sendKeyType(m *ChangelogModel, kt tea.KeyType) (*ChangelogModel, tea.Cmd) {
	updated, cmd := m.Update(tea.KeyMsg{Type: kt})
	return updated.(*ChangelogModel), cmd
}

func TestChangelog_EmptyState(t *testing.T) {
	m := newTestChangelog(nil)
	view := m.View()
	if !strings.Contains(view, "No file changes recorded yet") {
		t.Error("expected empty state message in view")
	}
}

func TestChangelog_AddEntry(t *testing.T) {
	m := newTestChangelog(nil)
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i1",
		Timestamp:     "2026-03-01 10:00:00",
		Description:   "tool modified 1 file(s)",
		Files: []ChangelogFile{
			{Path: "/src/main.go", Operation: "write", WasNew: false},
		},
	})

	if len(m.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(m.entries))
	}
	if m.entries[0].interactionID != "i1" {
		t.Errorf("expected interactionID i1, got %q", m.entries[0].interactionID)
	}
	view := m.View()
	if strings.Contains(view, "No file changes") {
		t.Error("should not show empty message with entries")
	}
	if !strings.Contains(view, "tool modified 1 file(s)") {
		t.Error("expected description in view")
	}
}

func TestChangelog_MergeByInteractionID(t *testing.T) {
	m := newTestChangelog(nil)
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i1",
		Timestamp:     "2026-03-01 10:00:00",
		Description:   "tool modified 1 file(s)",
		Files:         []ChangelogFile{{Path: "/a.go", Operation: "write"}},
	})
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i1",
		Timestamp:     "2026-03-01 10:00:01",
		Description:   "tool modified 2 file(s)",
		Files:         []ChangelogFile{{Path: "/b.go", Operation: "write", WasNew: true}},
	})

	if len(m.entries) != 1 {
		t.Fatalf("expected 1 entry after merge, got %d", len(m.entries))
	}
	if len(m.entries[0].files) != 2 {
		t.Errorf("expected 2 files after merge, got %d", len(m.entries[0].files))
	}
	if m.entries[0].description != "tool modified 2 file(s)" {
		t.Errorf("expected description to update on merge, got %q", m.entries[0].description)
	}
}

func TestChangelog_PrependNewestFirst(t *testing.T) {
	m := newTestChangelog(nil)
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i1",
		Timestamp:     "2026-03-01 10:00:00",
		Description:   "first",
		Files:         []ChangelogFile{{Path: "/a.go", Operation: "write"}},
	})
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i2",
		Timestamp:     "2026-03-01 10:01:00",
		Description:   "second",
		Files:         []ChangelogFile{{Path: "/b.go", Operation: "write"}},
	})

	if len(m.entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(m.entries))
	}
	if m.entries[0].interactionID != "i2" {
		t.Error("expected newest entry at index 0")
	}
	if m.entries[1].interactionID != "i1" {
		t.Error("expected oldest entry at index 1")
	}
}

func TestChangelog_CursorShiftOnPrepend(t *testing.T) {
	m := newTestChangelog(nil)

	// First entry — cursor starts at 0.
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i1",
		Description:   "first",
		Files:         []ChangelogFile{{Path: "/a.go", Operation: "write"}},
	})
	if m.cursor != 0 {
		t.Errorf("expected cursor=0 after first entry, got %d", m.cursor)
	}

	// Second entry prepends — cursor should shift to 1 (still on "first").
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i2",
		Description:   "second",
		Files:         []ChangelogFile{{Path: "/b.go", Operation: "write"}},
	})
	if m.cursor != 1 {
		t.Errorf("expected cursor=1 after prepend, got %d", m.cursor)
	}
	if m.entries[m.cursor].interactionID != "i1" {
		t.Error("cursor should still point at the original entry")
	}
}

func TestChangelog_ExpandCollapse(t *testing.T) {
	m := newTestChangelog(nil)
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i1",
		Description:   "entry",
		Files:         []ChangelogFile{{Path: "/a.go", Operation: "write"}},
	})

	// Enter to expand.
	m, _ = sendKeyType(m, tea.KeyEnter)
	if !m.entries[0].expanded {
		t.Error("expected entry to be expanded after Enter")
	}

	// Enter again to collapse (cursor is on header, not restore).
	m.restoreFocused = false
	m, _ = sendKeyType(m, tea.KeyEnter)
	if m.entries[0].expanded {
		t.Error("expected entry to be collapsed after second Enter")
	}
}

func TestChangelog_RestoreFunc(t *testing.T) {
	var restoredID string
	rf := func(interactionID string) tea.Cmd {
		return func() tea.Msg {
			restoredID = interactionID
			return ChangelogRestoreResultMsg{
				InteractionID: interactionID,
				Success:       true,
				Message:       "Restored 1 file(s)",
			}
		}
	}

	m := newTestChangelog(rf)
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i1",
		Description:   "entry",
		Files:         []ChangelogFile{{Path: "/a.go", Operation: "write"}},
	})

	// Expand, navigate to restore button, press Enter.
	m, _ = sendKeyType(m, tea.KeyEnter) // expand
	m, _ = sendKeyType(m, tea.KeyDown)  // focus restore
	if !m.restoreFocused {
		t.Fatal("expected restoreFocused=true after down on expanded entry")
	}

	m, cmd := sendKeyType(m, tea.KeyEnter) // trigger restore
	if cmd == nil {
		t.Fatal("expected non-nil cmd from restore")
	}

	// Execute the command to verify it calls our restore func.
	msg := cmd()
	if restoredID != "i1" {
		t.Errorf("expected restore for i1, got %q", restoredID)
	}

	// Feed result back.
	result := msg.(ChangelogRestoreResultMsg)
	updated, _ := m.Update(result)
	m = updated.(*ChangelogModel)
	if !strings.Contains(m.message, "Restored") {
		t.Errorf("expected restore success message, got %q", m.message)
	}
}

func TestChangelog_RestoreFailure(t *testing.T) {
	m := newTestChangelog(nil)
	updated, _ := m.Update(ChangelogRestoreResultMsg{
		Success: false,
		Message: "blob not found",
	})
	m = updated.(*ChangelogModel)
	if !strings.Contains(m.message, "Restore failed") {
		t.Errorf("expected failure message, got %q", m.message)
	}
}

func TestChangelog_KeysOnEmpty(t *testing.T) {
	m := newTestChangelog(nil)

	// Keys should be no-ops on empty list (no panic).
	m, _ = sendKeyType(m, tea.KeyUp)
	m, _ = sendKeyType(m, tea.KeyDown)
	m, _ = sendKeyType(m, tea.KeyEnter)

	if len(m.entries) != 0 {
		t.Error("entries should still be empty")
	}
}

func TestChangelog_ViewShowsFileOperations(t *testing.T) {
	m := newTestChangelog(nil)
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i1",
		Description:   "multi-file edit",
		Files: []ChangelogFile{
			{Path: "/src/new.go", Operation: "write", WasNew: true},
			{Path: "/src/old.go", Operation: "write", WasNew: false},
			{Path: "/src/gone.go", Operation: "delete", WasNew: false},
		},
	})

	// Expand to show file details.
	m, _ = sendKeyType(m, tea.KeyEnter)
	view := m.View()

	if !strings.Contains(view, "new.go") {
		t.Error("expected new.go in expanded view")
	}
	if !strings.Contains(view, "old.go") {
		t.Error("expected old.go in expanded view")
	}
	if !strings.Contains(view, "gone.go") {
		t.Error("expected gone.go in expanded view")
	}
	if !strings.Contains(view, "Restore") {
		t.Error("expected Restore button in expanded view")
	}
}

func TestChangelog_Navigation(t *testing.T) {
	m := newTestChangelog(nil)
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i1",
		Description:   "first",
		Files:         []ChangelogFile{{Path: "/a.go", Operation: "write"}},
	})
	m = sendEntry(m, ChangelogEntryMsg{
		InteractionID: "i2",
		Description:   "second",
		Files:         []ChangelogFile{{Path: "/b.go", Operation: "write"}},
	})

	// After two entries: cursor=1 (on "first" due to prepend shift).
	// Navigate up to entry 0.
	m, _ = sendKeyType(m, tea.KeyUp)
	if m.cursor != 0 {
		t.Errorf("expected cursor=0 after up, got %d", m.cursor)
	}

	// Navigate down back to entry 1.
	m, _ = sendKeyType(m, tea.KeyDown)
	if m.cursor != 1 {
		t.Errorf("expected cursor=1 after down, got %d", m.cursor)
	}

	// Can't go below last entry.
	m, _ = sendKeyType(m, tea.KeyDown)
	if m.cursor != 1 {
		t.Errorf("expected cursor to stay at 1, got %d", m.cursor)
	}
}
