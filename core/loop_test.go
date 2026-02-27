package core

import (
	"context"
	"cosmos/core/provider"
	"cosmos/engine/policy"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- Mock provider ---

// mockStreamIterator replays a fixed sequence of StreamChunks.
type mockStreamIterator struct {
	chunks []provider.StreamChunk
	idx    int
}

func (it *mockStreamIterator) Next() (provider.StreamChunk, error) {
	if it.idx >= len(it.chunks) {
		return provider.StreamChunk{}, io.EOF
	}
	c := it.chunks[it.idx]
	it.idx++
	return c, nil
}

func (it *mockStreamIterator) Close() error { return nil }

// mockProvider returns a sequence of stream iterators, one per Send call.
type mockProvider struct {
	calls  [][]provider.StreamChunk // one chunk sequence per call
	idx    int
	mu     sync.Mutex
	models []provider.ModelInfo // models to return from ListModels
}

func (p *mockProvider) Send(_ context.Context, _ provider.Request) (provider.StreamIterator, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.idx >= len(p.calls) {
		return nil, fmt.Errorf("unexpected Send call #%d", p.idx+1)
	}
	chunks := p.calls[p.idx]
	p.idx++
	return &mockStreamIterator{chunks: chunks}, nil
}

func (p *mockProvider) ListModels(_ context.Context) ([]provider.ModelInfo, error) {
	if p.models != nil {
		return p.models, nil
	}
	return nil, nil
}

// --- Mock executor ---

type mockExecutor struct {
	results map[string]string // tool name → result
	errors  map[string]error  // tool name → error
}

func (e *mockExecutor) Execute(_ context.Context, name string, _ map[string]any) (string, error) {
	if err, ok := e.errors[name]; ok {
		return "", err
	}
	if result, ok := e.results[name]; ok {
		return result, nil
	}
	return "", fmt.Errorf("unknown tool: %s", name)
}

// --- Mock notifier ---

type mockNotifier struct {
	mu   sync.Mutex
	msgs []any
}

func (n *mockNotifier) Send(msg any) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.msgs = append(n.msgs, msg)
}

func (n *mockNotifier) getMessages() []any {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]any, len(n.msgs))
	copy(out, n.msgs)
	return out
}

// waitForEvent polls the notifier for an event matching predicate, with timeout.
// Returns (event, true) on match or (nil, false) on timeout.
func (n *mockNotifier) waitForEvent(predicate func(any) bool, timeout time.Duration) (any, bool) {
	deadline := time.After(timeout)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		n.mu.Lock()
		for _, m := range n.msgs {
			if predicate(m) {
				n.mu.Unlock()
				return m, true
			}
		}
		n.mu.Unlock()

		select {
		case <-deadline:
			return nil, false
		case <-ticker.C:
			continue
		}
	}
}

// --- Helpers ---

