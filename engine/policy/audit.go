package policy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AuditEntry is a single audit log record (JSON-lines format).
type AuditEntry struct {
	Timestamp   string                 `json:"timestamp"`    // RFC3339
	SessionID   string                 `json:"session_id"`
	Agent       string                 `json:"agent"`
	Tool        string                 `json:"tool"`
	Permission  string                 `json:"permission"`
	Decision    string                 `json:"decision"`      // "allowed", "denied", "user_approved", "user_denied"
	Source      string                 `json:"source"`        // "manifest", "policy_override", "persisted_grant", "default_deny"
	Arguments   map[string]any `json:"arguments"`     // Redacted for sensitive data
	ToolCallID  string                 `json:"tool_call_id"`
	Error       string                 `json:"error,omitempty"`
}

// AuditLogger appends audit entries to a session-specific JSON-lines file.
type AuditLogger struct {
	mu        sync.Mutex
	file      *os.File
	path      string
	sessionID string
}

// NewAuditLogger creates an audit logger for the given session.
// Path should be like ".cosmos/audit-<session-id>.jsonl".
func NewAuditLogger(sessionID, cosmosDir string) (*AuditLogger, error) {
	// Ensure .cosmos directory exists
	if err := os.MkdirAll(cosmosDir, 0o700); err != nil {
		return nil, fmt.Errorf("create cosmos directory: %w", err)
	}

	path := filepath.Join(cosmosDir, fmt.Sprintf("audit-%s.jsonl", sessionID))

	// Open in append mode (O_CREATE | O_WRONLY | O_APPEND)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}

	return &AuditLogger{
		file:      file,
		path:      path,
		sessionID: sessionID,
	}, nil
}

// Log writes an audit entry to the log file.
func (a *AuditLogger) Log(entry AuditEntry) error {
	// Set session ID and timestamp
	entry.SessionID = a.sessionID
	entry.Timestamp = time.Now().UTC().Format(time.RFC3339)

	// Redact sensitive data
	entry.Arguments = redactSensitiveData(entry.Arguments)

	// Marshal to JSON
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal audit entry: %w", err)
	}
	data = append(data, '\n')

	// Write atomically (mutex-protected)
	a.mu.Lock()
	defer a.mu.Unlock()

	// Defensive: check if file was closed during shutdown race
	if a.file == nil {
		return fmt.Errorf("audit logger closed")
	}

	if _, err := a.file.Write(data); err != nil {
		return fmt.Errorf("write audit entry: %w", err)
	}

	return nil
}

// Close flushes and closes the audit log file.
func (a *AuditLogger) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.file == nil {
		return nil
	}

	if err := a.file.Sync(); err != nil {
		return fmt.Errorf("sync audit log: %w", err)
	}
	if err := a.file.Close(); err != nil {
		return fmt.Errorf("close audit log: %w", err)
	}
	a.file = nil
	return nil
}

// sensitivePatterns is the list of substrings that indicate a sensitive key.
var sensitivePatterns = []string{"token", "key", "password", "secret", "credential", "auth"}

// redactSensitiveData removes or masks sensitive values from arguments (recursively).
func redactSensitiveData(args map[string]any) map[string]any {
	if args == nil {
		return nil
	}

	redacted := make(map[string]any)
	for k, v := range args {
		redacted[k] = redactSensitiveRecursive(k, v)
	}
	return redacted
}

// redactSensitiveRecursive recursively walks through values and redacts sensitive data.
// It handles maps, slices, and checks keys for sensitive patterns at all nesting levels.
// Sensitive keys are only redacted if their values are primitives; nested structures are recursed.
func redactSensitiveRecursive(key string, value any) any {
	// Recursively process maps first (always recurse into structures)
	if m, ok := value.(map[string]any); ok {
		redacted := make(map[string]any)
		for k, v := range m {
			redacted[k] = redactSensitiveRecursive(k, v)
		}
		return redacted
	}

	// Recursively process slices
	if s, ok := value.([]any); ok {
		redacted := make([]any, len(s))
		for i, v := range s {
			// For slice elements, use empty key (no key-based redaction)
			redacted[i] = redactSensitiveRecursive("", v)
		}
		return redacted
	}

	// For primitives: check if key is sensitive and redact
	lowerKey := strings.ToLower(key)
	for _, pattern := range sensitivePatterns {
		if strings.Contains(lowerKey, pattern) {
			return "[REDACTED]"
		}
	}

	// For string values: check if content contains sensitive patterns
	if str, ok := value.(string); ok {
		lowerVal := strings.ToLower(str)
		for _, pattern := range sensitivePatterns {
			if strings.Contains(lowerVal, pattern) {
				return "[REDACTED]"
			}
		}
	}

	// Safe primitive value
	return value
}

// ReadAuditLog reads all entries from a session's audit log.
func ReadAuditLog(sessionID, cosmosDir string) ([]AuditEntry, error) {
	path := filepath.Join(cosmosDir, fmt.Sprintf("audit-%s.jsonl", sessionID))

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []AuditEntry{}, nil // Empty log for new sessions
		}
		return nil, fmt.Errorf("read audit log: %w", err)
	}

	var entries []AuditEntry
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("parse audit entry line %d: %w", i+1, err)
		}
		entries = append(entries, entry)
	}

	return entries, nil
}
