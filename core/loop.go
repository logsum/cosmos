package core

import (
	"context"
	"cosmos/core/provider"
	"cosmos/engine/policy"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

const (
	// compactionPreserveRecent is the number of most recent messages to preserve during compaction.
	compactionPreserveRecent = 4

	// compactionTargetRatio is the target summary length as a percentage of original.
	compactionTargetRatio = 0.25 // 25% of original

	// compactionMinReduction is the minimum reduction percentage required for compaction to be worthwhile.
	compactionMinReduction = 20.0 // Must reduce by at least 20%

	// compactionMinHistory is the minimum number of messages needed for compaction to be meaningful.
	// With fewer messages, preserving recent ones + adding a summary would likely increase token count.
	compactionMinHistory = compactionPreserveRecent + 2

	// compactionPromptTemplate is the prompt sent to the LLM for summarization.
	compactionPromptTemplate = `You are tasked with summarizing a coding conversation to reduce token usage while preserving all critical information.

**Guidelines:**
- Preserve all technical decisions, code snippets, file paths, and function names
- Maintain chronological order of key developments
- Omit pleasantries, redundant explanations, and off-topic tangents
- Use concise technical language
- Target length: ~25%% of original

**Conversation to Summarize:**
%s

**Instructions:**
Provide a dense, technical summary that captures:
1. Main objectives and problems addressed
2. Key decisions made (with brief rationale)
3. Code changes and their locations
4. Current state and next steps

Write the summary in markdown format. Be extremely concise.`
)

// ToolExecutor runs a tool and returns its result.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, input map[string]any) (string, error)
}

// Session manages a single LLM conversation loop
type Session struct {
	provider provider.Provider
	tracker  *Tracker
	notifier Notifier // UI update channel
	executor ToolExecutor
	tools    []provider.ToolDefinition

	model     string
	systemMsg string
	maxTokens int

	id          string              // UUID v4, generated at creation
	auditLogger *policy.AuditLogger // nil if audit disabled

	mu          sync.Mutex
	history     []provider.Message
	userMsgChan chan string
	stopChan    chan struct{}
	stopOnce    sync.Once
	wg          sync.WaitGroup // Tracks in-flight operations (loop, message processing)

	cachedModelInfo *provider.ModelInfo
	modelInfoOnce   sync.Once

	warned50 bool // Track if 50% context warning already sent (reset after compaction)
}

// Notifier interface for UI updates. The Send method accepts any event type;
// the adapter in main.go translates core events into framework-specific messages.
type Notifier interface {
	Send(msg any)
}

// NewSession creates a new conversation session
func NewSession(
	sessionID string,
	prov provider.Provider,
	tracker *Tracker,
	notifier Notifier,
	model string,
	systemMsg string,
	maxTokens int,
	executor ToolExecutor,
	tools []provider.ToolDefinition,
	auditLogger *policy.AuditLogger,
) *Session {
	return &Session{
		provider:    prov,
		tracker:     tracker,
		notifier:    notifier,
		model:       model,
		systemMsg:   systemMsg,
		maxTokens:   maxTokens,
		executor:    executor,
		tools:       tools,
		id:          sessionID,
		auditLogger: auditLogger,
		history:     []provider.Message{},
		userMsgChan: make(chan string, 16), // Buffered for responsiveness
		stopChan:    make(chan struct{}),
	}
}

// SubmitMessage queues a user message for processing
func (s *Session) SubmitMessage(text string) {
	select {
	case s.userMsgChan <- text:
	case <-s.stopChan:
		// Session stopped, drop message
	}
}

// Start begins the background conversation loop
func (s *Session) Start(ctx context.Context) {
	s.wg.Add(1)
	go s.loop(ctx)
}

// Stop gracefully terminates the session. It is safe to call multiple times.
func (s *Session) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopChan)
		s.wg.Wait() // Wait for loop and in-flight message processing to complete
		if s.auditLogger != nil {
			if err := s.auditLogger.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "cosmos: audit log close failed: %v\n", err)
			}
		}
	})
}

// ID returns the session's unique identifier.
func (s *Session) ID() string {
	return s.id
}