func textChunks(text string) []provider.StreamChunk {
	return []provider.StreamChunk{
		{Event: provider.EventTextDelta, Text: text},
		{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
	}
}

func toolUseChunks(toolID, toolName, inputJSON string) []provider.StreamChunk {
	return []provider.StreamChunk{
		{Event: provider.EventToolStart, ToolCallID: toolID, ToolName: toolName},
		{Event: provider.EventToolDelta, InputDelta: inputJSON},
		{Event: provider.EventToolEnd},
		{Event: provider.EventMessageStop, StopReason: "tool_use", Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
	}
}

func newTestSession(prov provider.Provider, executor ToolExecutor, notifier Notifier) *Session {
	return NewSession("test-session-id", prov, NewTracker(nil, nil), notifier, "test-model", "system", 1024, executor, nil, nil, nil)
}

// --- Tests ---

func TestTextOnlyResponse(t *testing.T) {
	prov := &mockProvider{calls: [][]provider.StreamChunk{
		textChunks("Hello, world!"),
	}}
	notifier := &mockNotifier{}
	executor := &mockExecutor{}
	session := newTestSession(prov, executor, notifier)

	err := session.processUserMessage(context.Background(), "Hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// History: user + assistant
	if len(session.history) != 2 {
		t.Fatalf("history length = %d, want 2", len(session.history))
	}
	if session.history[0].Role != provider.RoleUser {
		t.Errorf("history[0].Role = %q, want %q", session.history[0].Role, provider.RoleUser)
	}
	if session.history[1].Role != provider.RoleAssistant {
		t.Errorf("history[1].Role = %q, want %q", session.history[1].Role, provider.RoleAssistant)
	}
	if session.history[1].Content != "Hello, world!" {
		t.Errorf("history[1].Content = %q, want %q", session.history[1].Content, "Hello, world!")
	}

	// Verify ChatCompletionMsg was sent
	msgs := notifier.getMessages()
	hasCompletion := false
	for _, m := range msgs {
		if _, ok := m.(CompletionEvent); ok {
			hasCompletion = true
		}
	}
	if !hasCompletion {
		t.Error("expected ChatCompletionMsg in notifier messages")
	}
}

func TestSingleToolCall(t *testing.T) {
	// First call: model requests tool use
	// Second call: model returns text
	prov := &mockProvider{calls: [][]provider.StreamChunk{
		toolUseChunks("tool-1", "get_weather", `{"location":"Rome"}`),
		textChunks("The weather in Rome is sunny."),
	}}
	notifier := &mockNotifier{}
	executor := &mockExecutor{
		results: map[string]string{
			"get_weather": `{"temperature":"22°C","condition":"sunny"}`,
		},
	}
	session := newTestSession(prov, executor, notifier)

	err := session.processUserMessage(context.Background(), "What's the weather?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// History: user → assistant(tool_calls) → user(tool_results) → assistant(text)
	if len(session.history) != 4 {
		t.Fatalf("history length = %d, want 4", len(session.history))
	}

	// First message: user
	if session.history[0].Role != provider.RoleUser {
		t.Errorf("history[0].Role = %q, want user", session.history[0].Role)
	}

	// Second: assistant with tool calls
	if session.history[1].Role != provider.RoleAssistant {
		t.Errorf("history[1].Role = %q, want assistant", session.history[1].Role)
	}
	if len(session.history[1].ToolCalls) != 1 {
		t.Fatalf("history[1].ToolCalls length = %d, want 1", len(session.history[1].ToolCalls))
	}
	if session.history[1].ToolCalls[0].Name != "get_weather" {
		t.Errorf("tool call name = %q, want get_weather", session.history[1].ToolCalls[0].Name)
	}

	// Third: user with tool results
	if session.history[2].Role != provider.RoleUser {
		t.Errorf("history[2].Role = %q, want user", session.history[2].Role)
	}
	if len(session.history[2].ToolResults) != 1 {
		t.Fatalf("history[2].ToolResults length = %d, want 1", len(session.history[2].ToolResults))
	}
	if session.history[2].ToolResults[0].IsError {
		t.Error("tool result should not be an error")
	}

	// Fourth: assistant with final text
	if session.history[3].Role != provider.RoleAssistant {
		t.Errorf("history[3].Role = %q, want assistant", session.history[3].Role)
	}
	if session.history[3].Content != "The weather in Rome is sunny." {
		t.Errorf("history[3].Content = %q, want final text", session.history[3].Content)
	}

	// Verify UI messages: should have ChatToolUseMsg and ChatToolResultMsg with ToolCallID
	msgs := notifier.getMessages()
	var hasToolUse, hasToolResult, hasToolExec bool
	for _, m := range msgs {
		switch msg := m.(type) {
		case ToolUseEvent:
			hasToolUse = true
			if msg.ToolCallID != "tool-1" {
				t.Errorf("ChatToolUseMsg.ToolCallID = %q, want %q", msg.ToolCallID, "tool-1")
			}
		case ToolResultEvent:
			hasToolResult = true
			if msg.ToolCallID != "tool-1" {
				t.Errorf("ChatToolResultMsg.ToolCallID = %q, want %q", msg.ToolCallID, "tool-1")
			}
		case ToolExecutionEvent:
			hasToolExec = true
			if msg.ToolCallID != "tool-1" {
				t.Errorf("ToolExecutionMsg.ToolCallID = %q, want %q", msg.ToolCallID, "tool-1")
			}
			if msg.ToolName != "get_weather" {
				t.Errorf("ToolExecutionMsg.ToolName = %q, want %q", msg.ToolName, "get_weather")
			}
			if msg.IsError {
				t.Error("ToolExecutionMsg.IsError should be false")
			}
		}
	}
	if !hasToolUse {
		t.Error("expected ChatToolUseMsg")
	}
	if !hasToolResult {
		t.Error("expected ChatToolResultMsg")
	}
	if !hasToolExec {
		t.Error("expected ToolExecutionMsg")
	}
}

func TestMultipleToolCallsInOneResponse(t *testing.T) {
	// Model requests two tools in one response
	chunks := []provider.StreamChunk{
		{Event: provider.EventToolStart, ToolCallID: "t1", ToolName: "get_weather"},
		{Event: provider.EventToolDelta, InputDelta: `{"location":"Rome"}`},
		{Event: provider.EventToolEnd},
		{Event: provider.EventToolStart, ToolCallID: "t2", ToolName: "read_file"},
		{Event: provider.EventToolDelta, InputDelta: `{"path":"/tmp/a.txt"}`},
		{Event: provider.EventToolEnd},
		{Event: provider.EventMessageStop, StopReason: "tool_use", Usage: &provider.Usage{InputTokens: 20, OutputTokens: 10}},
	}

	prov := &mockProvider{calls: [][]provider.StreamChunk{
		chunks,
		textChunks("Done."),
	}}
	notifier := &mockNotifier{}
	executor := &mockExecutor{
		results: map[string]string{
			"get_weather": `{"temp":"20°C"}`,
			"read_file":   "file content",
		},
	}
	session := newTestSession(prov, executor, notifier)

	err := session.processUserMessage(context.Background(), "Do both")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// History: user → assistant(2 tool_calls) → user(2 tool_results) → assistant(text)
	if len(session.history) != 4 {
		t.Fatalf("history length = %d, want 4", len(session.history))
	}
	if len(session.history[1].ToolCalls) != 2 {
		t.Errorf("tool calls = %d, want 2", len(session.history[1].ToolCalls))
	}
	if len(session.history[2].ToolResults) != 2 {
		t.Errorf("tool results = %d, want 2", len(session.history[2].ToolResults))
	}

	// Verify both tool use, result, and execution messages sent with correct IDs
	msgs := notifier.getMessages()
	toolUseCount := 0
	toolResultCount := 0
	toolExecCount := 0
	toolUseIDs := map[string]bool{}
	toolExecIDs := map[string]bool{}
	for _, m := range msgs {
		switch msg := m.(type) {
		case ToolUseEvent:
			toolUseCount++
			if msg.ToolCallID == "" {
				t.Error("ChatToolUseMsg.ToolCallID is empty")
			}
			toolUseIDs[msg.ToolCallID] = true
		case ToolResultEvent:
			toolResultCount++
			if msg.ToolCallID == "" {
				t.Error("ChatToolResultMsg.ToolCallID is empty")
			}
		case ToolExecutionEvent:
			toolExecCount++
			if msg.ToolCallID == "" {
				t.Error("ToolExecutionMsg.ToolCallID is empty")
			}
			toolExecIDs[msg.ToolCallID] = true
		}
	}
	if toolUseCount != 2 {
		t.Errorf("ChatToolUseMsg count = %d, want 2", toolUseCount)
	}
	if toolResultCount != 2 {
		t.Errorf("ChatToolResultMsg count = %d, want 2", toolResultCount)
	}
	if toolExecCount != 2 {
		t.Errorf("ToolExecutionMsg count = %d, want 2", toolExecCount)
	}
	if !toolUseIDs["t1"] || !toolUseIDs["t2"] {
		t.Errorf("expected ToolCallIDs t1 and t2 in ChatToolUseMsg, got %v", toolUseIDs)
	}
	if !toolExecIDs["t1"] || !toolExecIDs["t2"] {
		t.Errorf("expected ToolCallIDs t1 and t2 in ToolExecutionMsg, got %v", toolExecIDs)
	}
}

func TestToolExecutorError(t *testing.T) {
	prov := &mockProvider{calls: [][]provider.StreamChunk{
		toolUseChunks("t1", "bad_tool", `{}`),
		textChunks("Sorry, the tool failed."),
	}}
	notifier := &mockNotifier{}
	executor := &mockExecutor{
		errors: map[string]error{
			"bad_tool": fmt.Errorf("tool exploded"),
		},
	}
	session := newTestSession(prov, executor, notifier)

	err := session.processUserMessage(context.Background(), "try it")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Tool result should have IsError=true
	if len(session.history) != 4 {
		t.Fatalf("history length = %d, want 4", len(session.history))
	}
	results := session.history[2].ToolResults
	if len(results) != 1 {
		t.Fatalf("tool results = %d, want 1", len(results))
	}
	if !results[0].IsError {
		t.Error("expected tool result IsError=true")
	}
	if results[0].Content != "tool exploded" {
		t.Errorf("error content = %q, want %q", results[0].Content, "tool exploded")
	}

	// Verify ChatToolResultMsg and ToolExecutionMsg have IsError=true and correct ToolCallID
	msgs := notifier.getMessages()
	var hasExecMsg bool
	for _, m := range msgs {
		switch msg := m.(type) {
		case ToolResultEvent:
			if !msg.IsError {
				t.Error("expected ChatToolResultMsg.IsError=true")
			}
			if msg.ToolCallID != "t1" {
				t.Errorf("ChatToolResultMsg.ToolCallID = %q, want %q", msg.ToolCallID, "t1")
			}
		case ToolExecutionEvent:
			hasExecMsg = true
			if !msg.IsError {
				t.Error("expected ToolExecutionMsg.IsError=true")
			}
			if msg.ToolCallID != "t1" {
				t.Errorf("ToolExecutionMsg.ToolCallID = %q, want %q", msg.ToolCallID, "t1")
			}
			if msg.ToolName != "bad_tool" {
				t.Errorf("ToolExecutionMsg.ToolName = %q, want %q", msg.ToolName, "bad_tool")
			}
		}
	}
	if !hasExecMsg {
		t.Error("expected ToolExecutionMsg for failed tool")
	}
}

func TestMultiRoundToolUse(t *testing.T) {
	// Round 1: tool_use → Round 2: tool_use → Round 3: end_turn
	prov := &mockProvider{calls: [][]provider.StreamChunk{
		toolUseChunks("t1", "get_weather", `{"location":"Rome"}`),
		toolUseChunks("t2", "read_file", `{"path":"/tmp/b.txt"}`),
		textChunks("All done."),
	}}
	notifier := &mockNotifier{}
	executor := &mockExecutor{
		results: map[string]string{
			"get_weather": "sunny",
			"read_file":   "data",
		},
	}
	session := newTestSession(prov, executor, notifier)

	err := session.processUserMessage(context.Background(), "do everything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// History: user → assistant(tc) → user(tr) → assistant(tc) → user(tr) → assistant(text)
	if len(session.history) != 6 {
		t.Fatalf("history length = %d, want 6", len(session.history))
	}

	// Verify alternating roles
	expectedRoles := []provider.Role{
		provider.RoleUser,
		provider.RoleAssistant,
		provider.RoleUser,
		provider.RoleAssistant,
		provider.RoleUser,
		provider.RoleAssistant,
	}
	for i, want := range expectedRoles {
		if session.history[i].Role != want {
			t.Errorf("history[%d].Role = %q, want %q", i, session.history[i].Role, want)
		}
	}

	// Final message should be text
	if session.history[5].Content != "All done." {
		t.Errorf("final content = %q, want %q", session.history[5].Content, "All done.")
	}

	// Should have 3 ChatCompletionMsg (one per round)
	msgs := notifier.getMessages()
	completionCount := 0
	toolExecCount := 0
	for _, m := range msgs {
		switch m.(type) {
		case CompletionEvent:
			completionCount++
		case ToolExecutionEvent:
			toolExecCount++
		}
	}
	if completionCount != 3 {
		t.Errorf("ChatCompletionMsg count = %d, want 3", completionCount)
	}
	// Two tool calls across two rounds → 2 ToolExecutionMsg
	if toolExecCount != 2 {
		t.Errorf("ToolExecutionMsg count = %d, want 2", toolExecCount)
	}
}

func TestDoubleStopNoPanic(t *testing.T) {
	prov := &mockProvider{calls: [][]provider.StreamChunk{
		textChunks("Hello"),
	}}
	notifier := &mockNotifier{}
	session := newTestSession(prov, &mockExecutor{}, notifier)

	// Calling Stop twice must not panic
	session.Stop()
	session.Stop()
}

func TestNilExecutorToolUse(t *testing.T) {
	// Model requests a tool, but no executor is configured
	prov := &mockProvider{calls: [][]provider.StreamChunk{
		toolUseChunks("t1", "some_tool", `{"key":"val"}`),
		textChunks("OK, the tool was unavailable."),
	}}
	notifier := &mockNotifier{}
	// Pass nil executor
	session := newTestSession(prov, nil, notifier)

	err := session.processUserMessage(context.Background(), "use the tool")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// History: user → assistant(tool_calls) → user(tool_results) → assistant(text)
	if len(session.history) != 4 {
		t.Fatalf("history length = %d, want 4", len(session.history))
	}

	// Tool result should be an error about no executor
	results := session.history[2].ToolResults
	if len(results) != 1 {
		t.Fatalf("tool results = %d, want 1", len(results))
	}
	if !results[0].IsError {
		t.Error("expected tool result IsError=true")
	}
	if results[0].Content != "no tool executor configured" {
		t.Errorf("error content = %q, want %q", results[0].Content, "no tool executor configured")
	}

	// Verify the UI got notified of the error result
	msgs := notifier.getMessages()
	var hasErrorResult bool
	for _, m := range msgs {
		if msg, ok := m.(ToolResultEvent); ok {
			if msg.IsError && msg.ToolCallID == "t1" {
				hasErrorResult = true
			}
		}
	}
	if !hasErrorResult {
		t.Error("expected ChatToolResultMsg with IsError=true for nil executor")
	}
}

func TestStripRegionalPrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"us.anthropic.claude-3-5-sonnet-20241022-v2:0", "anthropic.claude-3-5-sonnet-20241022-v2:0"},
		{"eu.anthropic.claude-3-5-sonnet-20241022-v2:0", "anthropic.claude-3-5-sonnet-20241022-v2:0"},
		{"ap.anthropic.claude-3-5-sonnet-20241022-v2:0", "anthropic.claude-3-5-sonnet-20241022-v2:0"},
		{"anthropic.claude-3-5-sonnet-20241022-v2:0", "anthropic.claude-3-5-sonnet-20241022-v2:0"},
		{"custom-model", "custom-model"},
	}
	for _, tt := range tests {
		got := stripRegionalPrefix(tt.input)
		if got != tt.want {
			t.Errorf("stripRegionalPrefix(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestGetModelInfoCaching(t *testing.T) {
	listCallCount := 0
	prov := &countingMockProvider{
		models: []provider.ModelInfo{
			{ID: "anthropic.claude-3-5-sonnet-20241022-v2:0"},
		},
		callCount: &listCallCount,
	}
	notifier := &mockNotifier{}
	session := NewSession("test-session-id", prov, NewTracker(nil, nil), notifier, "us.anthropic.claude-3-5-sonnet-20241022-v2:0", "system", 1024, &mockExecutor{}, nil, nil, nil)

	// First call — should hit ListModels
	info1, err := session.getModelInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info1 == nil {
		t.Fatal("expected non-nil model info")
	}
	if info1.ID != "anthropic.claude-3-5-sonnet-20241022-v2:0" {
		t.Errorf("model ID = %q, want base ID", info1.ID)
	}

	// Second call — should use cache, not call ListModels again
	info2, err := session.getModelInfo(context.Background())
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if info2 != info1 {
		t.Error("expected same pointer from cache")
	}
	if listCallCount != 1 {
		t.Errorf("ListModels called %d times, want 1", listCallCount)
	}
}

// countingMockProvider tracks how many times ListModels is called.
type countingMockProvider struct {
	models    []provider.ModelInfo
	callCount *int
}

func (p *countingMockProvider) Send(_ context.Context, _ provider.Request) (provider.StreamIterator, error) {
	return nil, fmt.Errorf("not implemented")
}

func (p *countingMockProvider) ListModels(_ context.Context) ([]provider.ModelInfo, error) {
	*p.callCount++
	return p.models, nil
}

func TestContextWarning50Percent(t *testing.T) {
	// Setup: Model with 1000 token context window
	// Per-response usage: Bedrock reports full-conversation totals per call
	// First response: 400 input + 100 output = 50% exactly → triggers warning
	// Second response: 500 input + 100 output = 60% → no new warning (warned50=true)
	// Expected: ContextWarningEvent fired once, not twice

	model := provider.ModelInfo{
		ID:              "test-model",
		Name:            "Test Model",
		ContextWindow:   1000,
		InputCostPer1M:  1.0,
		OutputCostPer1M: 5.0,
	}

	// First response: 400 input + 100 output = 500 tokens (50%)
	chunks1 := []provider.StreamChunk{
		{Event: provider.EventTextDelta, Text: "First response"},
		{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 400, OutputTokens: 100}},
	}

	// Second response: Bedrock reports growing totals (500 input + 100 output = 60%)
	chunks2 := []provider.StreamChunk{
		{Event: provider.EventTextDelta, Text: "Second response"},
		{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 500, OutputTokens: 100}},
	}

	prov := &mockProvider{calls: [][]provider.StreamChunk{chunks1, chunks2}}
	prov.models = []provider.ModelInfo{model}

	notifier := &mockNotifier{}
	tracker := NewTracker(nil, nil)
	session := NewSession("test-session-id", prov, tracker, notifier, "test-model", "system", 1024, &mockExecutor{}, nil, nil, nil)

	// First message: should trigger warning at 50%
	err := session.processUserMessage(context.Background(), "First")
	if err != nil {
		t.Fatalf("first message failed: %v", err)
	}

	// Second message: should NOT trigger warning again (warned50=true)
	err = session.processUserMessage(context.Background(), "Second")
	if err != nil {
		t.Fatalf("second message failed: %v", err)
	}

	// Verify events
	msgs := notifier.getMessages()
	var warningCount int
	var updateCount int
	for _, m := range msgs {
		switch msg := m.(type) {
		case ContextWarningEvent:
			warningCount++
			if msg.Percentage < 50.0 || msg.Percentage > 51.0 {
				t.Errorf("warning percentage = %.1f, want ~50.0", msg.Percentage)
			}
			if msg.Threshold != 50.0 {
				t.Errorf("warning threshold = %.1f, want 50.0", msg.Threshold)
			}
		case ContextUpdateEvent:
			updateCount++
		}
	}

	if warningCount != 1 {
		t.Errorf("ContextWarningEvent count = %d, want 1 (warning should fire once)", warningCount)
	}
	if updateCount != 2 {
		t.Errorf("ContextUpdateEvent count = %d, want 2 (one per response)", updateCount)
	}
}

func TestContextAutoCompactAt90Percent(t *testing.T) {
	// Setup: Model with 1000 token context window
	// Response with 900 total tokens (90%)
	// Auto-compaction is deferred until after tool loop; with only 2 messages
	// in history (below compactionMinHistory=6), compaction fails gracefully.

	model := provider.ModelInfo{
		ID:              "test-model",
		Name:            "Test Model",
		ContextWindow:   1000,
		InputCostPer1M:  1.0,
		OutputCostPer1M: 5.0,
	}

	// Response: 720 input + 180 output = 900 tokens (90%)
	chunks := []provider.StreamChunk{
		{Event: provider.EventTextDelta, Text: "Large response"},
		{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 720, OutputTokens: 180}},
	}

	prov := &mockProvider{calls: [][]provider.StreamChunk{chunks}}
	prov.models = []provider.ModelInfo{model}

	notifier := &mockNotifier{}
	tracker := NewTracker(nil, nil)
	session := NewSession("test-session-id", prov, tracker, notifier, "test-model", "system", 1024, &mockExecutor{}, nil, nil, nil)

	err := session.processUserMessage(context.Background(), "Large prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify events
	msgs := notifier.getMessages()
	var hasAutoCompact bool
	var hasWarning bool
	var hasUpdate bool
	var hasError bool
	for _, m := range msgs {
		switch msg := m.(type) {
		case ContextAutoCompactEvent:
			hasAutoCompact = true
			if msg.Percentage < 90.0 {
				t.Errorf("auto-compact percentage = %.1f, want >= 90.0", msg.Percentage)
			}
		case ContextWarningEvent:
			hasWarning = true
		case ContextUpdateEvent:
			hasUpdate = true
			if msg.Percentage < 90.0 {
				t.Errorf("update percentage = %.1f, want >= 90.0", msg.Percentage)
			}
		case ErrorEvent:
			if strings.Contains(msg.Error, "auto-compaction failed") {
				hasError = true
			}
		}
	}

	if !hasAutoCompact {
		t.Error("expected ContextAutoCompactEvent at 90%")
	}
	if hasWarning {
		t.Error("should not have ContextWarningEvent when >= 90% (auto-compact takes precedence)")
	}
	if !hasUpdate {
		t.Error("expected ContextUpdateEvent")
	}
	if !hasError {
		t.Error("expected ErrorEvent for auto-compaction failure (history too short)")
	}
}

func TestContextUpdateEveryResponse(t *testing.T) {
	// Setup: Multiple responses with increasing token counts
	// Bedrock reports full-conversation totals per call, so InputTokens grows
	// Responses: ~10%, ~20%, ~33%, ~42% (all below 50%)

	model := provider.ModelInfo{
		ID:              "test-model",
		Name:            "Test Model",
		ContextWindow:   1000,
		InputCostPer1M:  1.0,
		OutputCostPer1M: 5.0,
	}

	// Four responses with growing per-response totals (Bedrock convention)
	chunks := [][]provider.StreamChunk{
		{{Event: provider.EventTextDelta, Text: "Response 1"},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 80, OutputTokens: 20}}},
		{{Event: provider.EventTextDelta, Text: "Response 2"},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 160, OutputTokens: 40}}},
		{{Event: provider.EventTextDelta, Text: "Response 3"},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 250, OutputTokens: 60}}},
		{{Event: provider.EventTextDelta, Text: "Response 4"},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 340, OutputTokens: 80}}},
	}

	prov := &mockProvider{calls: chunks}
	prov.models = []provider.ModelInfo{model}

	notifier := &mockNotifier{}
	tracker := NewTracker(nil, nil)
	session := NewSession("test-session-id", prov, tracker, notifier, "test-model", "system", 1024, &mockExecutor{}, nil, nil, nil)

	// Send four messages
	for i := 1; i <= 4; i++ {
		err := session.processUserMessage(context.Background(), fmt.Sprintf("Message %d", i))
		if err != nil {
			t.Fatalf("message %d failed: %v", i, err)
		}
	}

	// Verify events
	msgs := notifier.getMessages()
	var updateCount int
	var percentages []float64
	for _, m := range msgs {
		switch msg := m.(type) {
		case ContextUpdateEvent:
			updateCount++
			percentages = append(percentages, msg.Percentage)
		case ContextWarningEvent:
			t.Error("should not have warning (all below 50%)")
		case ContextAutoCompactEvent:
			t.Error("should not have auto-compact (all below 90%)")
		}
	}

	if updateCount != 4 {
		t.Errorf("ContextUpdateEvent count = %d, want 4 (one per response)", updateCount)
	}

	// Verify percentages increase
	for i := 1; i < len(percentages); i++ {
		if percentages[i] <= percentages[i-1] {
			t.Errorf("percentage[%d] = %.1f should be > percentage[%d] = %.1f", i, percentages[i], i-1, percentages[i-1])
		}
	}
}

