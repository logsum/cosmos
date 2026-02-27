package policy

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAuditLogger_NewAndLog(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create logger
	sessionID := "test-session-123"
	logger, err := NewAuditLogger(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("NewAuditLogger failed: %v", err)
	}
	defer logger.Close()

	// Log an entry
	entry := AuditEntry{
		Agent:      "test-agent",
		Tool:       "test-tool",
		Permission: "fs:read:/tmp/**",
		Decision:   "allowed",
		Source:     "manifest",
		Arguments:  map[string]any{"path": "/tmp/test.txt"},
		ToolCallID: "call-123",
	}

	if err := logger.Log(entry); err != nil {
		t.Fatalf("Log failed: %v", err)
	}

	// Close to flush
	if err := logger.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Read back and verify
	entries, err := ReadAuditLog(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("ReadAuditLog failed: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.Agent != "test-agent" {
		t.Errorf("agent mismatch: got %s, want test-agent", e.Agent)
	}
	if e.Tool != "test-tool" {
		t.Errorf("tool mismatch: got %s, want test-tool", e.Tool)
	}
	if e.Decision != "allowed" {
		t.Errorf("decision mismatch: got %s, want allowed", e.Decision)
	}
	if e.SessionID != sessionID {
		t.Errorf("session_id mismatch: got %s, want %s", e.SessionID, sessionID)
	}
	if e.Timestamp == "" {
		t.Error("timestamp is empty")
	}
}

func TestAuditLogger_Redaction(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := "test-session-redact"
	logger, err := NewAuditLogger(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("NewAuditLogger failed: %v", err)
	}
	defer logger.Close()

	// Log entry with sensitive data
	entry := AuditEntry{
		Agent:      "test-agent",
		Tool:       "auth-tool",
		Permission: "net:http",
		Decision:   "allowed",
		Source:     "manifest",
		Arguments: map[string]any{
			"url":         "https://api.example.com",
			"api_token":   "secret-value-123",
			"password":    "p@ssw0rd",
			"secret_key":  "sk-abc123",
			"credential":  "cred-xyz",
			"auth_header": "Bearer token",
			"safe_value":  "public-data",
		},
		ToolCallID: "call-456",
	}

	if err := logger.Log(entry); err != nil {
		t.Fatalf("Log failed: %v", err)
	}
	logger.Close()

	// Read back and verify redaction
	entries, err := ReadAuditLog(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("ReadAuditLog failed: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	args := entries[0].Arguments
	sensitiveKeys := []string{"api_token", "password", "secret_key", "credential", "auth_header"}
	for _, key := range sensitiveKeys {
		val := args[key]
		if val != "[REDACTED]" {
			t.Errorf("%s not redacted: got %v, want [REDACTED]", key, val)
		}
	}

	// Verify safe value is not redacted
	if args["url"] != "https://api.example.com" {
		t.Errorf("url was incorrectly redacted: got %v", args["url"])
	}
	if args["safe_value"] != "public-data" {
		t.Errorf("safe_value was incorrectly redacted: got %v", args["safe_value"])
	}
}

func TestAuditLogger_ConcurrentWrites(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := "test-session-concurrent"
	logger, err := NewAuditLogger(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("NewAuditLogger failed: %v", err)
	}
	defer logger.Close()

	// Write concurrently from multiple goroutines
	const numWorkers = 10
	const entriesPerWorker = 20
	var wg sync.WaitGroup

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < entriesPerWorker; j++ {
				entry := AuditEntry{
					Agent:      "concurrent-agent",
					Tool:       "test-tool",
					Permission: "test",
					Decision:   "allowed",
					Source:     "manifest",
					Arguments:  map[string]any{"worker": workerID, "seq": j},
					ToolCallID: "call-concurrent",
				}
				if err := logger.Log(entry); err != nil {
					t.Errorf("Log failed: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()
	logger.Close()

	// Verify all entries were written
	entries, err := ReadAuditLog(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("ReadAuditLog failed: %v", err)
	}

	expectedCount := numWorkers * entriesPerWorker
	if len(entries) != expectedCount {
		t.Errorf("entry count mismatch: got %d, want %d", len(entries), expectedCount)
	}
}

func TestReadAuditLog_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := "nonexistent-session"

	// Read non-existent log
	entries, err := ReadAuditLog(sessionID, tmpDir)
	if err != nil {
		t.Fatalf("ReadAuditLog failed for non-existent log: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf("expected empty slice for non-existent log, got %d entries", len(entries))
	}
}

func TestReadAuditLog_Malformed(t *testing.T) {
	tmpDir := t.TempDir()
	sessionID := "malformed-session"

	// Write malformed JSON
	path := filepath.Join(tmpDir, "audit-"+sessionID+".jsonl")
	content := `{"valid": "json"}
{malformed json here
{"another": "valid"}`

	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Attempt to read
	_, err := ReadAuditLog(sessionID, tmpDir)
	if err == nil {
		t.Error("expected error for malformed JSON, got nil")
	}
}

func TestRedactSensitiveData(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected map[string]any
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: nil,
		},
		{
			name:     "empty map",
			input:    map[string]any{},
			expected: map[string]any{},
		},
		{
			name: "all safe values",
			input: map[string]any{
				"url":      "https://example.com",
				"method":   "GET",
				"timeout":  30,
				"retries":  3,
			},
			expected: map[string]any{
				"url":      "https://example.com",
				"method":   "GET",
				"timeout":  30,
				"retries":  3,
			},
		},
		{
			name: "sensitive keys",
			input: map[string]any{
				"api_token":     "secret-123",
				"password":      "pass123",
				"secret":        "my-secret",
				"credential":    "cred-abc",
				"auth_key":      "key-xyz",
				"client_secret": "client-sec",
			},
			expected: map[string]any{
				"api_token":     "[REDACTED]",
				"password":      "[REDACTED]",
				"secret":        "[REDACTED]",
				"credential":    "[REDACTED]",
				"auth_key":      "[REDACTED]",
				"client_secret": "[REDACTED]",
			},
		},
		{
			name: "case insensitive",
			input: map[string]any{
				"API_TOKEN": "secret-123",
				"Password":  "pass123",
				"SECRET":    "my-secret",
			},
			expected: map[string]any{
				"API_TOKEN": "[REDACTED]",
				"Password":  "[REDACTED]",
				"SECRET":    "[REDACTED]",
			},
		},
		{
			name: "mixed safe and sensitive",
			input: map[string]any{
				"url":       "https://example.com",
				"api_token": "secret-123",
				"method":    "POST",
				"password":  "pass123",
			},
			expected: map[string]any{
				"url":       "https://example.com",
				"api_token": "[REDACTED]",
				"method":    "POST",
				"password":  "[REDACTED]",
			},
		},
		{
			name: "nested sensitive data in map",
			input: map[string]any{
				"url": "https://example.com",
				"config": map[string]any{
					"timeout": 30,
					"auth": map[string]any{
						"username": "admin",
						"password": "secret123",
						"api_key":  "key-xyz",
					},
				},
			},
			expected: map[string]any{
				"url": "https://example.com",
				"config": map[string]any{
					"timeout": 30,
					"auth": map[string]any{
						"username": "admin",
						"password": "[REDACTED]",
						"api_key":  "[REDACTED]",
					},
				},
			},
		},
		{
			name: "nested sensitive data in slice",
			input: map[string]any{
				"servers": []any{
					map[string]any{
						"host":     "server1.example.com",
						"password": "pass1",
					},
					map[string]any{
						"host":     "server2.example.com",
						"api_key":  "key-abc",
					},
				},
			},
			expected: map[string]any{
				"servers": []any{
					map[string]any{
						"host":     "server1.example.com",
						"password": "[REDACTED]",
					},
					map[string]any{
						"host":     "server2.example.com",
						"api_key":  "[REDACTED]",
					},
				},
			},
		},
		{
			name: "deeply nested structure",
			input: map[string]any{
				"level1": map[string]any{
					"safe": "value",
					"level2": map[string]any{
						"level3": map[string]any{
							"secret_token": "should-be-redacted",
							"public":       "visible",
						},
					},
				},
			},
			expected: map[string]any{
				"level1": map[string]any{
					"safe": "value",
					"level2": map[string]any{
						"level3": map[string]any{
							"secret_token": "[REDACTED]",
							"public":       "visible",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := redactSensitiveData(tt.input)

			if tt.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}

			if !deepEqual(result, tt.expected) {
				t.Errorf("result mismatch:\ngot:  %#v\nwant: %#v", result, tt.expected)
			}
		})
	}
}

// deepEqual recursively compares two values for equality.
func deepEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Compare maps
	aMap, aIsMap := a.(map[string]any)
	bMap, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		if len(aMap) != len(bMap) {
			return false
		}
		for k, av := range aMap {
			bv, ok := bMap[k]
			if !ok {
				return false
			}
			if !deepEqual(av, bv) {
				return false
			}
		}
		return true
	}

	// Compare slices
	aSlice, aIsSlice := a.([]any)
	bSlice, bIsSlice := b.([]any)
	if aIsSlice && bIsSlice {
		if len(aSlice) != len(bSlice) {
			return false
		}
		for i := range aSlice {
			if !deepEqual(aSlice[i], bSlice[i]) {
				return false
			}
		}
		return true
	}

	// Primitives: direct comparison
	return a == b
}
