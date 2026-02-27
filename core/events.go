package core

import "time"

// Core-level events emitted by the LLM loop. These are framework-agnostic
// counterparts to the UI message types in ui/messages.go. The adapter in
// main.go translates them into Bubble Tea messages for the TUI.

// TokenEvent carries a single token delta from LLM streaming.
type TokenEvent struct{ Text string }

// CompletionEvent signals the assistant message is complete.
type CompletionEvent struct{}

// ErrorEvent reports an error during LLM interaction.
type ErrorEvent struct{ Error string }

// ToolUseEvent signals the LLM is invoking a tool.
type ToolUseEvent struct {
	ToolCallID string
	ToolName   string
	Input      string
}

// ToolResultEvent carries the result of a tool execution.
type ToolResultEvent struct {
	ToolCallID string
	ToolName   string
	Result     string
	IsError    bool
}

// ToolExecutionEvent carries full tool execution data for the agents page.
type ToolExecutionEvent struct {
	ToolCallID string
	ToolName   string
	Input      string
	Output     string
	IsError    bool
}

// ContextWarningEvent signals that context usage crossed the 50% threshold.
type ContextWarningEvent struct {
	Percentage float64
	Threshold  float64 // Always 50.0
	ModelID    string
}

// ContextAutoCompactEvent signals automatic compaction at 90%.
type ContextAutoCompactEvent struct {
	Percentage float64
	ModelID    string
}

// ContextUpdateEvent updates the context percentage display in status bar.
type ContextUpdateEvent struct {
	Percentage float64
	ModelID    string
}

// CompactionStartEvent signals that compaction has begun (manual or automatic).
type CompactionStartEvent struct {
	Mode string // "manual" or "automatic"
}

// CompactionProgressEvent provides mid-flight update during compaction.
type CompactionProgressEvent struct {
	Stage string // "generating_summary", "estimating_tokens", etc.
}

// CompactionCompleteEvent signals successful compaction with metrics.
type CompactionCompleteEvent struct {
	OldTokens int
	NewTokens int
}

// CompactionFailedEvent signals compaction failure with preserved conversation.
type CompactionFailedEvent struct {
	Error string
}

// PermissionRequestEvent is emitted when a tool requires user permission.
// The core blocks on ResponseChan waiting for the user's decision.
type PermissionRequestEvent struct {
	ToolCallID   string
	ToolName     string
	AgentName    string
	Permission   string        // e.g. "fs:write:./src/**"
	Description  string        // User-friendly description
	Timeout      time.Duration // 0 = no timeout
	DefaultAllow bool          // If timeout expires, grant or deny?
	ResponseChan chan<- PermissionResponse
}

// PermissionResponse is the user's decision sent back via channel.
type PermissionResponse struct {
	Allowed  bool
	Remember bool // Only meaningful for request_once
}

// PermissionTimeoutEvent is emitted when a permission request times out.
// The UI should mark the request as resolved with the default decision.
type PermissionTimeoutEvent struct {
	ToolCallID string
	Allowed    bool // Whether the default was to allow (from DefaultAllow)
}
