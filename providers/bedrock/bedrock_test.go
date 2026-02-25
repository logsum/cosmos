package bedrock

import (
	"context"
	"cosmos/core/provider"
	"errors"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// Compile-time check: Bedrock satisfies Provider.
var _ provider.Provider = (*Bedrock)(nil)

// --- Role conversion tests ---

func TestToBedrockRole(t *testing.T) {
	got, err := toBedrockRole(provider.RoleUser)
	if err != nil {
		t.Fatalf("RoleUser: unexpected error: %v", err)
	}
	if got != brtypes.ConversationRoleUser {
		t.Errorf("RoleUser: got %q, want %q", got, brtypes.ConversationRoleUser)
	}

	got, err = toBedrockRole(provider.RoleAssistant)
	if err != nil {
		t.Fatalf("RoleAssistant: unexpected error: %v", err)
	}
	if got != brtypes.ConversationRoleAssistant {
		t.Errorf("RoleAssistant: got %q, want %q", got, brtypes.ConversationRoleAssistant)
	}
}

func TestToBedrockRoleUnknown(t *testing.T) {
	_, err := toBedrockRole(provider.Role("system"))
	if err == nil {
		t.Fatal("expected error for unknown role, got nil")
	}
	_, err = toBedrockRole(provider.Role(""))
	if err == nil {
		t.Fatal("expected error for empty role, got nil")
	}
}

// --- Message conversion tests ---

func TestToBedrockMessages(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "Hello"},
		{
			Role:    provider.RoleAssistant,
			Content: "I'll help.",
			ToolCalls: []provider.ToolCall{
				{ID: "tc1", Name: "readFile", Input: map[string]any{"path": "/tmp/x"}},
			},
		},
		{
			Role: provider.RoleUser,
			ToolResults: []provider.ToolResult{
				{ToolUseID: "tc1", Content: "file contents", IsError: false},
			},
		},
		{
			Role: provider.RoleUser,
			ToolResults: []provider.ToolResult{
				{ToolUseID: "tc2", Content: "not found", IsError: true},
			},
		},
	}

	out, err := toBedrockMessages(msgs)
	if err != nil {
		t.Fatalf("toBedrockMessages: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(out))
	}

	// Message 0: simple text.
	if out[0].Role != brtypes.ConversationRoleUser {
		t.Errorf("msg 0 role: got %q", out[0].Role)
	}
	if len(out[0].Content) != 1 {
		t.Fatalf("msg 0: expected 1 content block, got %d", len(out[0].Content))
	}
	if textBlock, ok := out[0].Content[0].(*brtypes.ContentBlockMemberText); !ok {
		t.Errorf("msg 0 block 0: expected text, got %T", out[0].Content[0])
	} else if textBlock.Value != "Hello" {
		t.Errorf("msg 0 text: got %q", textBlock.Value)
	}

	// Message 1: text + tool use (2 content blocks).
	if len(out[1].Content) != 2 {
		t.Fatalf("msg 1: expected 2 content blocks, got %d", len(out[1].Content))
	}
	if _, ok := out[1].Content[0].(*brtypes.ContentBlockMemberText); !ok {
		t.Errorf("msg 1 block 0: expected text, got %T", out[1].Content[0])
	}
	toolUseBlock, ok := out[1].Content[1].(*brtypes.ContentBlockMemberToolUse)
	if !ok {
		t.Fatalf("msg 1 block 1: expected tool use, got %T", out[1].Content[1])
	}
	if aws.ToString(toolUseBlock.Value.Name) != "readFile" {
		t.Errorf("tool use name: got %q", aws.ToString(toolUseBlock.Value.Name))
	}
	if aws.ToString(toolUseBlock.Value.ToolUseId) != "tc1" {
		t.Errorf("tool use id: got %q", aws.ToString(toolUseBlock.Value.ToolUseId))
	}

	// Message 2: successful tool result.
	if len(out[2].Content) != 1 {
		t.Fatalf("msg 2: expected 1 content block, got %d", len(out[2].Content))
	}
	resultBlock, ok := out[2].Content[0].(*brtypes.ContentBlockMemberToolResult)
	if !ok {
		t.Fatalf("msg 2 block 0: expected tool result, got %T", out[2].Content[0])
	}
	if resultBlock.Value.Status != brtypes.ToolResultStatusSuccess {
		t.Errorf("msg 2 status: got %q", resultBlock.Value.Status)
	}
	if aws.ToString(resultBlock.Value.ToolUseId) != "tc1" {
		t.Errorf("msg 2 tool use id: got %q", aws.ToString(resultBlock.Value.ToolUseId))
	}

	// Message 3: error tool result.
	errResult, ok := out[3].Content[0].(*brtypes.ContentBlockMemberToolResult)
	if !ok {
		t.Fatalf("msg 3 block 0: expected tool result, got %T", out[3].Content[0])
	}
	if errResult.Value.Status != brtypes.ToolResultStatusError {
		t.Errorf("msg 3 status: got %q, want %q", errResult.Value.Status, brtypes.ToolResultStatusError)
	}
}

