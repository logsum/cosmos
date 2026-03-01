package app

import (
	"cosmos/core"
	"cosmos/ui"
	"fmt"
	"log"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// coreNotifierAdapter translates core-level events into UI-specific Bubble Tea messages,
// bridging the gap between the framework-agnostic core and the TUI.
type coreNotifierAdapter struct {
	ui interface{ Send(tea.Msg) }
}

func (a *coreNotifierAdapter) Send(msg any) {
	switch e := msg.(type) {
	case core.TokenEvent:
		a.ui.Send(ui.ChatTokenMsg{Text: e.Text})
	case core.CompletionEvent:
		a.ui.Send(ui.ChatCompletionMsg{})
	case core.ErrorEvent:
		a.ui.Send(ui.ChatErrorMsg{Error: e.Error})
	case core.ToolUseEvent:
		a.ui.Send(ui.ChatToolUseMsg{ToolCallID: e.ToolCallID, ToolName: e.ToolName, Input: e.Input})
	case core.ToolResultEvent:
		a.ui.Send(ui.ChatToolResultMsg{ToolCallID: e.ToolCallID, ToolName: e.ToolName, Result: e.Result, IsError: e.IsError})
	case core.ToolExecutionEvent:
		a.ui.Send(ui.ToolExecutionMsg{ToolCallID: e.ToolCallID, ToolName: e.ToolName, Input: e.Input, Output: e.Output, IsError: e.IsError})
	case core.ContextWarningEvent:
		a.ui.Send(ui.ChatContextWarningMsg{
			Percentage: e.Percentage,
			Threshold:  e.Threshold,
			ModelID:    e.ModelID,
		})
	case core.ContextAutoCompactEvent:
		a.ui.Send(ui.ChatContextAutoCompactMsg{
			Percentage: e.Percentage,
			ModelID:    e.ModelID,
		})
	case core.ContextUpdateEvent:
		// Format and update status bar
		formatted := formatContextPercentage(e.Percentage)
		a.ui.Send(ui.StatusItemUpdateMsg{
			Key:   "context",
			Value: formatted,
		})
	case core.CompactionStartEvent:
		a.ui.Send(ui.ChatCompactionStartMsg{Mode: e.Mode})
	case core.CompactionProgressEvent:
		a.ui.Send(ui.ChatCompactionProgressMsg{Stage: e.Stage})
	case core.CompactionCompleteEvent:
		a.ui.Send(ui.ChatCompactionCompleteMsg{
			OldTokens: e.OldTokens,
			NewTokens: e.NewTokens,
		})
	case core.CompactionFailedEvent:
		a.ui.Send(ui.ChatCompactionFailedMsg{Error: e.Error})
	case core.PermissionRequestEvent:
		// Wrap the core channel in a callback so ui never imports core.
		// The recover() handles the race where core closes the channel
		// (on timeout/cancellation) before the UI responds.
		ch := e.ResponseChan
		a.ui.Send(ui.ChatPermissionRequestMsg{
			ToolCallID:   e.ToolCallID,
			ToolName:     e.ToolName,
			AgentName:    e.AgentName,
			Permission:   e.Permission,
			Description:  e.Description,
			Timeout:      e.Timeout,
			DefaultAllow: e.DefaultAllow,
			RespondFunc: func(allowed, remember bool) {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("permission response channel already closed (timeout race): %v", r)
					}
				}()
				ch <- core.PermissionResponse{
					Allowed:  allowed,
					Remember: remember,
				}
			},
		})
	case core.PermissionTimeoutEvent:
		a.ui.Send(ui.ChatPermissionTimeoutMsg{
			ToolCallID: e.ToolCallID,
			Allowed:    e.Allowed,
		})
	default:
		// Log unhandled events to detect integration mistakes during refactors.
		// This should never happen in production if all core events are properly handled.
		fmt.Fprintf(os.Stderr, "cosmos: warning: unhandled core event type: %T\n", msg)
	}
}

// formatContextPercentage formats context usage for status bar display.
// Uses lightning bolt emoji (⚡) to match status bar style.
func formatContextPercentage(pct float64) string {
	if pct < 1.0 {
		return "⚡<1%"
	}
	if pct >= 100.0 {
		return "⚡100%"
	}
	return fmt.Sprintf("⚡%.0f%%", pct)
}
