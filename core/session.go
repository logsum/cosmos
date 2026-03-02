package core

import (
	"cosmos/core/provider"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SavedSession is the full serialized conversation saved to disk.
type SavedSession struct {
	Version     int                `json:"version"`     // always 1
	SessionID   string             `json:"sessionId"`
	Model       string             `json:"model"`
	WorkDir     string             `json:"workDir"`
	CreatedAt   time.Time          `json:"createdAt"`
	SavedAt     time.Time          `json:"savedAt"`
	Description string             `json:"description"` // first user msg, ≤100 chars
	History     []provider.Message `json:"history"`
	Usage       SavedUsage         `json:"usage"`
}

// SavedUsage holds token/cost totals for a saved session.
type SavedUsage struct {
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	TotalCostUSD float64 `json:"totalCostUSD"`
}

// SessionInfo is a lightweight summary of a saved session (history not loaded).
type SessionInfo struct {
	Filename     string    `json:"filename"`
	Description  string    `json:"description"`
	Model        string    `json:"model"`
	SavedAt      time.Time `json:"savedAt"`
	MessageCount int       `json:"messageCount"`
}

// SaveSession persists a session to sessionsDir. Returns nil (no-op) if history is empty.
// Uses atomic rename to prevent partial writes.
func SaveSession(s *Session, tracker *Tracker, sessionsDir, workDir string) error {
	s.mu.Lock()
	history := append([]provider.Message{}, s.history...)
	model := s.model
	sessionID := s.id
	createdAt := s.createdAt
	s.mu.Unlock()

	if len(history) == 0 {
		return nil // Nothing to save
	}

	// Build description from first user message (≤100 runes)
	desc := ""
	for _, msg := range history {
		if msg.Role == provider.RoleUser && msg.Content != "" {
			desc = msg.Content
			if runes := []rune(desc); len(runes) > 100 {
				desc = string(runes[:97]) + "..."
			}
			break
		}
	}

	var usage SavedUsage
	if tracker != nil {
		snap := tracker.Snapshot()
		usage = SavedUsage{
			InputTokens:  snap.TotalInputTokens,
			OutputTokens: snap.TotalOutputTokens,
			TotalCostUSD: snap.TotalCost,
		}
	}

	saved := SavedSession{
		Version:     1,
		SessionID:   sessionID,
		Model:       model,
		WorkDir:     workDir,
		CreatedAt:   createdAt,
		SavedAt:     time.Now().UTC(),
		Description: desc,
		History:     history,
		Usage:       usage,
	}

	// Filename: <base(workDir)>-<timestamp>.json
	base := filepath.Base(workDir)
	if base == "" || base == "." {
		base = "cosmos"
	}
	timestamp := saved.SavedAt.Format("20060102T150405Z")
	filename := base + "-" + timestamp + ".json"

	if err := os.MkdirAll(sessionsDir, 0700); err != nil {
		return fmt.Errorf("creating sessions dir: %w", err)
	}

	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling session: %w", err)
	}

	// Atomic write: write to .tmp then rename
	tmpPath := filepath.Join(sessionsDir, filename+".tmp")
	finalPath := filepath.Join(sessionsDir, filename)

	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("writing session file: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming session file: %w", err)
	}

	return nil
}

// LoadSavedSession reads and parses a session file from sessionsDir.
// The filename must resolve to a path within sessionsDir (path traversal is rejected).
func LoadSavedSession(sessionsDir, filename string) (SavedSession, error) {
	path := filepath.Join(sessionsDir, filename)

	// Guard against path traversal (e.g. "../../etc/passwd")
	absPath, err := filepath.Abs(path)
	if err != nil {
		return SavedSession{}, fmt.Errorf("resolving session path: %w", err)
	}
	absDir, err := filepath.Abs(sessionsDir)
	if err != nil {
		return SavedSession{}, fmt.Errorf("resolving sessions dir: %w", err)
	}
	if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
		return SavedSession{}, fmt.Errorf("invalid session filename: path escapes sessions directory")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return SavedSession{}, fmt.Errorf("reading session file: %w", err)
	}

	var sess SavedSession
	if err := json.Unmarshal(data, &sess); err != nil {
		return SavedSession{}, fmt.Errorf("parsing session file: %w", err)
	}

	return sess, nil
}

// ListSavedSessions returns lightweight summaries of all saved sessions,
// sorted by save time (newest first). Returns nil if the directory does not exist.
func ListSavedSessions(sessionsDir string) ([]SessionInfo, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing sessions dir: %w", err)
	}

	var sessions []SessionInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}

		path := filepath.Join(sessionsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue // Skip unreadable files
		}

		var sess SavedSession
		if err := json.Unmarshal(data, &sess); err != nil {
			continue // Skip malformed files
		}

		sessions = append(sessions, SessionInfo{
			Filename:     name,
			Description:  sess.Description,
			Model:        sess.Model,
			SavedAt:      sess.SavedAt,
			MessageCount: len(sess.History),
		})
	}

	// Newest first
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].SavedAt.After(sessions[j].SavedAt)
	})

	return sessions, nil
}