// loop is the main goroutine that processes user messages
func (s *Session) loop(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopChan:
			return
		case userText := <-s.userMsgChan:
			s.wg.Add(1)
			if err := s.processUserMessage(ctx, userText); err != nil {
				// Send error to UI
				s.notifier.Send(ErrorEvent{Error: err.Error()})
			}
			s.wg.Done()
		}
	}
}

// pendingToolCall accumulates streaming fragments for a single tool call.
type pendingToolCall struct {
	id        string
	name      string
	inputJSON strings.Builder
}

// processUserMessage handles one user prompt through a multi-turn LLM loop.
// It continues looping as long as the model requests tool use, and exits
// when the model produces a final text response (end_turn).
func (s *Session) processUserMessage(ctx context.Context, text string) error {
	// Check for commands before adding to history
	if text == "/compact" {
		return s.handleCompactCommand(ctx)
	}

	// Append user message to history
	s.mu.Lock()
	s.history = append(s.history, provider.Message{
		Role:    provider.RoleUser,
		Content: text,
	})
	s.mu.Unlock()

	var autoCompactPending bool

	for {
		// Build request from current history
		s.mu.Lock()
		conversationCopy := append([]provider.Message{}, s.history...)
		s.mu.Unlock()

		req := provider.Request{
			Model:     s.model,
			System:    s.systemMsg,
			Messages:  conversationCopy,
			Tools:     s.tools,
			MaxTokens: s.maxTokens,
		}

		// Send to provider
		iter, err := s.provider.Send(ctx, req)
		if err != nil {
			return fmt.Errorf("provider send failed: %w", err)
		}

		// Stream response — accumulate text and tool calls
		var fullText strings.Builder
		var toolCalls []provider.ToolCall
		var pending *pendingToolCall
		var usage *provider.Usage
		var stopReason string

		for {
			chunk, err := iter.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				iter.Close()
				return fmt.Errorf("stream error: %w", err)
			}

			switch chunk.Event {
			case provider.EventTextDelta:
				fullText.WriteString(chunk.Text)
				s.notifier.Send(TokenEvent{Text: chunk.Text})

			case provider.EventToolStart:
				pending = &pendingToolCall{
					id:   chunk.ToolCallID,
					name: chunk.ToolName,
				}

			case provider.EventToolDelta:
				if pending != nil {
					pending.inputJSON.WriteString(chunk.InputDelta)
				}

			case provider.EventToolEnd:
				if pending != nil {
					var input map[string]any
					if raw := pending.inputJSON.String(); raw != "" {
						if err := json.Unmarshal([]byte(raw), &input); err != nil {
							input = map[string]any{"_raw": raw}
						}
					}
					toolCalls = append(toolCalls, provider.ToolCall{
						ID:    pending.id,
						Name:  pending.name,
						Input: input,
					})
					pending = nil
				}

			case provider.EventMessageStop:
				usage = chunk.Usage
				stopReason = chunk.StopReason
			}
		}
		iter.Close()

		// Record token usage
		if usage != nil {
			modelInfo, err := s.getModelInfo(ctx)
			if err == nil && modelInfo != nil {
				s.tracker.Record(*modelInfo, *usage, SourcePrompt)

				// Monitor context usage — use THIS response's tokens
				// (Bedrock reports full-conversation total per call)
				pct := 0.0
				if modelInfo.ContextWindow > 0 {
					pct = float64(usage.InputTokens+usage.OutputTokens) / float64(modelInfo.ContextWindow) * 100.0
				}

				// Always update status bar with current percentage
				s.notifier.Send(ContextUpdateEvent{
					Percentage: pct,
					ModelID:    s.model,
				})

				// Check thresholds
				if pct >= 90.0 {
					// Defer compaction until after tool loop completes
					autoCompactPending = true
					s.notifier.Send(ContextAutoCompactEvent{
						Percentage: pct,
						ModelID:    s.model,
					})
				} else if pct >= 50.0 {
					s.mu.Lock()
					shouldWarn := !s.warned50
					if shouldWarn {
						s.warned50 = true
					}
					s.mu.Unlock()
					if shouldWarn {
						s.notifier.Send(ContextWarningEvent{
							Percentage: pct,
							Threshold:  50.0,
							ModelID:    s.model,
						})
					}
				}
			}
		}

		// Check if this is a tool-use turn
		if stopReason == "tool_use" && len(toolCalls) > 0 {
			// Append assistant message with text + tool calls
			s.mu.Lock()
			s.history = append(s.history, provider.Message{
				Role:      provider.RoleAssistant,
				Content:   fullText.String(),
				ToolCalls: toolCalls,
			})
			s.mu.Unlock()

			// Dispatch each tool call and collect results
			var toolResults []provider.ToolResult
			for _, tc := range toolCalls {
				// Notify UI of tool invocation
				inputJSON, _ := json.Marshal(tc.Input)
				s.notifier.Send(ToolUseEvent{
					ToolCallID: tc.ID,
					ToolName:   tc.Name,
					Input:      string(inputJSON),
				})

				// Execute tool
				var tr provider.ToolResult
				if s.executor == nil {
					tr = provider.ToolResult{
						ToolUseID: tc.ID,
						Content:   "no tool executor configured",
						IsError:   true,
					}
				} else {
					result, execErr := s.executor.Execute(ctx, tc.Name, tc.Input)
					tr = provider.ToolResult{
						ToolUseID: tc.ID,
						Content:   result,
					}
					if execErr != nil {
						tr.Content = execErr.Error()
						tr.IsError = true
					}
				}
				toolResults = append(toolResults, tr)

				// Notify UI of result
				s.notifier.Send(ToolResultEvent{
					ToolCallID: tc.ID,
					ToolName:   tc.Name,
					Result:     tr.Content,
					IsError:    tr.IsError,
				})

				// Send full execution data for agents page
				s.notifier.Send(ToolExecutionEvent{
					ToolCallID: tc.ID,
					ToolName:   tc.Name,
					Input:      string(inputJSON),
					Output:     tr.Content,
					IsError:    tr.IsError,
				})

				// Log to audit trail
				if s.auditLogger != nil {
					if err := s.auditLogger.Log(policy.AuditEntry{
						Agent:      "stub",  // Will be agent name once loader is implemented
						Tool:       tc.Name,
						Permission: "stub",  // Will be actual permission once policy integration is complete
						Decision:   decisionFromError(tr.IsError),
						Source:     "manifest",
						Arguments:  tc.Input,
						ToolCallID: tc.ID,
						Error:      errorString(tr),
					}); err != nil {
						fmt.Fprintf(os.Stderr, "cosmos: audit log failed: %v\n", err)
					}
				}
			}

			// Append tool results as a user message (Bedrock convention)
			s.mu.Lock()
			s.history = append(s.history, provider.Message{
				Role:        provider.RoleUser,
				ToolResults: toolResults,
			})
			s.mu.Unlock()

			// Signal completion of this turn, then loop for next LLM call
			s.notifier.Send(CompletionEvent{})
			continue
		}

		// Final text response — append and break out of tool loop
		s.mu.Lock()
		content := fullText.String()
		if content == "" {
			content = "(No response)"
		}
		s.history = append(s.history, provider.Message{
			Role:    provider.RoleAssistant,
			Content: content,
		})
		s.mu.Unlock()

		s.notifier.Send(CompletionEvent{})
		break
	}

	// Deferred auto-compaction (runs after tool loop is fully complete)
	if autoCompactPending {
		if err := s.performCompaction(ctx, "automatic"); err != nil {
			s.notifier.Send(ErrorEvent{Error: "auto-compaction failed: " + err.Error()})
		} else {
			// Update status bar with post-compaction percentage
			modelInfo, err := s.getModelInfo(ctx)
			if err == nil && modelInfo != nil && modelInfo.ContextWindow > 0 {
				s.mu.Lock()
				newPct := float64(s.estimateTokenCount(s.history)) / float64(modelInfo.ContextWindow) * 100.0
				s.mu.Unlock()
				s.notifier.Send(ContextUpdateEvent{
					Percentage: newPct,
					ModelID:    s.model,
				})
			}
		}
	}

	return nil
}

