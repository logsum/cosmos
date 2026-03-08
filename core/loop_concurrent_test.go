package core

import (
	"context"
	"cosmos/core/provider"
	"cosmos/engine/manifest"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mustParsePermissionKey is a test helper that parses a permission key and panics on error.
func mustParsePermissionKey(raw string) manifest.PermissionKey {
	key, err := manifest.ParsePermissionKey(raw)
	if err != nil {
		panic("test setup error: invalid permission key: " + raw + ": " + err.Error())
	}
	return key
}

// mockManifestExecutor implements both ToolExecutor and ToolManifestProvider for testing.
type mockManifestExecutor struct {
	manifests map[string]manifestEntry
	execFunc  func(ctx context.Context, name string, input map[string]any) (string, error)
}

type manifestEntry struct {
	agentName string
	rules     []manifest.PermissionRule
}

func (m *mockManifestExecutor) Execute(ctx context.Context, name string, input map[string]any) (string, error) {
	if m.execFunc != nil {
		return m.execFunc(ctx, name, input)
	}
	return "mock result", nil
}

func (m *mockManifestExecutor) ToolPermissionRules(name string) (string, []manifest.PermissionRule, bool) {
	entry, ok := m.manifests[name]
	if !ok {
		return "", nil, false
	}
	return entry.agentName, entry.rules, true
}

func TestIsWriteTool(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		rules    []manifest.PermissionRule
		want     bool
	}{
		{
			name:     "no rules - pure function",
			toolName: "pure_calc",
			rules:    []manifest.PermissionRule{},
			want:     false,
		},
		{
			name:     "read-only fs permission",
			toolName: "read_file",
			rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("fs:read:/tmp/*"), Mode: manifest.PermissionAllow},
			},
			want: false,
		},
		{
			name:     "write fs permission",
			toolName: "write_file",
			rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("fs:write:/tmp/*"), Mode: manifest.PermissionAllow},
			},
			want: true,
		},
		{
			name:     "generic fs write",
			toolName: "write_any",
			rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("fs:write"), Mode: manifest.PermissionRequestOnce},
			},
			want: true,
		},
		{
			name:     "docker permission",
			toolName: "docker_build",
			rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("docker:build"), Mode: manifest.PermissionAllow},
			},
			want: true,
		},
		{
			name:     "fs write denied doesn't count",
			toolName: "denied_write",
			rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("fs:write"), Mode: manifest.PermissionDeny},
			},
			want: false,
		},
		{
			name:     "http only - read-only",
			toolName: "fetch_url",
			rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("net:http"), Mode: manifest.PermissionAllow},
			},
			want: false,
		},
		{
			name:     "mixed read and write - is write",
			toolName: "read_write",
			rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("fs:read"), Mode: manifest.PermissionAllow},
				{Key: mustParsePermissionKey("fs:write"), Mode: manifest.PermissionRequestOnce},
			},
			want: true,
		},
		{
			name:     "fs:unlink is write",
			toolName: "unlink_file",
			rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("fs:unlink"), Mode: manifest.PermissionAllow},
			},
			want: true,
		},
		{
			name:     "fs:unlink denied doesn't count",
			toolName: "denied_unlink",
			rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("fs:unlink"), Mode: manifest.PermissionDeny},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &mockManifestExecutor{
				manifests: map[string]manifestEntry{
					tt.toolName: {agentName: "test-agent", rules: tt.rules},
				},
			}
			session := &Session{executor: executor}
			got := session.isWriteTool(tt.toolName)
			if got != tt.want {
				t.Errorf("isWriteTool() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBatchToolsByPermissions(t *testing.T) {
	tests := []struct {
		name          string
		toolCalls     []provider.ToolCall
		toolTypes     map[string]bool // tool name -> isWrite
		expectedCount int
		expectedTypes []bool // expected isWrite for each batch
	}{
		{
			name:          "empty",
			toolCalls:     []provider.ToolCall{},
			toolTypes:     map[string]bool{},
			expectedCount: 0,
			expectedTypes: []bool{},
		},
		{
			name: "single read",
			toolCalls: []provider.ToolCall{
				{ID: "1", Name: "read1"},
			},
			toolTypes:     map[string]bool{"read1": false},
			expectedCount: 1,
			expectedTypes: []bool{false},
		},
		{
			name: "single write",
			toolCalls: []provider.ToolCall{
				{ID: "1", Name: "write1"},
			},
			toolTypes:     map[string]bool{"write1": true},
			expectedCount: 1,
			expectedTypes: []bool{true},
		},
		{
			name: "all reads - single batch",
			toolCalls: []provider.ToolCall{
				{ID: "1", Name: "read1"},
				{ID: "2", Name: "read2"},
				{ID: "3", Name: "read3"},
			},
			toolTypes:     map[string]bool{"read1": false, "read2": false, "read3": false},
			expectedCount: 1,
			expectedTypes: []bool{false},
		},
		{
			name: "all writes - single sequential batch",
			toolCalls: []provider.ToolCall{
				{ID: "1", Name: "write1"},
				{ID: "2", Name: "write2"},
			},
			toolTypes:     map[string]bool{"write1": true, "write2": true},
			expectedCount: 1,
			expectedTypes: []bool{true},
		},
		{
			name: "mixed R,R,W,R,R",
			toolCalls: []provider.ToolCall{
				{ID: "1", Name: "read1"},
				{ID: "2", Name: "read2"},
				{ID: "3", Name: "write1"},
				{ID: "4", Name: "read3"},
				{ID: "5", Name: "read4"},
			},
			toolTypes: map[string]bool{
				"read1": false, "read2": false, "read3": false, "read4": false,
				"write1": true,
			},
			expectedCount: 3,
			expectedTypes: []bool{false, true, false},
		},
		{
			name: "alternating R,W,R,W",
			toolCalls: []provider.ToolCall{
				{ID: "1", Name: "read1"},
				{ID: "2", Name: "write1"},
				{ID: "3", Name: "read2"},
				{ID: "4", Name: "write2"},
			},
			toolTypes: map[string]bool{
				"read1": false, "read2": false,
				"write1": true, "write2": true,
			},
			expectedCount: 4,
			expectedTypes: []bool{false, true, false, true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifests := make(map[string]manifestEntry)
			for toolName, isWrite := range tt.toolTypes {
				var rules []manifest.PermissionRule
				if isWrite {
					rules = []manifest.PermissionRule{
						{Key: mustParsePermissionKey("fs:write"), Mode: manifest.PermissionAllow},
					}
				}
				manifests[toolName] = manifestEntry{agentName: "test-agent", rules: rules}
			}

			executor := &mockManifestExecutor{manifests: manifests}
			session := &Session{executor: executor}

			batches := session.batchToolsByPermissions(tt.toolCalls)

			if len(batches) != tt.expectedCount {
				t.Errorf("got %d batches, want %d", len(batches), tt.expectedCount)
			}

			for i, batch := range batches {
				if i >= len(tt.expectedTypes) {
					break
				}
				if batch.isWrite != tt.expectedTypes[i] {
					t.Errorf("batch[%d].isWrite = %v, want %v", i, batch.isWrite, tt.expectedTypes[i])
				}
			}

			// Verify all tool calls are included
			totalTools := 0
			for _, batch := range batches {
				totalTools += len(batch.toolCalls)
			}
			if totalTools != len(tt.toolCalls) {
				t.Errorf("total tools in batches = %d, want %d", totalTools, len(tt.toolCalls))
			}
		})
	}
}

func TestExecuteBatchConcurrent(t *testing.T) {
	var peakConcurrent int32
	var mu sync.Mutex
	var concurrentCalls int32

	executor := &mockManifestExecutor{
		manifests: map[string]manifestEntry{
			"read1": {agentName: "agent1"},
			"read2": {agentName: "agent1"},
			"read3": {agentName: "agent1"},
		},
		execFunc: func(ctx context.Context, name string, input map[string]any) (string, error) {
			current := atomic.AddInt32(&concurrentCalls, 1)
			defer atomic.AddInt32(&concurrentCalls, -1)

			mu.Lock()
			if current > peakConcurrent {
				peakConcurrent = current
			}
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)
			return "result:" + name, nil
		},
	}

	session := &Session{
		executor: executor,
		notifier: &mockNotifier{},
	}

	// Pre-create executions (simulating preflight)
	execs := []toolExecution{
		{toolCallID: "tc1", toolCall: provider.ToolCall{ID: "tc1", Name: "read1"}},
		{toolCallID: "tc2", toolCall: provider.ToolCall{ID: "tc2", Name: "read2"}},
		{toolCallID: "tc3", toolCall: provider.ToolCall{ID: "tc3", Name: "read3"}},
	}

	session.executeBatchConcurrent(context.Background(), execs, "interaction-1")

	// Verify results
	for i, exec := range execs {
		expectedResult := "result:" + exec.toolCall.Name
		if exec.result.Content != expectedResult {
			t.Errorf("execs[%d].result.Content = %s, want %s", i, exec.result.Content, expectedResult)
		}
	}

	// Verify concurrent execution occurred
	if peakConcurrent < 2 {
		t.Errorf("peak concurrent calls = %d, expected at least 2 for concurrent execution", peakConcurrent)
	}
}

