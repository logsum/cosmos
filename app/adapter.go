package app

import (
	"cosmos/core"
	"cosmos/engine/policy"
	"cosmos/engine/vfs"
	"cosmos/ui"
	"fmt"
	"log"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// coreNotifierAdapter translates core-level events into UI-specific Bubble Tea messages,
// bridging the gap between the framework-agnostic core and the TUI.
type coreNotifierAdapter struct {
	ui        interface{ Send(tea.Msg) }
	cosmosDir string // project-local .cosmos directory for audit/snapshot replay
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
	case core.ModelChangedEvent:
		a.ui.Send(ui.StatusItemUpdateMsg{
			Key:   "model",
			Value: "⚙ " + ui.FormatModelName(e.ModelID),
		})
		a.ui.Send(ui.ChatSystemMsg{Text: "Model changed to " + e.ModelID})
	case core.HistoryClearedEvent:
		a.ui.Send(ui.ChatClearMsg{})
		a.ui.Send(ui.ChatSystemMsg{Text: "Conversation cleared."})
	case core.ContextInfoEvent:
		var text string
		if e.Total > 0 {
			text = fmt.Sprintf("Context: %.0f%% (%d / %d tokens) — %s", e.Percentage, e.Used, e.Total, e.ModelID)
		} else {
			text = fmt.Sprintf("Context: ~%d tokens used (context window unknown for %s)", e.Used, e.ModelID)
		}
		a.ui.Send(ui.ChatSystemMsg{Text: text})
	case core.SessionRestoredEvent:
		a.ui.Send(ui.ChatSystemMsg{Text: fmt.Sprintf("Restored: %s (%d messages)", e.Description, e.MessageCount)})
		if e.SessionID != "" && a.cosmosDir != "" {
			go a.replayChangelog(e.SessionID)
		}
	case core.FileChangeEvent:
		files := make([]ui.ChangelogFile, len(e.Changes))
		for i, c := range e.Changes {
			files[i] = ui.ChangelogFile{Path: c.Path, Operation: c.Operation, WasNew: c.WasNew}
		}
		a.ui.Send(ui.ChangelogEntryMsg{
			InteractionID: e.InteractionID,
			Timestamp:     e.Timestamp.Local().Format("2006-01-02 15:04:05"),
			Description:   formatChangeDesc(e.ToolName, e.AgentName, len(e.Changes)),
			Files:         files,
		})
	default:
		// Log unhandled events to detect integration mistakes during refactors.
		// This should never happen in production if all core events are properly handled.
		fmt.Fprintf(os.Stderr, "cosmos: warning: unhandled core event type: %T\n", msg)
	}
}

// replayChangelog reconstructs changelog entries for a restored session by
// correlating the VFS snapshot manifest with the audit log. Each unique
// InteractionID in the manifest becomes a ChangelogEntryMsg sent to the UI.
// Entries are emitted oldest-first so the newest appears at the top after
// the changelog page prepends each one.
func (a *coreNotifierAdapter) replayChangelog(sessionID string) {
	records, err := vfs.ReadSnapshotManifest(a.cosmosDir, sessionID)
	if err != nil {
		log.Printf("changelog replay: read snapshot manifest: %v", err)
		return
	}
	if len(records) == 0 {
		return
	}

	// Read audit log to enrich descriptions with tool function names.
	auditEntries, _ := policy.ReadAuditLog(sessionID, a.cosmosDir)

	// Index audit entries by ToolCallID (first matching entry wins).
	type auditInfo struct{ tool, agent string }
	byToolCall := make(map[string]auditInfo, len(auditEntries))
	for _, e := range auditEntries {
		if _, exists := byToolCall[e.ToolCallID]; !exists {
			byToolCall[e.ToolCallID] = auditInfo{tool: e.Tool, agent: e.Agent}
		}
	}

	// Group snapshot records by InteractionID, preserving first-seen order.
	type group struct {
		timestamp time.Time
		toolName  string
		agentName string
		changes   []ui.ChangelogFile
	}
	var orderedIDs []string
	groups := make(map[string]*group)

	for _, rec := range records {
		if rec.InteractionID == "" {
			continue
		}
		g, exists := groups[rec.InteractionID]
		if !exists {
			g = &group{
				timestamp: rec.Timestamp,
				agentName: rec.AgentName,
			}
			if info, ok := byToolCall[rec.ToolCallID]; ok {
				g.toolName = info.tool
				if g.agentName == "" {
					g.agentName = info.agent
				}
			}
			orderedIDs = append(orderedIDs, rec.InteractionID)
			groups[rec.InteractionID] = g
		} else if rec.Timestamp.Before(g.timestamp) {
			g.timestamp = rec.Timestamp
		}
		g.changes = append(g.changes, ui.ChangelogFile{
			Path:      rec.Path,
			Operation: rec.Operation,
			WasNew:    rec.WasNewFile,
		})
	}

	// Emit oldest-first so the changelog page (which prepends) ends up newest-at-top.
	for _, id := range orderedIDs {
		g := groups[id]
		a.ui.Send(ui.ChangelogEntryMsg{
			InteractionID: id,
			Timestamp:     g.timestamp.Local().Format("2006-01-02 15:04:05"),
			Description:   formatChangeDesc(g.toolName, g.agentName, len(g.changes)),
			Files:         g.changes,
		})
	}
}

// formatChangeDesc builds a human-readable description for a changelog entry.
// Rules: if both names are known, format as "tool (agent) modified N file(s)";
// if only one is known, use it; if neither is known, fall back to "unknown".
func formatChangeDesc(toolName, agentName string, n int) string {
	if toolName != "" && agentName != "" {
		return fmt.Sprintf("%s (%s) modified %d file(s)", toolName, agentName, n)
	}
	actor := toolName
	if actor == "" {
		actor = agentName
	}
	if actor == "" {
		actor = "unknown"
	}
	return fmt.Sprintf("%s modified %d file(s)", actor, n)
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