func TestManualCompaction(t *testing.T) {
	// Test /compact command with sufficient history
	model := provider.ModelInfo{
		ID:              "test-model",
		Name:            "Test Model",
		ContextWindow:   10000,
		InputCostPer1M:  1.0,
		OutputCostPer1M: 5.0,
	}

	// Build up history with multiple messages
	// Early messages have large token counts, recent messages are smaller
	// This ensures compaction (which preserves recent 4 messages) saves significant tokens
	longResponse := strings.Repeat("This is a detailed response explaining the implementation. ", 15)
	shortResponse := "Brief reply."
	chunks := [][]provider.StreamChunk{
		{{Event: provider.EventTextDelta, Text: longResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 200, OutputTokens: 150}}},
		{{Event: provider.EventTextDelta, Text: longResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 250, OutputTokens: 150}}},
		{{Event: provider.EventTextDelta, Text: longResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 300, OutputTokens: 150}}},
		{{Event: provider.EventTextDelta, Text: longResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 350, OutputTokens: 150}}},
		{{Event: provider.EventTextDelta, Text: longResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 400, OutputTokens: 150}}},
		// Recent messages are shorter (these will be preserved)
		{{Event: provider.EventTextDelta, Text: shortResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 420, OutputTokens: 10}}},
		{{Event: provider.EventTextDelta, Text: shortResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 430, OutputTokens: 10}}},
		{{Event: provider.EventTextDelta, Text: shortResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 440, OutputTokens: 10}}},
		// Compaction summary response (very short)
		textChunks("Summary."),
	}

	prov := &mockProvider{calls: chunks, models: []provider.ModelInfo{model}}
	notifier := &mockNotifier{}
	tracker := NewTracker(nil, nil)
	session := NewSession("test-session-id", prov, tracker, notifier, "test-model", "system", 1024, &mockExecutor{}, nil, nil, nil)

	// Build up conversation with longer user messages (8 exchanges)
	longUserMsg := strings.Repeat("Can you explain the implementation details? ", 12)
	for i := 1; i <= 8; i++ {
		err := session.processUserMessage(context.Background(), longUserMsg)
		if err != nil {
			t.Fatalf("message %d failed: %v", i, err)
		}
	}

	// Record history length before compaction
	historyBefore := len(session.history)

	// Run /compact command
	err := session.processUserMessage(context.Background(), "/compact")
	if err != nil {
		t.Fatalf("compaction failed: %v", err)
	}

	// Verify events
	msgs := notifier.getMessages()
	var hasStart, hasComplete bool
	var oldTokens, newTokens int
	for _, m := range msgs {
		switch msg := m.(type) {
		case CompactionStartEvent:
			hasStart = true
			if msg.Mode != "manual" {
				t.Errorf("mode = %q, want %q", msg.Mode, "manual")
			}
		case CompactionCompleteEvent:
			hasComplete = true
			oldTokens = msg.OldTokens
			newTokens = msg.NewTokens
		case CompactionFailedEvent:
			t.Errorf("unexpected CompactionFailedEvent: %s", msg.Error)
		}
	}

	if !hasStart {
		t.Error("expected CompactionStartEvent")
	}
	if !hasComplete {
		t.Error("expected CompactionCompleteEvent")
	}
	if newTokens >= oldTokens {
		t.Errorf("compaction didn't reduce tokens: %d → %d", oldTokens, newTokens)
	}

	// Verify history was compacted (should have summary + recent messages)
	historyAfter := len(session.history)
	if historyAfter >= historyBefore {
		t.Errorf("history length not reduced: %d → %d", historyBefore, historyAfter)
	}

	// Verify first message is summary
	if !strings.Contains(session.history[0].Content, "[Conversation Summary]") {
		t.Error("first message should be summary")
	}
}