func TestToBedrockMessageUnknownRole(t *testing.T) {
	_, err := toBedrockMessage(provider.Message{Role: "moderator", Content: "hi"})
	if err == nil {
		t.Fatal("expected error for unknown role, got nil")
	}
}

func TestToBedrockMessageEmpty(t *testing.T) {
	_, err := toBedrockMessage(provider.Message{Role: provider.RoleUser})
	if err == nil {
		t.Fatal("expected error for empty message, got nil")
	}
}

func TestToBedrockMessagesPropagatesToBedrockMessageError(t *testing.T) {
	_, err := toBedrockMessages([]provider.Message{
		{Role: provider.RoleUser, Content: "ok"},
		{Role: provider.Role("bad"), Content: "nope"},
	})
	if err == nil {
		t.Fatal("expected error from bad role in second message")
	}
}

// --- Tool config tests ---

func TestToBedrockToolConfig(t *testing.T) {
	tools := []provider.ToolDefinition{
		{
			Name:        "analyzeFile",
			Description: "Analyze a source file",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []any{"path"},
			},
		},
	}

	tc, err := toBedrockToolConfig(tools)
	if err != nil {
		t.Fatalf("toBedrockToolConfig: %v", err)
	}
	if len(tc.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tc.Tools))
	}

	spec, ok := tc.Tools[0].(*brtypes.ToolMemberToolSpec)
	if !ok {
		t.Fatalf("expected ToolMemberToolSpec, got %T", tc.Tools[0])
	}
	if aws.ToString(spec.Value.Name) != "analyzeFile" {
		t.Errorf("tool name: got %q", aws.ToString(spec.Value.Name))
	}
	if aws.ToString(spec.Value.Description) != "Analyze a source file" {
		t.Errorf("tool description: got %q", aws.ToString(spec.Value.Description))
	}

	_, ok = spec.Value.InputSchema.(*brtypes.ToolInputSchemaMemberJson)
	if !ok {
		t.Fatalf("expected ToolInputSchemaMemberJson, got %T", spec.Value.InputSchema)
	}
}

// --- Request building tests ---

func TestBuildConverseStreamInput(t *testing.T) {
	req := provider.Request{
		Model:     "anthropic.claude-sonnet-4-20250514-v1:0",
		System:    "You are helpful.",
		MaxTokens: 2048,
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "Hi"},
		},
	}

	input, err := buildConverseStreamInput(req)
	if err != nil {
		t.Fatalf("buildConverseStreamInput: %v", err)
	}

	if aws.ToString(input.ModelId) != req.Model {
		t.Errorf("model: got %q", aws.ToString(input.ModelId))
	}
	if len(input.System) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(input.System))
	}
	if aws.ToInt32(input.InferenceConfig.MaxTokens) != 2048 {
		t.Errorf("max tokens: got %d", aws.ToInt32(input.InferenceConfig.MaxTokens))
	}
	if input.ToolConfig != nil {
		t.Error("expected nil ToolConfig when no tools")
	}
}

func TestBuildConverseStreamInputDefaults(t *testing.T) {
	req := provider.Request{
		Model:    "anthropic.claude-3-haiku-20240307-v1:0",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "Hi"}},
	}

	input, err := buildConverseStreamInput(req)
	if err != nil {
		t.Fatalf("buildConverseStreamInput: %v", err)
	}

	if aws.ToInt32(input.InferenceConfig.MaxTokens) != int32(defaultMaxTokens) {
		t.Errorf("default max tokens: got %d, want %d",
			aws.ToInt32(input.InferenceConfig.MaxTokens), defaultMaxTokens)
	}
	if len(input.System) != 0 {
		t.Errorf("expected no system blocks, got %d", len(input.System))
	}
}

