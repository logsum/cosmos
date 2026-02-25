package provider

import (
	"context"
	"io"
	"testing"
)

// mockIterator is a minimal StreamIterator that returns EOF immediately.
type mockIterator struct{}

func (m *mockIterator) Next() (StreamChunk, error) { return StreamChunk{}, io.EOF }
func (m *mockIterator) Close() error               { return nil }

// mockProvider is a minimal Provider implementation for compile-time checks.
type mockProvider struct{}

func (m *mockProvider) Send(_ context.Context, _ Request) (StreamIterator, error) {
	return &mockIterator{}, nil
}

func (m *mockProvider) ListModels(_ context.Context) ([]ModelInfo, error) {
	return nil, nil
}

// Compile-time interface satisfaction checks.
var _ Provider = (*mockProvider)(nil)
var _ StreamIterator = (*mockIterator)(nil)

func TestMessageConstruction(t *testing.T) {
	// Build a multi-turn conversation: user text -> assistant with tool calls -> tool results.
	conversation := []Message{
		{
			Role:    RoleUser,
			Content: "Analyze the main.go file",
		},
		{
			Role:    RoleAssistant,
			Content: "I'll analyze that file for you.",
			ToolCalls: []ToolCall{
				{
					ID:   "call_001",
					Name: "analyzeFile",
					Input: map[string]any{
						"filePath": "main.go",
						"depth":    float64(2),
					},
				},
			},
		},
		{
			Role: RoleUser,
			ToolResults: []ToolResult{
				{
					ToolUseID: "call_001",
					Content:   `{"lines": 150, "functions": 5}`,
					IsError:   false,
				},
			},
		},
	}

	if len(conversation) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(conversation))
	}

	// Verify user message.
	if conversation[0].Role != RoleUser {
		t.Errorf("message 0: expected role %q, got %q", RoleUser, conversation[0].Role)
	}
	if conversation[0].Content != "Analyze the main.go file" {
		t.Errorf("message 0: unexpected content %q", conversation[0].Content)
	}

	// Verify assistant message with tool call.
	assistant := conversation[1]
	if assistant.Role != RoleAssistant {
		t.Errorf("message 1: expected role %q, got %q", RoleAssistant, assistant.Role)
	}
	if len(assistant.ToolCalls) != 1 {
		t.Fatalf("message 1: expected 1 tool call, got %d", len(assistant.ToolCalls))
	}
	tc := assistant.ToolCalls[0]
	if tc.ID != "call_001" || tc.Name != "analyzeFile" {
		t.Errorf("tool call: got ID=%q Name=%q", tc.ID, tc.Name)
	}
	if tc.Input["filePath"] != "main.go" {
		t.Errorf("tool call input filePath: got %v", tc.Input["filePath"])
	}

	// Verify tool result message.
	if len(conversation[2].ToolResults) != 1 {
		t.Fatalf("message 2: expected 1 tool result, got %d", len(conversation[2].ToolResults))
	}
	tr := conversation[2].ToolResults[0]
	if tr.ToolUseID != "call_001" {
		t.Errorf("tool result: expected ToolUseID %q, got %q", "call_001", tr.ToolUseID)
	}
	if tr.IsError {
		t.Error("tool result: expected IsError=false")
	}
}

func TestStreamChunkPerEvent(t *testing.T) {
	chunks := []StreamChunk{
		{
			Event: EventTextDelta,
			Text:  "Hello",
		},
		{
			Event:      EventToolStart,
			ToolCallID: "call_002",
			ToolName:   "readFile",
		},
		{
			Event:      EventToolDelta,
			InputDelta: `{"path": "src/`,
		},
		{
			Event: EventToolEnd,
		},
		{
			Event:      EventMessageStop,
			StopReason: "end_turn",
			Usage:      &Usage{InputTokens: 100, OutputTokens: 50},
		},
	}

	if chunks[0].Event != EventTextDelta || chunks[0].Text != "Hello" {
		t.Errorf("EventTextDelta chunk: got event=%d text=%q", chunks[0].Event, chunks[0].Text)
	}

	if chunks[1].Event != EventToolStart || chunks[1].ToolCallID != "call_002" || chunks[1].ToolName != "readFile" {
		t.Errorf("EventToolStart chunk: got event=%d id=%q name=%q", chunks[1].Event, chunks[1].ToolCallID, chunks[1].ToolName)
	}

	if chunks[2].Event != EventToolDelta || chunks[2].InputDelta != `{"path": "src/` {
		t.Errorf("EventToolDelta chunk: got event=%d delta=%q", chunks[2].Event, chunks[2].InputDelta)
	}

	if chunks[3].Event != EventToolEnd {
		t.Errorf("EventToolEnd chunk: got event=%d", chunks[3].Event)
	}

	stop := chunks[4]
	if stop.Event != EventMessageStop || stop.StopReason != "end_turn" {
		t.Errorf("EventMessageStop chunk: got event=%d reason=%q", stop.Event, stop.StopReason)
	}
	if stop.Usage == nil {
		t.Fatal("EventMessageStop: expected non-nil Usage")
	}
	if stop.Usage.InputTokens != 100 || stop.Usage.OutputTokens != 50 {
		t.Errorf("Usage: got input=%d output=%d", stop.Usage.InputTokens, stop.Usage.OutputTokens)
	}
}