func TestCompactionWithShortHistory(t *testing.T) {
	model := provider.ModelInfo{
		ID:              "test-model",
		Name:            "Test Model",
		ContextWindow:   1000,
		InputCostPer1M:  1.0,
		OutputCostPer1M: 5.0,
	}

	t.Run("empty_history", func(t *testing.T) {
		// Test compaction fails gracefully with 0 messages
		prov := &mockProvider{calls: [][]provider.StreamChunk{}, models: []provider.ModelInfo{model}}
		notifier := &mockNotifier{}
		tracker := NewTracker(nil, nil)
		session := NewSession("test-session-id", prov, tracker, notifier, "test-model", "system", 1024, &mockExecutor{}, nil, nil, nil)

		err := session.processUserMessage(context.Background(), "/compact")
		if err == nil {
			t.Fatal("expected compaction to fail with short history")
		}

		msgs := notifier.getMessages()
		var hasFailedEvent bool
		for _, m := range msgs {
			if msg, ok := m.(CompactionFailedEvent); ok {
				hasFailedEvent = true
				if !strings.Contains(msg.Error, "too short") {
					t.Errorf("error message = %q, want 'too short'", msg.Error)
				}
			}
		}
		if !hasFailedEvent {
			t.Error("expected CompactionFailedEvent")
		}
	})

	t.Run("four_messages_below_threshold", func(t *testing.T) {
		// 4 messages (2 exchanges) passes old threshold of 2 but fails new threshold of 6
		chunks := [][]provider.StreamChunk{
			textChunks("Response 1"),
			textChunks("Response 2"),
		}
		prov := &mockProvider{calls: chunks, models: []provider.ModelInfo{model}}
		notifier := &mockNotifier{}
		tracker := NewTracker(nil, nil)
		session := NewSession("test-session-id", prov, tracker, notifier, "test-model", "system", 1024, &mockExecutor{}, nil, nil, nil)

		// Build 4 messages (2 user + 2 assistant)
		for i := 1; i <= 2; i++ {
			err := session.processUserMessage(context.Background(), fmt.Sprintf("Message %d", i))
			if err != nil {
				t.Fatalf("message %d failed: %v", i, err)
			}
		}

		// Try to compact with 4 messages (below compactionMinHistory of 6)
		err := session.processUserMessage(context.Background(), "/compact")
		if err == nil {
			t.Fatal("expected compaction to fail with 4 messages (below threshold of 6)")
		}

		msgs := notifier.getMessages()
		var hasFailedEvent bool
		for _, m := range msgs {
			if msg, ok := m.(CompactionFailedEvent); ok {
				hasFailedEvent = true
				if !strings.Contains(msg.Error, "too short") {
					t.Errorf("error message = %q, want 'too short'", msg.Error)
				}
			}
		}
		if !hasFailedEvent {
			t.Error("expected CompactionFailedEvent")
		}
	})
}

