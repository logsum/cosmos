// Package provider defines the LLM provider abstraction for Cosmos.
// It contains only interfaces and data types — no implementation.
package provider

import (
	"context"
	"errors"
)

// Common errors returned by providers.
var (
	ErrThrottled     = errors.New("provider: request throttled")
	ErrAccessDenied  = errors.New("provider: access denied")
	ErrModelNotFound = errors.New("provider: model not found")
	ErrModelNotReady = errors.New("provider: model not ready")
)

// Role identifies who authored a conversation message.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message represents a single conversation turn.
// An assistant message may contain both text and tool calls.
// A user message may carry tool results (Bedrock convention).
type Message struct {
	Role        Role
	Content     string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
}

// ToolCall represents the LLM requesting a tool invocation.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

// ToolResult carries the output of a tool execution back to the LLM.
type ToolResult struct {
	ToolUseID string
	Content   string
	IsError   bool
}

// ToolDefinition describes a tool the LLM can invoke.
// InputSchema is a JSON Schema object built from manifest function params.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// StreamEvent identifies the type of a streaming chunk.
type StreamEvent int

const (
	EventTextDelta   StreamEvent = iota // Partial text content
	EventToolStart                      // Tool invocation begins
	EventToolDelta                      // Partial tool input JSON
	EventToolEnd                        // Tool invocation block complete
	EventMessageStop                    // Response finished
)

// StreamChunk is one unit of streamed LLM output.
// Fields are relevant per event type; others are zero-valued.
type StreamChunk struct {
	Event      StreamEvent
	Text       string // EventTextDelta
	ToolCallID string // EventToolStart
	ToolName   string // EventToolStart
	InputDelta string // EventToolDelta: partial JSON fragment
	StopReason string // EventMessageStop: "end_turn", "tool_use"
	Usage      *Usage // Set on EventMessageStop
}

// Usage holds token counts from a single LLM response.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// ModelInfo describes a model's metadata and pricing.
type ModelInfo struct {
	ID              string  // Provider-specific model identifier
	Name            string  // Human-readable display name
	ContextWindow   int
	InputCostPer1M  float64
	OutputCostPer1M float64
}

// Request bundles everything sent to the LLM for one round-trip.
type Request struct {
	Model     string
	System    string
	Messages  []Message
	Tools     []ToolDefinition
	MaxTokens int
}

// StreamIterator provides token-by-token iteration over a streamed response.
// Callers loop on Next() until it returns io.EOF.
type StreamIterator interface {
	Next() (StreamChunk, error)
	Close() error
}

// Provider is the LLM provider abstraction that the core loop consumes.
type Provider interface {
	Send(ctx context.Context, req Request) (StreamIterator, error)
	ListModels(ctx context.Context) ([]ModelInfo, error)
}

// PricingConfig holds provider-agnostic settings for dynamic pricing.
// Passed to provider constructors to decouple providers from the application config.
type PricingConfig struct {
	Enabled  bool   // Whether to fetch dynamic pricing
	CacheDir string // Directory for caching pricing data
	CacheTTL int    // Check interval in hours
}