// stripRegionalPrefix removes a Bedrock regional prefix (e.g. "us.", "eu.", "ap.")
// from a model ID, returning the base model ID.
func stripRegionalPrefix(modelID string) string {
	prefixes := []string{"us.", "eu.", "ap."}
	for _, p := range prefixes {
		if after, found := strings.CutPrefix(modelID, p); found {
			return after
		}
	}
	return modelID
}

// getModelInfo retrieves model info for pricing, caching the result after the
// first successful lookup to avoid repeated ListModels API calls.
// Returns nil if not found (non-fatal).
func (s *Session) getModelInfo(ctx context.Context) (*provider.ModelInfo, error) {
	var fetchErr error
	s.modelInfoOnce.Do(func() {
		models, err := s.provider.ListModels(ctx)
		if err != nil {
			fetchErr = err
			return
		}

		baseModel := stripRegionalPrefix(s.model)
		for _, m := range models {
			if m.ID == s.model || m.ID == baseModel {
				info := m
				s.cachedModelInfo = &info
				return
			}
		}
	})
	if fetchErr != nil {
		// Reset Once so next call retries on transient errors
		s.modelInfoOnce = sync.Once{}
		return nil, fetchErr
	}
	return s.cachedModelInfo, nil
}

// handleCompactCommand processes the /compact user command.
func (s *Session) handleCompactCommand(ctx context.Context) error {
	return s.performCompaction(ctx, "manual")
}

