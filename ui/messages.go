package ui

import "time"

// ChatTokenMsg carries a single token delta from LLM streaming
type ChatTokenMsg struct {
	Text string
}

// ChatCompletionMsg signals the assistant message is complete
type ChatCompletionMsg struct{}

// ChatErrorMsg reports an error during LLM interaction
type ChatErrorMsg struct {
	Error string
}

// ChatToolUseMsg signals the LLM is invoking a tool.
type ChatToolUseMsg struct {
	ToolCallID string
	ToolName   string
	Input      string // JSON string of input args
}

// ChatToolResultMsg carries the result of a tool execution.
type ChatToolResultMsg struct {
	ToolCallID string
	ToolName   string
	Result     string
	IsError    bool
}

// ToolExecutionMsg carries full tool execution data for the Agents page.
type ToolExecutionMsg struct {
	ToolCallID string
	ToolName   string
	Input      string // Full JSON input
	Output     string // Full result text
	IsError    bool
}

// ChatContextWarningMsg warns about context usage reaching 50%.
type ChatContextWarningMsg struct {
	Percentage float64
	Threshold  float64
	ModelID    string
}

// ChatContextAutoCompactMsg signals automatic compaction at 90%.
type ChatContextAutoCompactMsg struct {
	Percentage float64
	ModelID    string
}

// ChatCompactionStartMsg signals that compaction has begun.
type ChatCompactionStartMsg struct {
	Mode string // "manual" or "automatic"
}

// ChatCompactionProgressMsg provides mid-flight update during compaction.
type ChatCompactionProgressMsg struct {
	Stage string
}

// ChatCompactionCompleteMsg signals successful compaction with metrics.
type ChatCompactionCompleteMsg struct {
	OldTokens int
	NewTokens int
}

// ChatCompactionFailedMsg signals compaction failure.
type ChatCompactionFailedMsg struct {
	Error string
}

// ChatPermissionRequestMsg requests user permission for a tool action.
// The chat page must display an inline prompt and send the user's decision
// back via RespondFunc. The adapter wraps the core channel into a callback
// so that ui never imports core.
type ChatPermissionRequestMsg struct {
	ToolCallID   string
	ToolName     string
	AgentName    string
	Permission   string
	Description  string
	Timeout      time.Duration
	DefaultAllow bool
	RespondFunc  func(allowed, remember bool) // Adapter-provided callback to send decision to core
}

// ChatPermissionTimeoutMsg signals that a permission request timed out in core.
// The UI should mark the request as resolved.
type ChatPermissionTimeoutMsg struct {
	ToolCallID string
	Allowed    bool // Whether the default was to allow
}

// PermissionDecisionMsg is sent by the user when they press y/n on a permission prompt.
// This is an internal UI message, not sent from core.
type PermissionDecisionMsg struct {
	ToolCallID string
	Allowed    bool
	Remember   bool
}

// ChatSystemMsg is an informational system message rendered inline in chat
// (e.g. responses to /clear, /context, /model commands). Displayed in dim gray
// with no colored bar.
type ChatSystemMsg struct{ Text string }

// ChatClearMsg instructs the chat page to flush all visible content to the
// terminal scrollback and then reset its in-memory message state.
type ChatClearMsg struct{}