func TestCompactionPreservesRecentMessages(t *testing.T) {
	// Test that last 4 messages are preserved verbatim
	model := provider.ModelInfo{
		ID:              "test-model",
		Name:            "Test Model",
		ContextWindow:   10000,
		InputCostPer1M:  1.0,
		OutputCostPer1M: 5.0,
	}

	// Build up 8 messages (4 user + 4 assistant)
	chunks := [][]provider.StreamChunk{
		textChunks("Response 1"),
		textChunks("Response 2"),
		textChunks("Response 3"),
		textChunks("Response 4"),
		textChunks("Response 5"),
		textChunks("Response 6"),
		textChunks("Response 7"),
		textChunks("Response 8"),
		// Compaction summary
		textChunks("Summary..."),
	}

	prov := &mockProvider{calls: chunks, models: []provider.ModelInfo{model}}
	notifier := &mockNotifier{}
	tracker := NewTracker(nil, nil)
	session := NewSession("test-session-id", prov, tracker, notifier, "test-model", "system", 1024, &mockExecutor{}, nil, nil, nil)

	// Build conversation
	for i := 1; i <= 8; i++ {
		err := session.processUserMessage(context.Background(), fmt.Sprintf("Message %d", i))
		if err != nil {
			t.Fatalf("message %d failed: %v", i, err)
		}
	}

	// Record last 4 messages before compaction
	recentBefore := make([]provider.Message, 4)
	copy(recentBefore, session.history[len(session.history)-4:])

	// Compact
	err := session.processUserMessage(context.Background(), "/compact")
	if err != nil {
		t.Fatalf("compaction failed: %v", err)
	}

	// Verify last 4 messages are preserved
	if len(session.history) < 5 { // summary + 4 recent
		t.Fatalf("history too short after compaction: %d", len(session.history))
	}

	recentAfter := session.history[len(session.history)-4:]
	for i := 0; i < 4; i++ {
		if recentAfter[i].Content != recentBefore[i].Content {
			t.Errorf("message %d changed: %q → %q", i, recentBefore[i].Content, recentAfter[i].Content)
		}
	}
}