// performCompaction executes the actual compaction logic (shared by manual and auto).
// It summarizes conversation history, replaces it with a condensed version, and adjusts token counts.
func (s *Session) performCompaction(ctx context.Context, mode string) error {
	s.mu.Lock()

	// 1. Validate minimum history
	if len(s.history) < compactionMinHistory {
		s.mu.Unlock()
		err := fmt.Errorf("conversation too short to compact (need at least %d messages, have %d)", compactionMinHistory, len(s.history))
		s.notifier.Send(CompactionFailedEvent{Error: err.Error()})
		return err
	}

	// 2. Estimate old token count (character-based, same unit as newTokenCount)
	oldTokens := s.estimateTokenCount(s.history)

	s.mu.Unlock()

	// Notify UI (after validation, before work begins)
	s.notifier.Send(CompactionStartEvent{Mode: mode})

	// 3. Generate summary
	s.notifier.Send(CompactionProgressEvent{Stage: "generating_summary"})
	summary, err := s.generateSummary(ctx)
	if err != nil {
		errMsg := fmt.Sprintf("failed to generate summary: %v", err)
		s.notifier.Send(CompactionFailedEvent{Error: errMsg})
		return fmt.Errorf("failed to generate summary: %w", err)
	}

	// 4. Build new history with summary + recent messages
	s.mu.Lock()
	newHistory := s.buildCompactedHistory(summary)
	s.mu.Unlock()

	// 5. Estimate token count for new history
	s.notifier.Send(CompactionProgressEvent{Stage: "estimating_tokens"})
	newTokenCount := s.estimateTokenCount(newHistory)

	// 6. Validate compaction achieved reduction
	if newTokenCount >= oldTokens {
		err := fmt.Errorf("summary would increase token count (%d → %d)", oldTokens, newTokenCount)
		s.notifier.Send(CompactionFailedEvent{Error: err.Error()})
		return err
	}

	reductionPct := 100.0 * float64(oldTokens-newTokenCount) / float64(oldTokens)
	if reductionPct < compactionMinReduction {
		err := fmt.Errorf("insufficient reduction (%.0f%%), compaction not worthwhile", reductionPct)
		s.notifier.Send(CompactionFailedEvent{Error: err.Error()})
		return err
	}

	// 7. Commit changes (point of no return)
	s.mu.Lock()
	s.history = newHistory
	s.warned50 = false // Reset warning flag for fresh warnings
	s.mu.Unlock()

	// 8. Notify UI of success
	s.notifier.Send(CompactionCompleteEvent{
		OldTokens: oldTokens,
		NewTokens: newTokenCount,
	})

	return nil
}