func TestExecuteBatchSequential(t *testing.T) {
	var executionOrder []string
	var mu sync.Mutex
	var concurrentCalls int32

	executor := &mockManifestExecutor{
		manifests: map[string]manifestEntry{
			"write1": {agentName: "agent1", rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("fs:write"), Mode: manifest.PermissionAllow},
			}},
			"write2": {agentName: "agent1", rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("fs:write"), Mode: manifest.PermissionAllow},
			}},
		},
		execFunc: func(ctx context.Context, name string, input map[string]any) (string, error) {
			mu.Lock()
			executionOrder = append(executionOrder, name)
			mu.Unlock()

			current := atomic.AddInt32(&concurrentCalls, 1)
			defer atomic.AddInt32(&concurrentCalls, -1)

			time.Sleep(20 * time.Millisecond)

			if current > 1 {
				t.Errorf("concurrent execution detected: %d concurrent calls", current)
			}

			return "result:" + name, nil
		},
	}

	session := &Session{
		executor: executor,
		notifier: &mockNotifier{},
	}

	execs := []toolExecution{
		{toolCallID: "tc1", toolCall: provider.ToolCall{ID: "tc1", Name: "write1"}},
		{toolCallID: "tc2", toolCall: provider.ToolCall{ID: "tc2", Name: "write2"}},
	}

	session.executeBatchSequential(context.Background(), execs, "interaction-1")

	// Verify sequential order preserved
	if len(executionOrder) != 2 || executionOrder[0] != "write1" || executionOrder[1] != "write2" {
		t.Errorf("execution order = %v, want [write1 write2]", executionOrder)
	}
}