func TestBuildConverseStreamInputBadRole(t *testing.T) {
	req := provider.Request{
		Model:    "model",
		Messages: []provider.Message{{Role: "bad", Content: "hi"}},
	}
	_, err := buildConverseStreamInput(req)
	if err == nil {
		t.Fatal("expected error for bad role, got nil")
	}
}

// --- Error classification tests ---

type stubAPIError struct {
	code    string
	message string
}

func (e *stubAPIError) Error() string                  { return e.code + ": " + e.message }
func (e *stubAPIError) ErrorCode() string              { return e.code }
func (e *stubAPIError) ErrorMessage() string           { return e.message }
func (e *stubAPIError) ErrorFault() smithy.ErrorFault  { return smithy.FaultServer }

func TestClassifyErr(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantSent error
	}{
		{"nil", nil, nil},
		{"throttling", &stubAPIError{code: "ThrottlingException", message: "slow down"}, provider.ErrThrottled},
		{"access denied", &stubAPIError{code: "AccessDeniedException", message: "nope"}, provider.ErrAccessDenied},
		{"resource not found", &stubAPIError{code: "ResourceNotFoundException", message: "gone"}, provider.ErrModelNotFound},
		{"model not found", &stubAPIError{code: "ModelNotFoundException", message: "no model"}, provider.ErrModelNotFound},
		{"model not ready", &stubAPIError{code: "ModelNotReadyException", message: "warming"}, provider.ErrModelNotReady},
		{"unknown API error", &stubAPIError{code: "ValidationException", message: "bad"}, nil},
		{"generic error", errors.New("timeout"), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyErr(tt.err)
			if tt.err == nil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if tt.wantSent != nil {
				if !errors.Is(got, tt.wantSent) {
					t.Errorf("expected errors.Is(%v, %v) = true", got, tt.wantSent)
				}
			} else if got == nil {
				t.Error("expected non-nil error")
			}
		})
	}
}

// --- ListModels tests ---

type stubCatalog struct {
	summaries []bedrocktypes.FoundationModelSummary
	err       error
}

func (s *stubCatalog) ListFoundationModels(_ context.Context, _ *bedrock.ListFoundationModelsInput, _ ...func(*bedrock.Options)) (*bedrock.ListFoundationModelsOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &bedrock.ListFoundationModelsOutput{ModelSummaries: s.summaries}, nil
}

func TestListModelsFiltersAndEnriches(t *testing.T) {
	catalog := &stubCatalog{
		summaries: []bedrocktypes.FoundationModelSummary{
			{
				// Known model with streaming support — should be enriched.
				ModelId:                    aws.String("anthropic.claude-3-haiku-20240307-v1:0"),
				ModelName:                  aws.String("Claude 3 Haiku"),
				ResponseStreamingSupported: aws.Bool(true),
				OutputModalities:           []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText},
			},
			{
				// Unknown model with streaming — should appear with name only.
				ModelId:                    aws.String("anthropic.claude-4-5-sonnet-20260101-v1:0"),
				ModelName:                  aws.String("Claude 4.5 Sonnet"),
				ResponseStreamingSupported: aws.Bool(true),
				OutputModalities:           []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText},
			},
			{
				// No streaming support — should be filtered out.
				ModelId:                    aws.String("anthropic.claude-3-instant-v1"),
				ModelName:                  aws.String("Claude 3 Instant"),
				ResponseStreamingSupported: aws.Bool(false),
				OutputModalities:           []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText},
			},
			{
				// Embedding model — no text output — should be filtered out.
				ModelId:                    aws.String("anthropic.claude-embed-v1"),
				ModelName:                  aws.String("Claude Embed"),
				ResponseStreamingSupported: aws.Bool(true),
				OutputModalities:           []bedrocktypes.ModelModality{bedrocktypes.ModelModalityEmbedding},
			},
			{
				// Nil streaming flag — should be filtered out.
				ModelId:          aws.String("anthropic.claude-nil-streaming"),
				ModelName:        aws.String("Claude Nil"),
				OutputModalities: []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText},
			},
		},
	}

	b := &Bedrock{catalog: catalog}
	models, err := b.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d: %+v", len(models), models)
	}

	// First: known model, enriched with static data.
	if models[0].ID != "anthropic.claude-3-haiku-20240307-v1:0" {
		t.Errorf("model 0 ID: got %q", models[0].ID)
	}
	if models[0].ContextWindow != 200_000 {
		t.Errorf("model 0 context window: got %d", models[0].ContextWindow)
	}
	if models[0].InputCostPer1M != 0.25 {
		t.Errorf("model 0 input cost: got %f", models[0].InputCostPer1M)
	}

	// Second: unknown model, basic info only.
	if models[1].ID != "anthropic.claude-4-5-sonnet-20260101-v1:0" {
		t.Errorf("model 1 ID: got %q", models[1].ID)
	}
	if models[1].Name != "Claude 4.5 Sonnet" {
		t.Errorf("model 1 name: got %q", models[1].Name)
	}
	if models[1].ContextWindow != 0 {
		t.Errorf("model 1 context window: expected 0, got %d", models[1].ContextWindow)
	}
}

