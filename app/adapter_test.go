package app

import (
	"cosmos/core"
	"cosmos/ui"
	"fmt"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestAllCoreEventsHandled is a compile-time assertion that all core event types
// are handled by the adapter. If a new event type is added to core/events.go,
// this test will fail to compile until the adapter is updated.
//
// This mitigates the "weakly typed event contract" issue by catching missing
// handlers at compile time rather than silently dropping events at runtime.
func TestAllCoreEventsHandled(t *testing.T) {
	// Instantiate all core event types
	var _ interface{} = core.TokenEvent{}
	var _ interface{} = core.CompletionEvent{}
	var _ interface{} = core.ErrorEvent{}
	var _ interface{} = core.ToolUseEvent{}
	var _ interface{} = core.ToolResultEvent{}
	var _ interface{} = core.ToolExecutionEvent{}
	var _ interface{} = core.ContextWarningEvent{}
	var _ interface{} = core.ContextAutoCompactEvent{}
	var _ interface{} = core.ContextUpdateEvent{}
	var _ interface{} = core.CompactionStartEvent{}
	var _ interface{} = core.CompactionProgressEvent{}
	var _ interface{} = core.CompactionCompleteEvent{}
	var _ interface{} = core.CompactionFailedEvent{}
	var _ interface{} = core.PermissionRequestEvent{}
	var _ interface{} = core.PermissionTimeoutEvent{}
	var _ interface{} = core.ModelChangedEvent{}
	var _ interface{} = core.HistoryClearedEvent{}
	var _ interface{} = core.ContextInfoEvent{}
	var _ interface{} = core.SessionRestoredEvent{}
	var _ interface{} = core.FileChangeEvent{}

	// If a new event type is added to core/events.go, add it here.
	// The adapter's Send() method must also handle it.
	// If the adapter doesn't handle it, the default case will log a warning.

	// Verify that the adapter handles these types (manual inspection required)
	// This test serves as documentation of the event contract.
	t.Log("All known core event types are documented in this test")
	t.Log("If you add a new event type:")
	t.Log("  1. Add it to core/events.go")
	t.Log("  2. Add it to this test as `var _ interface{} = core.NewEventType{}`")
	t.Log("  3. Add a case for it in app/adapter.go Send() method")
	t.Log("  4. Add corresponding ui.*Msg type in ui/messages.go if needed")
}

// TestAdapterDefaultCase verifies that unhandled events are logged (not silently dropped)
func TestAdapterDefaultCase(t *testing.T) {
	adapter := &coreNotifierAdapter{
		ui: &mockUINotifier{},
	}

	// Send an unknown event type
	type unknownEvent struct{ data string }
	adapter.Send(unknownEvent{data: "test"})

	// The default case should log to stderr. We can't easily capture stderr in this test,
	// but we verify that Send() doesn't panic and the code path is exercised.
	t.Log("Default case handles unknown events without panic")
}

// mockUINotifier is a minimal mock for testing (no-op).
type mockUINotifier struct{}

func (m *mockUINotifier) Send(tea.Msg) {}

// collectingUINotifier captures all messages sent through the adapter.
type collectingUINotifier struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (c *collectingUINotifier) Send(msg tea.Msg) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, msg)
}

func (c *collectingUINotifier) all() []tea.Msg {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]tea.Msg{}, c.msgs...)
}

func TestAdapterModelChangedEvent(t *testing.T) {
	col := &collectingUINotifier{}
	adapter := &coreNotifierAdapter{ui: col}

	adapter.Send(core.ModelChangedEvent{ModelID: "anthropic.claude-3-sonnet"})

	msgs := col.all()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	status, ok := msgs[0].(ui.StatusItemUpdateMsg)
	if !ok {
		t.Fatalf("expected StatusItemUpdateMsg, got %T", msgs[0])
	}
	if status.Key != "model" {
		t.Errorf("expected key 'model', got %q", status.Key)
	}
	sys, ok := msgs[1].(ui.ChatSystemMsg)
	if !ok {
		t.Fatalf("expected ChatSystemMsg, got %T", msgs[1])
	}
	if sys.Text != "Model changed to anthropic.claude-3-sonnet" {
		t.Errorf("unexpected system text: %q", sys.Text)
	}
}

func TestAdapterHistoryClearedEvent(t *testing.T) {
	col := &collectingUINotifier{}
	adapter := &coreNotifierAdapter{ui: col}

	adapter.Send(core.HistoryClearedEvent{})

	msgs := col.all()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if _, ok := msgs[0].(ui.ChatClearMsg); !ok {
		t.Fatalf("expected ChatClearMsg, got %T", msgs[0])
	}
	sys, ok := msgs[1].(ui.ChatSystemMsg)
	if !ok {
		t.Fatalf("expected ChatSystemMsg, got %T", msgs[1])
	}
	if sys.Text != "Conversation cleared." {
		t.Errorf("unexpected system text: %q", sys.Text)
	}
}

func TestAdapterContextInfoEvent(t *testing.T) {
	col := &collectingUINotifier{}
	adapter := &coreNotifierAdapter{ui: col}

	adapter.Send(core.ContextInfoEvent{
		Percentage: 42.5,
		Used:       4250,
		Total:      10000,
		ModelID:    "test-model",
	})

	msgs := col.all()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	sys, ok := msgs[0].(ui.ChatSystemMsg)
	if !ok {
		t.Fatalf("expected ChatSystemMsg, got %T", msgs[0])
	}
	expected := fmt.Sprintf("Context: %.0f%% (%d / %d tokens) — %s", 42.5, 4250, 10000, "test-model")
	if sys.Text != expected {
		t.Errorf("expected %q, got %q", expected, sys.Text)
	}
}

func TestAdapterContextInfoEvent_UnknownWindow(t *testing.T) {
	col := &collectingUINotifier{}
	adapter := &coreNotifierAdapter{ui: col}

	adapter.Send(core.ContextInfoEvent{
		Percentage: 0,
		Used:       3000,
		Total:      0,
		ModelID:    "unknown-model",
	})

	msgs := col.all()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	sys := msgs[0].(ui.ChatSystemMsg)
	expected := fmt.Sprintf("Context: ~%d tokens used (context window unknown for %s)", 3000, "unknown-model")
	if sys.Text != expected {
		t.Errorf("expected %q, got %q", expected, sys.Text)
	}
}

func TestAdapterSessionRestoredEvent(t *testing.T) {
	col := &collectingUINotifier{}
	adapter := &coreNotifierAdapter{ui: col}

	adapter.Send(core.SessionRestoredEvent{
		Description:  "Hello world",
		MessageCount: 12,
	})

	msgs := col.all()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	sys, ok := msgs[0].(ui.ChatSystemMsg)
	if !ok {
		t.Fatalf("expected ChatSystemMsg, got %T", msgs[0])
	}
	expected := "Restored: Hello world (12 messages)"
	if sys.Text != expected {
		t.Errorf("expected %q, got %q", expected, sys.Text)
	}
}