func TestExecuteBatchConcurrent_SkipsDenied(t *testing.T) {
	executor := &mockManifestExecutor{
		manifests: map[string]manifestEntry{
			"read1": {agentName: "agent1"},
			"read2": {agentName: "agent1"},
		},
		execFunc: func(ctx context.Context, name string, input map[string]any) (string, error) {
			return "executed:" + name, nil
		},
	}

	session := &Session{
		executor: executor,
		notifier: &mockNotifier{},
	}

	// Simulate one tool denied at preflight, one approved
	execs := []toolExecution{
		{
			toolCallID: "tc1",
			toolCall:   provider.ToolCall{ID: "tc1", Name: "read1"},
			result:     provider.ToolResult{ToolUseID: "tc1", Content: "Permission denied: test", IsError: true},
		},
		{
			toolCallID: "tc2",
			toolCall:   provider.ToolCall{ID: "tc2", Name: "read2"},
		},
	}

	session.executeBatchConcurrent(context.Background(), execs, "int-1")

	// Denied tool should keep its original result
	if execs[0].result.Content != "Permission denied: test" {
		t.Errorf("denied tool result changed: %q", execs[0].result.Content)
	}
	// Approved tool should be executed
	if execs[1].result.Content != "executed:read2" {
		t.Errorf("approved tool result = %q, want 'executed:read2'", execs[1].result.Content)
	}
}