func TestCompactionResetsWarned50(t *testing.T) {
	// Test that warned50 flag resets after successful compaction, allowing
	// the 50% warning to fire again. Uses per-response usage (Bedrock convention).
	model := provider.ModelInfo{
		ID:              "test-model",
		Name:            "Test Model",
		ContextWindow:   1000,
		InputCostPer1M:  1.0,
		OutputCostPer1M: 5.0,
	}

	// Use longer responses so compaction actually reduces token count
	longResponse := strings.Repeat("This is a detailed response explaining the implementation. ", 10)

	chunks := [][]provider.StreamChunk{
		// Response 1: 50% per-response usage → triggers warning
		{
			{Event: provider.EventTextDelta, Text: longResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 400, OutputTokens: 100}},
		},
		// Responses 2-4: below 50%
		{
			{Event: provider.EventTextDelta, Text: longResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 100, OutputTokens: 20}},
		},
		{
			{Event: provider.EventTextDelta, Text: longResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 150, OutputTokens: 30}},
		},
		{
			{Event: provider.EventTextDelta, Text: longResponse},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 200, OutputTokens: 40}},
		},
		// Compaction summary (short)
		textChunks("Summary of the conversation."),
		// Post-compact response: 50% again → should re-trigger warning
		{
			{Event: provider.EventTextDelta, Text: "After compact"},
			{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 400, OutputTokens: 100}},
		},
	}

	prov := &mockProvider{calls: chunks, models: []provider.ModelInfo{model}}
	notifier := &mockNotifier{}
	tracker := NewTracker(nil, nil)
	session := NewSession("test-session-id", prov, tracker, notifier, "test-model", "system", 1024, &mockExecutor{}, nil, nil, nil)

	// Use longer user messages for meaningful compaction
	longUserMsg := strings.Repeat("Can you explain the implementation details? ", 8)

	// Trigger first 50% warning
	err := session.processUserMessage(context.Background(), longUserMsg)
	if err != nil {
		t.Fatalf("first message failed: %v", err)
	}

	// Build up more history (need >= compactionMinHistory messages)
	for i := 2; i <= 4; i++ {
		err := session.processUserMessage(context.Background(), longUserMsg)
		if err != nil {
			t.Fatalf("message %d failed: %v", i, err)
		}
	}

	// Compact
	err = session.processUserMessage(context.Background(), "/compact")
	if err != nil {
		t.Fatalf("compaction failed: %v", err)
	}

	// Send message that should trigger 50% warning again (warned50 was reset)
	err = session.processUserMessage(context.Background(), "After compact")
	if err != nil {
		t.Fatalf("post-compact message failed: %v", err)
	}

	// Verify we got 2 warnings (before and after compaction)
	msgs := notifier.getMessages()
	warningCount := 0
	for _, m := range msgs {
		if _, ok := m.(ContextWarningEvent); ok {
			warningCount++
		}
	}

	if warningCount != 2 {
		t.Errorf("warning count = %d, want 2 (before and after compaction)", warningCount)
	}
}

func TestAutoCompactionDeferredDuringToolUse(t *testing.T) {
	// Verify that auto-compaction is deferred until after tool loop completes.
	// Scenario: tool_use response reports 90% context → compaction should NOT
	// fire mid-loop, only after end_turn.

	model := provider.ModelInfo{
		ID:              "test-model",
		Name:            "Test Model",
		ContextWindow:   1000,
		InputCostPer1M:  1.0,
		OutputCostPer1M: 5.0,
	}

	// Round 1: tool_use with 90% context usage
	toolChunks := []provider.StreamChunk{
		{Event: provider.EventToolStart, ToolCallID: "t1", ToolName: "get_weather"},
		{Event: provider.EventToolDelta, InputDelta: `{"location":"Rome"}`},
		{Event: provider.EventToolEnd},
		{Event: provider.EventMessageStop, StopReason: "tool_use", Usage: &provider.Usage{InputTokens: 720, OutputTokens: 180}},
	}
	// Round 2: end_turn
	endChunks := []provider.StreamChunk{
		{Event: provider.EventTextDelta, Text: "Weather is sunny."},
		{Event: provider.EventMessageStop, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 750, OutputTokens: 200}},
	}

	prov := &mockProvider{
		calls:  [][]provider.StreamChunk{toolChunks, endChunks},
		models: []provider.ModelInfo{model},
	}
	notifier := &mockNotifier{}
	tracker := NewTracker(nil, nil)
	session := NewSession("test-session-id", prov, tracker, notifier, "test-model", "system", 1024,
		&mockExecutor{results: map[string]string{"get_weather": `{"temp":"22°C"}`}}, nil, nil, nil)

	err := session.processUserMessage(context.Background(), "What's the weather?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify tool loop completed normally:
	// user → assistant(tool) → user(result) → assistant(text)
	if len(session.history) != 4 {
		t.Fatalf("history length = %d, want 4 (tool loop should complete fully before compaction attempt)", len(session.history))
	}

	msgs := notifier.getMessages()

	// ContextAutoCompactEvent should have been sent (flag was set during tool loop)
	var hasAutoCompact bool
	for _, m := range msgs {
		if _, ok := m.(ContextAutoCompactEvent); ok {
			hasAutoCompact = true
		}
	}
	if !hasAutoCompact {
		t.Error("expected ContextAutoCompactEvent at 90%")
	}

	// Compaction attempt should fail (history too short) but AFTER tool loop
	var hasCompactionFailure bool
	for _, m := range msgs {
		switch m.(type) {
		case ErrorEvent:
			hasCompactionFailure = true
		case CompactionFailedEvent:
			hasCompactionFailure = true
		}
	}
	if !hasCompactionFailure {
		t.Error("expected compaction failure event (history too short for compaction)")
	}

	// Verify ordering: all ToolExecutionEvents come before any compaction-related events
	lastToolExecIdx := -1
	firstCompactIdx := -1
	for i, m := range msgs {
		switch m.(type) {
		case ToolExecutionEvent:
			lastToolExecIdx = i
		case CompactionFailedEvent:
			if firstCompactIdx == -1 {
				firstCompactIdx = i
			}
		}
	}
	if firstCompactIdx != -1 && lastToolExecIdx != -1 && firstCompactIdx < lastToolExecIdx {
		t.Errorf("compaction event at index %d appeared before last tool execution at index %d", firstCompactIdx, lastToolExecIdx)
	}
}