// generateSummary sends conversation history to LLM for summarization.
// Returns the summary text or an error.
func (s *Session) generateSummary(ctx context.Context) (string, error) {
	s.mu.Lock()

	// Split history into "to summarize" and "to preserve"
	preserveCount := compactionPreserveRecent
	if len(s.history) <= preserveCount {
		preserveCount = 0 // No point summarizing if all would be preserved
	}

	historyToSummarize := s.history[:len(s.history)-preserveCount]
	s.mu.Unlock()

	// Build conversation text for summarization
	var conversationText strings.Builder
	for _, msg := range historyToSummarize {
		role := "User"
		if msg.Role == provider.RoleAssistant {
			role = "Assistant"
		}
		conversationText.WriteString(fmt.Sprintf("\n## %s\n%s\n", role, msg.Content))

		// Include tool calls/results if present
		for _, tc := range msg.ToolCalls {
			inputJSON, _ := json.Marshal(tc.Input)
			conversationText.WriteString(fmt.Sprintf("\n[Tool: %s]\nInput: %s\n", tc.Name, inputJSON))
		}
		for _, tr := range msg.ToolResults {
			conversationText.WriteString(fmt.Sprintf("\n[Tool Result]\n%s\n", tr.Content))
		}
	}

	// Build summarization request
	targetTokens := int(float64(s.estimateTokenCount(historyToSummarize)) * compactionTargetRatio * 1.5) // 1.5x target for safety

	summaryPrompt := fmt.Sprintf(compactionPromptTemplate, conversationText.String())

	req := provider.Request{
		Model:   s.model,
		System:  "You are a technical summarizer for a coding assistant.",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: summaryPrompt},
		},
		MaxTokens: targetTokens,
	}

	// Stream summary from LLM
	iter, err := s.provider.Send(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to request summary: %w", err)
	}
	defer iter.Close()

	var summary strings.Builder
	for {
		chunk, err := iter.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("summary stream error: %w", err)
		}
		if chunk.Event == provider.EventTextDelta {
			summary.WriteString(chunk.Text)
		}
	}

	return summary.String(), nil
}

// buildCompactedHistory creates new history with summary and recent messages preserved.
// Caller must hold s.mu lock.
func (s *Session) buildCompactedHistory(summary string) []provider.Message {
	// Preserve recent messages
	preserveCount := compactionPreserveRecent
	if len(s.history) < preserveCount {
		preserveCount = len(s.history)
	}
	recentMessages := s.history[len(s.history)-preserveCount:]

	// Build new history: [summary] + [recent messages]
	summaryMsg := provider.Message{
		Role:    provider.RoleAssistant,
		Content: "**[Conversation Summary]**\n\n" + summary,
	}

	newHistory := []provider.Message{summaryMsg}
	newHistory = append(newHistory, recentMessages...)

	return newHistory
}

// decisionFromError converts tool execution error status to audit decision.
func decisionFromError(isError bool) string {
	if isError {
		return "denied"
	}
	return "allowed"
}

// errorString extracts error message from tool result.
func errorString(tr provider.ToolResult) string {
	if tr.IsError {
		return tr.Content
	}
	return ""
}

// estimateTokenCount estimates token count using character heuristic.
// Claude averages ~1.3 characters per token. We use 1.2 to be conservative.
func (s *Session) estimateTokenCount(messages []provider.Message) int {
	totalChars := 0

	// Count system message if present
	totalChars += len(s.systemMsg)

	// Count all message content
	for _, msg := range messages {
		totalChars += len(msg.Content)

		// Tool calls/results also contribute
		for _, tc := range msg.ToolCalls {
			totalChars += len(tc.Name) + 50 // Name + metadata overhead
			inputJSON, _ := json.Marshal(tc.Input)
			totalChars += len(inputJSON)
		}
		for _, tr := range msg.ToolResults {
			totalChars += len(tr.Content) + 50 // Content + metadata
		}
	}

	// Convert chars to tokens (1.2 chars/token is conservative)
	estimatedTokens := int(float64(totalChars) / 1.2)

	// Add 10% buffer for special tokens and formatting
	return int(float64(estimatedTokens) * 1.1)
}