func TestConcurrentToolExecution_Integration(t *testing.T) {
	var executionLog []string
	var logMu sync.Mutex
	var peakConcurrent int32
	var currentConcurrent int32

	executor := &mockManifestExecutor{
		manifests: map[string]manifestEntry{
			"read1":  {agentName: "reader"},
			"read2":  {agentName: "reader"},
			"write1": {agentName: "writer", rules: []manifest.PermissionRule{
				{Key: mustParsePermissionKey("fs:write"), Mode: manifest.PermissionAllow},
			}},
			"read3": {agentName: "reader"},
			"read4": {agentName: "reader"},
		},
		execFunc: func(ctx context.Context, name string, input map[string]any) (string, error) {
			current := atomic.AddInt32(&currentConcurrent, 1)
			defer atomic.AddInt32(&currentConcurrent, -1)

			for {
				peak := atomic.LoadInt32(&peakConcurrent)
				if current <= peak || atomic.CompareAndSwapInt32(&peakConcurrent, peak, current) {
					break
				}
			}

			logMu.Lock()
			executionLog = append(executionLog, name)
			logMu.Unlock()

			time.Sleep(15 * time.Millisecond)
			return "ok:" + name, nil
		},
	}

	notifier := &mockNotifier{}
	session := &Session{
		executor: executor,
		notifier: notifier,
	}

	session.SetFileChangesFunc(func(toolCallID string) []FileChange { return nil })

	toolCalls := []provider.ToolCall{
		{ID: "1", Name: "read1"},
		{ID: "2", Name: "read2"},
		{ID: "3", Name: "write1"},
		{ID: "4", Name: "read3"},
		{ID: "5", Name: "read4"},
	}

	// Should produce 3 batches: [R,R], [W], [R,R]
	batches := session.batchToolsByPermissions(toolCalls)
	if len(batches) != 3 {
		t.Fatalf("got %d batches, want 3", len(batches))
	}

	// Phase 1: Preflight (simulate — no permission checks in this test)
	allExecs := make([]toolExecution, len(toolCalls))
	for i, tc := range toolCalls {
		allExecs[i] = toolExecution{toolCallID: tc.ID, toolCall: tc}
	}

	// Phase 2: Execute batches
	offset := 0
	for _, batch := range batches {
		batchExecs := allExecs[offset : offset+len(batch.toolCalls)]
		if batch.isWrite {
			session.executeBatchSequential(context.Background(), batchExecs, "int-1")
		} else {
			session.executeBatchConcurrent(context.Background(), batchExecs, "int-1")
		}
		offset += len(batch.toolCalls)
	}

	// Verify result count and order
	expectedOrder := []string{"read1", "read2", "write1", "read3", "read4"}
	for i, exec := range allExecs {
		if exec.toolCall.Name != expectedOrder[i] {
			t.Errorf("exec[%d].toolCall.Name = %s, want %s", i, exec.toolCall.Name, expectedOrder[i])
		}
		if exec.result.Content != "ok:"+expectedOrder[i] {
			t.Errorf("exec[%d].result.Content = %s, want ok:%s", i, exec.result.Content, expectedOrder[i])
		}
	}

	peak := atomic.LoadInt32(&peakConcurrent)
	if peak < 2 {
		t.Errorf("peak concurrent calls = %d, expected at least 2", peak)
	}

	t.Logf("Execution log: %v", executionLog)
}

func TestFileChangeTracking_Concurrent(t *testing.T) {
	var fileChangesMu sync.Mutex
	fileChangesByTool := make(map[string][]FileChange)

	executor := &mockManifestExecutor{
		manifests: map[string]manifestEntry{
			"tool1": {agentName: "agent1"},
			"tool2": {agentName: "agent1"},
			"tool3": {agentName: "agent1"},
		},
		execFunc: func(ctx context.Context, name string, input map[string]any) (string, error) {
			time.Sleep(10 * time.Millisecond)
			return "ok", nil
		},
	}

	session := &Session{
		executor: executor,
		notifier: &mockNotifier{},
	}

	session.SetFileChangesFunc(func(toolCallID string) []FileChange {
		fileChangesMu.Lock()
		defer fileChangesMu.Unlock()
		changes := fileChangesByTool[toolCallID]
		delete(fileChangesByTool, toolCallID)
		return changes
	})

	// Pre-populate file changes
	simulateSnapshot := func(toolCallID, path string) {
		fileChangesMu.Lock()
		defer fileChangesMu.Unlock()
		fileChangesByTool[toolCallID] = append(fileChangesByTool[toolCallID], FileChange{
			Path:      path,
			Operation: "write",
			WasNew:    false,
		})
	}

	simulateSnapshot("tc1", "/file1.txt")
	simulateSnapshot("tc2", "/file2.txt")
	simulateSnapshot("tc3", "/file3.txt")

	execs := []toolExecution{
		{toolCallID: "tc1", toolCall: provider.ToolCall{ID: "tc1", Name: "tool1"}},
		{toolCallID: "tc2", toolCall: provider.ToolCall{ID: "tc2", Name: "tool2"}},
		{toolCallID: "tc3", toolCall: provider.ToolCall{ID: "tc3", Name: "tool3"}},
	}

	session.executeBatchConcurrent(context.Background(), execs, "int-1")

	// Verify file changes are correctly attributed
	for i, exec := range execs {
		expectedPath := fmt.Sprintf("/file%d.txt", i+1)
		if len(exec.fileChanges) != 1 {
			t.Errorf("exec[%d] has %d file changes, want 1", i, len(exec.fileChanges))
			continue
		}
		if exec.fileChanges[0].Path != expectedPath {
			t.Errorf("exec[%d].fileChanges[0].Path = %s, want %s", i, exec.fileChanges[0].Path, expectedPath)
		}
	}

	// Verify map is empty (all consumed)
	if len(fileChangesByTool) != 0 {
		t.Errorf("fileChangesByTool has %d entries remaining, want 0", len(fileChangesByTool))
	}
}