// TestSession_AuditLogging verifies that tool executions are logged to the audit trail.
func TestSession_AuditLogging(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mock provider with tool use response
	chunks := []provider.StreamChunk{
		{Event: provider.EventToolStart, ToolCallID: "call_1", ToolName: "get_weather"},
		{Event: provider.EventToolDelta, InputDelta: `{"city":"SF"}`},
		{Event: provider.EventToolEnd},
		{Event: provider.EventMessageStop, StopReason: "tool_use"},
	}
	chunks2 := textChunks("The weather is nice.")

	prov := &mockProvider{calls: [][]provider.StreamChunk{chunks, chunks2}}
	executor := &mockExecutor{results: map[string]string{"get_weather": `{"temp":"22°C"}`}}
	notifier := &mockNotifier{}
	tracker := NewTracker(nil, nil)

	// Create audit logger
	sessionID := "test-session-audit-123"
	auditLogger, err := policy.NewAuditLogger(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("NewAuditLogger failed: %v", err)
	}
	defer auditLogger.Close()

	session := NewSession(sessionID, prov, tracker, notifier, "test-model", "system", 1024, executor, nil, auditLogger, nil)

	// Process message with tool execution
	err = session.processUserMessage(context.Background(), "What's the weather?")
	if err != nil {
		t.Fatalf("processUserMessage failed: %v", err)
	}

	// Close audit logger to flush
	session.Stop()

	// Read audit log and verify entry exists
	entries, err := policy.ReadAuditLog(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("ReadAuditLog failed: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Tool != "get_weather" {
		t.Errorf("tool mismatch: got %s, want get_weather", entry.Tool)
	}
	if entry.ToolCallID != "call_1" {
		t.Errorf("tool_call_id mismatch: got %s, want call_1", entry.ToolCallID)
	}
	if entry.Decision != "allowed" {
		t.Errorf("decision mismatch: got %s, want allowed", entry.Decision)
	}
	if entry.SessionID != sessionID {
		t.Errorf("session_id mismatch: got %s, want %s", entry.SessionID, sessionID)
	}
	if entry.Timestamp == "" {
		t.Error("timestamp is empty")
	}

	// Verify arguments were logged
	if entry.Arguments == nil {
		t.Error("arguments is nil")
	} else if city, ok := entry.Arguments["city"]; !ok || city != "SF" {
		t.Errorf("arguments[city] mismatch: got %v, want SF", city)
	}
}

// TestSession_AuditLoggingError verifies that tool execution errors are logged.
func TestSession_AuditLoggingError(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mock provider with tool use response
	chunks := []provider.StreamChunk{
		{Event: provider.EventToolStart, ToolCallID: "call_err", ToolName: "failing_tool"},
		{Event: provider.EventToolDelta, InputDelta: `{"input":"data"}`},
		{Event: provider.EventToolEnd},
		{Event: provider.EventMessageStop, StopReason: "tool_use"},
	}
	chunks2 := textChunks("Tool failed, let me try something else.")

	prov := &mockProvider{calls: [][]provider.StreamChunk{chunks, chunks2}}
	executor := &mockExecutor{errors: map[string]error{"failing_tool": fmt.Errorf("permission denied")}}
	notifier := &mockNotifier{}
	tracker := NewTracker(nil, nil)

	// Create audit logger
	sessionID := "test-session-audit-error"
	auditLogger, err := policy.NewAuditLogger(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("NewAuditLogger failed: %v", err)
	}
	defer auditLogger.Close()

	session := NewSession(sessionID, prov, tracker, notifier, "test-model", "system", 1024, executor, nil, auditLogger, nil)

	// Process message with failing tool
	err = session.processUserMessage(context.Background(), "Run the failing tool")
	if err != nil {
		t.Fatalf("processUserMessage failed: %v", err)
	}

	session.Stop()

	// Read audit log and verify error entry
	entries, err := policy.ReadAuditLog(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("ReadAuditLog failed: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(entries))
	}

	entry := entries[0]
	if entry.Decision != "denied" {
		t.Errorf("decision mismatch for error: got %s, want denied", entry.Decision)
	}
	if entry.Error == "" {
		t.Error("error field should contain error message")
	}
	if !strings.Contains(entry.Error, "permission denied") {
		t.Errorf("error message mismatch: got %s, want to contain 'permission denied'", entry.Error)
	}
}

// TestSession_ShutdownCoordination verifies clean shutdown with in-flight operations.
func TestSession_ShutdownCoordination(t *testing.T) {
	// Create slow executor that simulates long-running tool
	slowExecutor := &slowExecutor{delay: 100 * time.Millisecond}

	chunks := []provider.StreamChunk{
		{Event: provider.EventToolStart, ToolCallID: "call_slow", ToolName: "slow_tool"},
		{Event: provider.EventToolDelta, InputDelta: `{}`},
		{Event: provider.EventToolEnd},
		{Event: provider.EventMessageStop, StopReason: "tool_use"},
	}
	chunks2 := textChunks("Done.")

	prov := &mockProvider{calls: [][]provider.StreamChunk{chunks, chunks2}}
	notifier := &mockNotifier{}
	tracker := NewTracker(nil, nil)

	session := NewSession("test-shutdown", prov, tracker, notifier, "test-model", "system", 1024, slowExecutor, nil, nil, nil)

	ctx := context.Background()
	session.Start(ctx)

	// Submit message that will trigger slow tool execution
	session.SubmitMessage("Run slow tool")

	// Give it a moment to start processing
	time.Sleep(20 * time.Millisecond)

	// Stop session while tool is executing
	// This should NOT panic (previous bug: would close audit logger before processUserMessage finishes)
	session.Stop()

	// Verify executor was called (WaitGroup properly tracked the operation)
	if slowExecutor.calls == 0 {
		t.Error("executor was not called - WaitGroup may have blocked submission")
	}

	// Verify session stopped cleanly (no panic = success)
	// The test passes if we reach this point without crashing
}

// slowExecutor simulates a long-running tool execution
type slowExecutor struct {
	delay time.Duration
	mu    sync.Mutex
	calls int
}

func (e *slowExecutor) Execute(ctx context.Context, name string, _ map[string]any) (string, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()

	// Simulate slow operation
	select {
	case <-time.After(e.delay):
		return "slow operation completed", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// --- Test helpers for permissions ---

func createTestEvaluator(t *testing.T) (*policy.Evaluator, string) {
	t.Helper()
	// Create temporary policy file
	tmpDir := t.TempDir()
	policyPath := fmt.Sprintf("%s/policy.json", tmpDir)

	// Create empty policy file (evaluator will use manifest rules)
	if err := os.WriteFile(policyPath, []byte(`{"version":1,"overrides":{}}`), 0644); err != nil {
		t.Fatalf("failed to create test policy file: %v", err)
	}

	evaluator, err := policy.NewEvaluator(policyPath)
	if err != nil {
		t.Fatalf("failed to create evaluator: %v", err)
	}

	return evaluator, policyPath
}

// --- Permission flow tests ---

func TestPermissionRequestFlow_Allow(t *testing.T) {
	prov := &mockProvider{calls: [][]provider.StreamChunk{
		toolUseChunks("tool_1", "mock_permission_tool", `{"content":"test"}`),
		textChunks("Tool executed successfully!"),
	}}
	notifier := &mockNotifier{}
	executor := &mockExecutor{
		results: map[string]string{
			"mock_permission_tool": "Successfully wrote 4 bytes to ./test.txt",
		},
	}
	evaluator, _ := createTestEvaluator(t)

	session := NewSession(
		"test-session-id", prov, NewTracker(nil, nil), notifier,
		"test-model", "system", 1024, executor, nil, nil, evaluator,
	)

	errChan := make(chan error, 1)
	go func() {
		errChan <- session.processUserMessage(context.Background(), "Use mock_permission_tool to write 'test'")
	}()

	// Wait for PermissionRequestEvent (no sleep — poll with timeout)
	evt, ok := notifier.waitForEvent(func(m any) bool {
		_, is := m.(PermissionRequestEvent)
		return is
	}, 5*time.Second)
	if !ok {
		t.Fatal("timed out waiting for PermissionRequestEvent")
	}
	permRequest := evt.(PermissionRequestEvent)

	if permRequest.ToolName != "mock_permission_tool" {
		t.Errorf("ToolName = %q, want %q", permRequest.ToolName, "mock_permission_tool")
	}
	if permRequest.Permission != "fs:write:./test.txt" {
		t.Errorf("Permission = %q, want %q", permRequest.Permission, "fs:write:./test.txt")
	}

	// Simulate user approval
	permRequest.ResponseChan <- PermissionResponse{Allowed: true, Remember: false}

	if err := <-errChan; err != nil {
		t.Fatalf("processUserMessage failed: %v", err)
	}

	// Verify tool was executed (history should have tool result)
	if len(session.history) < 3 {
		t.Fatalf("history length = %d, want at least 3", len(session.history))
	}
	foundToolResult := false
	for _, msg := range session.history {
		if msg.Role == provider.RoleUser {
			for _, tr := range msg.ToolResults {
				if tr.ToolUseID == "tool_1" {
					foundToolResult = true
					if tr.IsError {
						t.Errorf("tool result has IsError=true, want false")
					}
				}
			}
		}
	}
	if !foundToolResult {
		t.Error("expected tool result in history after permission granted")
	}
}

func TestPermissionRequestFlow_Deny(t *testing.T) {
	prov := &mockProvider{calls: [][]provider.StreamChunk{
		toolUseChunks("tool_1", "mock_permission_tool", `{"content":"test"}`),
		textChunks("Permission was denied."),
	}}
	notifier := &mockNotifier{}
	executor := &mockExecutor{
		results: map[string]string{
			"mock_permission_tool": "Should not be executed",
		},
	}
	evaluator, _ := createTestEvaluator(t)

	session := NewSession(
		"test-session-id", prov, NewTracker(nil, nil), notifier,
		"test-model", "system", 1024, executor, nil, nil, evaluator,
	)

	errChan := make(chan error, 1)
	go func() {
		errChan <- session.processUserMessage(context.Background(), "Use mock_permission_tool to write 'test'")
	}()

	evt, ok := notifier.waitForEvent(func(m any) bool {
		_, is := m.(PermissionRequestEvent)
		return is
	}, 5*time.Second)
	if !ok {
		t.Fatal("timed out waiting for PermissionRequestEvent")
	}
	permRequest := evt.(PermissionRequestEvent)

	// Simulate user denial
	permRequest.ResponseChan <- PermissionResponse{Allowed: false, Remember: false}

	if err := <-errChan; err != nil {
		t.Fatalf("processUserMessage failed: %v", err)
	}

	// Verify tool was NOT executed (result should be a permission denied error)
	foundErrorResult := false
	for _, msg := range session.history {
		if msg.Role == provider.RoleUser {
			for _, tr := range msg.ToolResults {
				if tr.ToolUseID == "tool_1" && tr.IsError && strings.Contains(tr.Content, "Permission denied") {
					foundErrorResult = true
				}
			}
		}
	}
	if !foundErrorResult {
		t.Error("expected tool result with permission denial error")
	}
}

func TestPermissionRequestFlow_Timeout(t *testing.T) {
	prov := &mockProvider{calls: [][]provider.StreamChunk{
		toolUseChunks("tool_1", "mock_permission_tool", `{"content":"test"}`),
		textChunks("Permission timed out."),
	}}
	notifier := &mockNotifier{}
	executor := &mockExecutor{
		results: map[string]string{
			"mock_permission_tool": "Should not be executed",
		},
	}
	evaluator, _ := createTestEvaluator(t)

	session := NewSession(
		"test-session-id", prov, NewTracker(nil, nil), notifier,
		"test-model", "system", 1024, executor, nil, nil, evaluator,
	)
	session.permissionTimeout = 50 * time.Millisecond // Very short timeout for test

	errChan := make(chan error, 1)
	go func() {
		errChan <- session.processUserMessage(context.Background(), "Use mock_permission_tool to write 'test'")
	}()

	// Wait for PermissionRequestEvent but do NOT respond — let it time out
	_, ok := notifier.waitForEvent(func(m any) bool {
		_, is := m.(PermissionRequestEvent)
		return is
	}, 5*time.Second)
	if !ok {
		t.Fatal("timed out waiting for PermissionRequestEvent")
	}

	// Wait for processUserMessage to complete (it should time out and proceed)
	if err := <-errChan; err != nil {
		t.Fatalf("processUserMessage failed: %v", err)
	}

	// Verify PermissionTimeoutEvent was emitted
	_, gotTimeout := notifier.waitForEvent(func(m any) bool {
		_, is := m.(PermissionTimeoutEvent)
		return is
	}, 5*time.Second)
	if !gotTimeout {
		t.Error("expected PermissionTimeoutEvent after timeout")
	}

	// Verify tool result is a permission denied error
	foundErrorResult := false
	for _, msg := range session.history {
		if msg.Role == provider.RoleUser {
			for _, tr := range msg.ToolResults {
				if tr.ToolUseID == "tool_1" && tr.IsError && strings.Contains(tr.Content, "timed out") {
					foundErrorResult = true
				}
			}
		}
	}
	if !foundErrorResult {
		t.Error("expected tool result with timeout error")
	}
}

func TestPermissionRequestFlow_ContextCancelled(t *testing.T) {
	prov := &mockProvider{calls: [][]provider.StreamChunk{
		toolUseChunks("tool_1", "mock_permission_tool", `{"content":"test"}`),
		textChunks("Context cancelled."),
	}}
	notifier := &mockNotifier{}
	executor := &mockExecutor{
		results: map[string]string{
			"mock_permission_tool": "Should not be executed",
		},
	}
	evaluator, _ := createTestEvaluator(t)

	session := NewSession(
		"test-session-id", prov, NewTracker(nil, nil), notifier,
		"test-model", "system", 1024, executor, nil, nil, evaluator,
	)

	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error, 1)
	go func() {
		errChan <- session.processUserMessage(ctx, "Use mock_permission_tool to write 'test'")
	}()

	// Wait for PermissionRequestEvent
	_, ok := notifier.waitForEvent(func(m any) bool {
		_, is := m.(PermissionRequestEvent)
		return is
	}, 5*time.Second)
	if !ok {
		t.Fatal("timed out waiting for PermissionRequestEvent")
	}

	// Cancel context instead of responding
	cancel()

	if err := <-errChan; err != nil {
		t.Fatalf("processUserMessage failed: %v", err)
	}

	// Verify tool result is a cancellation error
	foundErrorResult := false
	for _, msg := range session.history {
		if msg.Role == provider.RoleUser {
			for _, tr := range msg.ToolResults {
				if tr.ToolUseID == "tool_1" && tr.IsError && strings.Contains(tr.Content, "cancelled") {
					foundErrorResult = true
				}
			}
		}
	}
	if !foundErrorResult {
		t.Error("expected tool result with cancellation error")
	}
}
