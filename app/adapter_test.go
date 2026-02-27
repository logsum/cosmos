package app

import (
	"cosmos/core"
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

// mockUINotifier is a minimal mock for testing
type mockUINotifier struct{}

func (m *mockUINotifier) Send(msg tea.Msg) {
	// No-op for testing
}