func TestListModelsAPIError(t *testing.T) {
	catalog := &stubCatalog{err: &stubAPIError{code: "AccessDeniedException", message: "no"}}
	b := &Bedrock{catalog: catalog}
	_, err := b.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, provider.ErrAccessDenied) {
		t.Errorf("expected provider.ErrAccessDenied, got %v", err)
	}
}

// --- Iterator tests ---

type fakeStream struct {
	ch     chan brtypes.ConverseStreamOutput
	closed bool
	err    error
}

func newFakeStream(events ...brtypes.ConverseStreamOutput) *fakeStream {
	ch := make(chan brtypes.ConverseStreamOutput, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return &fakeStream{ch: ch}
}

func (f *fakeStream) Events() <-chan brtypes.ConverseStreamOutput { return f.ch }
func (f *fakeStream) Close() error                                { f.closed = true; return nil }
func (f *fakeStream) Err() error                                  { return f.err }

func TestIteratorTextStream(t *testing.T) {
	stream := newFakeStream(
		&brtypes.ConverseStreamOutputMemberMessageStart{},
		&brtypes.ConverseStreamOutputMemberContentBlockStart{
			Value: brtypes.ContentBlockStartEvent{
				ContentBlockIndex: aws.Int32(0),
			},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockDelta{
			Value: brtypes.ContentBlockDeltaEvent{
				Delta: &brtypes.ContentBlockDeltaMemberText{Value: "Hello"},
			},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockDelta{
			Value: brtypes.ContentBlockDeltaEvent{
				Delta: &brtypes.ContentBlockDeltaMemberText{Value: " world"},
			},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockStop{
			Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: aws.Int32(0)},
		},
		&brtypes.ConverseStreamOutputMemberMessageStop{
			Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonEndTurn},
		},
		&brtypes.ConverseStreamOutputMemberMetadata{
			Value: brtypes.ConverseStreamMetadataEvent{
				Usage: &brtypes.TokenUsage{
					InputTokens:  aws.Int32(10),
					OutputTokens: aws.Int32(5),
					TotalTokens:  aws.Int32(15),
				},
			},
		},
	)

	iter := &bedrockIterator{stream: stream, events: stream.Events()}
	var chunks []provider.StreamChunk
	for {
		chunk, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %+v", len(chunks), chunks)
	}

	if chunks[0].Event != provider.EventTextDelta || chunks[0].Text != "Hello" {
		t.Errorf("chunk 0: got %+v", chunks[0])
	}
	if chunks[1].Event != provider.EventTextDelta || chunks[1].Text != " world" {
		t.Errorf("chunk 1: got %+v", chunks[1])
	}
	if chunks[2].Event != provider.EventMessageStop || chunks[2].StopReason != "end_turn" {
		t.Errorf("chunk 2: got %+v", chunks[2])
	}
	if chunks[2].Usage == nil || chunks[2].Usage.InputTokens != 10 || chunks[2].Usage.OutputTokens != 5 {
		t.Errorf("chunk 2 usage: got %+v", chunks[2].Usage)
	}
}

func TestIteratorToolUseStream(t *testing.T) {
	stream := newFakeStream(
		&brtypes.ConverseStreamOutputMemberMessageStart{},
		&brtypes.ConverseStreamOutputMemberContentBlockStart{
			Value: brtypes.ContentBlockStartEvent{ContentBlockIndex: aws.Int32(0)},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockDelta{
			Value: brtypes.ContentBlockDeltaEvent{
				Delta: &brtypes.ContentBlockDeltaMemberText{Value: "Let me check."},
			},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockStop{
			Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: aws.Int32(0)},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockStart{
			Value: brtypes.ContentBlockStartEvent{
				ContentBlockIndex: aws.Int32(1),
				Start: &brtypes.ContentBlockStartMemberToolUse{
					Value: brtypes.ToolUseBlockStart{
						ToolUseId: aws.String("tc_abc"),
						Name:      aws.String("readFile"),
					},
				},
			},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockDelta{
			Value: brtypes.ContentBlockDeltaEvent{
				Delta: &brtypes.ContentBlockDeltaMemberToolUse{
					Value: brtypes.ToolUseBlockDelta{Input: aws.String(`{"path":`)},
				},
			},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockDelta{
			Value: brtypes.ContentBlockDeltaEvent{
				Delta: &brtypes.ContentBlockDeltaMemberToolUse{
					Value: brtypes.ToolUseBlockDelta{Input: aws.String(`"/tmp/x"}`)},
				},
			},
		},
		&brtypes.ConverseStreamOutputMemberContentBlockStop{
			Value: brtypes.ContentBlockStopEvent{ContentBlockIndex: aws.Int32(1)},
		},
		&brtypes.ConverseStreamOutputMemberMessageStop{
			Value: brtypes.MessageStopEvent{StopReason: brtypes.StopReasonToolUse},
		},
		&brtypes.ConverseStreamOutputMemberMetadata{
			Value: brtypes.ConverseStreamMetadataEvent{
				Usage: &brtypes.TokenUsage{
					InputTokens:  aws.Int32(50),
					OutputTokens: aws.Int32(30),
					TotalTokens:  aws.Int32(80),
				},
			},
		},
	)

	iter := &bedrockIterator{stream: stream, events: stream.Events()}
	var chunks []provider.StreamChunk
	for {
		chunk, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		chunks = append(chunks, chunk)
	}

	if len(chunks) != 6 {
		t.Fatalf("expected 6 chunks, got %d", len(chunks))
	}

	if chunks[0].Event != provider.EventTextDelta {
		t.Errorf("chunk 0: expected TextDelta, got %d", chunks[0].Event)
	}
	if chunks[1].Event != provider.EventToolStart || chunks[1].ToolCallID != "tc_abc" || chunks[1].ToolName != "readFile" {
		t.Errorf("chunk 1: got %+v", chunks[1])
	}
	if chunks[2].Event != provider.EventToolDelta || chunks[2].InputDelta != `{"path":` {
		t.Errorf("chunk 2: got %+v", chunks[2])
	}
	if chunks[3].Event != provider.EventToolDelta || chunks[3].InputDelta != `"/tmp/x"}` {
		t.Errorf("chunk 3: got %+v", chunks[3])
	}
	if chunks[4].Event != provider.EventToolEnd {
		t.Errorf("chunk 4: expected ToolEnd, got %d", chunks[4].Event)
	}
	if chunks[5].Event != provider.EventMessageStop || chunks[5].StopReason != "tool_use" {
		t.Errorf("chunk 5: got %+v", chunks[5])
	}
}

func TestIteratorStreamError(t *testing.T) {
	stream := newFakeStream(
		&brtypes.ConverseStreamOutputMemberContentBlockDelta{
			Value: brtypes.ContentBlockDeltaEvent{
				Delta: &brtypes.ContentBlockDeltaMemberText{Value: "partial"},
			},
		},
	)
	stream.err = errors.New("connection reset")

	iter := &bedrockIterator{stream: stream, events: stream.Events()}

	chunk, err := iter.Next()
	if err != nil {
		t.Fatalf("first Next: unexpected error %v", err)
	}
	if chunk.Text != "partial" {
		t.Errorf("first chunk text: got %q", chunk.Text)
	}

	_, err = iter.Next()
	if err == nil {
		t.Fatal("expected error after stream error, got nil")
	}
}

func TestIteratorClose(t *testing.T) {
	stream := newFakeStream()
	iter := &bedrockIterator{stream: stream, events: stream.Events()}
	if err := iter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !stream.closed {
		t.Error("expected stream to be closed")
	}

	_, err := iter.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF after Close, got %v", err)
	}
}