func TestConcurrencyLimiter(t *testing.T) {
	var peakConcurrent int32
	var currentConcurrent int32

	executor := &mockManifestExecutor{
		manifests: map[string]manifestEntry{},
		execFunc: func(ctx context.Context, name string, input map[string]any) (string, error) {
			current := atomic.AddInt32(&currentConcurrent, 1)
			defer atomic.AddInt32(&currentConcurrent, -1)

			for {
				peak := atomic.LoadInt32(&peakConcurrent)
				if current <= peak || atomic.CompareAndSwapInt32(&peakConcurrent, peak, current) {
					break
				}
			}

			time.Sleep(20 * time.Millisecond)
			return "ok", nil
		},
	}

	// Register 20 tools
	for i := range 20 {
		name := fmt.Sprintf("tool%d", i)
		executor.manifests[name] = manifestEntry{agentName: "agent1"}
	}

	session := &Session{
		executor: executor,
		notifier: &mockNotifier{},
	}

	// Create 20 executions
	execs := make([]toolExecution, 20)
	for i := range 20 {
		name := fmt.Sprintf("tool%d", i)
		execs[i] = toolExecution{
			toolCallID: name,
			toolCall:   provider.ToolCall{ID: name, Name: name},
		}
	}

	session.executeBatchConcurrent(context.Background(), execs, "int-1")

	peak := atomic.LoadInt32(&peakConcurrent)
	if peak > int32(maxConcurrentTools) {
		t.Errorf("peak concurrent calls = %d, exceeds limit of %d", peak, maxConcurrentTools)
	}
	if peak < 2 {
		t.Errorf("peak concurrent calls = %d, expected at least 2", peak)
	}
	t.Logf("Peak concurrent calls: %d (limit: %d)", peak, maxConcurrentTools)
}

func TestExecuteBatchConcurrent_ContextCancellation(t *testing.T) {
	started := make(chan struct{})

	executor := &mockManifestExecutor{
		manifests: map[string]manifestEntry{
			"slow":  {agentName: "agent1"},
			"fast1": {agentName: "agent1"},
			"fast2": {agentName: "agent1"},
		},
		execFunc: func(ctx context.Context, name string, input map[string]any) (string, error) {
			if name == "slow" {
				close(started) // Signal that first tool is running
				// Block until context is cancelled
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "ok:" + name, nil
		},
	}

	session := &Session{
		executor: executor,
		notifier: &mockNotifier{},
	}

	ctx, cancel := context.WithCancel(context.Background())

	execs := []toolExecution{
		{toolCallID: "tc1", toolCall: provider.ToolCall{ID: "tc1", Name: "slow"}},
		{toolCallID: "tc2", toolCall: provider.ToolCall{ID: "tc2", Name: "fast1"}},
		{toolCallID: "tc3", toolCall: provider.ToolCall{ID: "tc3", Name: "fast2"}},
	}

	done := make(chan struct{})
	go func() {
		session.executeBatchConcurrent(ctx, execs, "int-1")
		close(done)
	}()

	// Wait for the slow tool to start, then cancel context
	<-started
	cancel()
	<-done

	// The slow tool should have a cancellation error
	if !execs[0].result.IsError {
		t.Errorf("slow tool should have error result, got: %q", execs[0].result.Content)
	}

	// All tools should have results (no empty ToolUseID)
	for i, exec := range execs {
		if exec.result.ToolUseID == "" && exec.result.Content == "" {
			t.Errorf("execs[%d] has no result", i)
		}
	}
}
