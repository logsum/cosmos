// Package vfs provides file snapshotting for destructive operations.
// Before any write or delete performed by a V8 tool, the original file
// content is captured in a content-addressed blob store. Snapshots are
// grouped by interaction (one LLM turn) so they can be restored together
// via the Changelog UI.
package vfs

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// maxSnapshotSize is the maximum file size (50 MB) that will be snapshotted.
// Files larger than this are tracked in the manifest but no blob is stored.
const maxSnapshotSize = 50 * 1024 * 1024

// SnapshotRecord is the metadata for one file snapshot.
type SnapshotRecord struct {
	Path          string      `json:"path"`
	Operation     string      `json:"operation"`       // "write" | "delete"
	ContentHash   string      `json:"content_hash"`    // SHA-256 hex (empty if WasNewFile)
	AgentName     string      `json:"agent_name"`
	InteractionID string      `json:"interaction_id"`
	ToolCallID    string      `json:"tool_call_id"`
	Timestamp     time.Time   `json:"timestamp"`
	WasNewFile    bool        `json:"was_new_file"`
	FileMode      os.FileMode `json:"file_mode"`       // original permissions (0 if WasNewFile)
	TooLarge      bool        `json:"too_large"`        // true if file exceeded size limit
}

// Snapshotter is a per-session snapshot manager. It stores file content
// before destructive operations so they can be rolled back.
type Snapshotter struct {
	mu            sync.Mutex
	sessionDir    string           // .cosmos/snapshots/<session-id>/
	records       []SnapshotRecord
	interactionID string // current LLM turn (set externally)
	toolCallID    string // current tool call (set externally)
}

// NewSnapshotter creates a snapshot manager for the given session.
// It creates the session directory if needed and loads any existing
// manifest from a previous run (for session resume).
func NewSnapshotter(cosmosDir, sessionID string) (*Snapshotter, error) {
	sessionDir := filepath.Join(cosmosDir, "snapshots", sessionID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}

	s := &Snapshotter{
		sessionDir: sessionDir,
	}

	// Load existing manifest if resuming a session.
	manifestPath := filepath.Join(sessionDir, "manifest.jsonl")
	if f, err := os.Open(manifestPath); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			var rec SnapshotRecord
			if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
				continue // skip malformed lines
			}
			s.records = append(s.records, rec)
		}
	}

	return s, nil
}

// SetSnapshotContext sets the current interaction and tool call IDs.
// Called by the core loop before each tool execution.
func (s *Snapshotter) SetSnapshotContext(interactionID, toolCallID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interactionID = interactionID
	s.toolCallID = toolCallID
}

// Snapshot captures the current content of a file before a destructive
// operation. The content is stored as a content-addressed blob. If the
// file does not exist (new file creation), WasNewFile is set and no
// blob is stored. Returns the snapshot record.
func (s *Snapshotter) Snapshot(path, operation, agentName string) (*SnapshotRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec := SnapshotRecord{
		Path:          path,
		Operation:     operation,
		AgentName:     agentName,
		InteractionID: s.interactionID,
		ToolCallID:    s.toolCallID,
		Timestamp:     time.Now().UTC(),
	}

	// Check file existence and metadata.
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			rec.WasNewFile = true
		} else {
			return nil, fmt.Errorf("stat file for snapshot: %w", err)
		}
	}

	// Read existing file content (if any).
	if !rec.WasNewFile {
		rec.FileMode = info.Mode().Perm()

		// Skip blob storage for files exceeding 50 MB.
		if info.Size() > maxSnapshotSize {
			rec.TooLarge = true
			fmt.Fprintf(os.Stderr, "cosmos: snapshot: %s too large (%d bytes), skipping blob\n", path, info.Size())
		}
	}

	var data []byte
	if !rec.WasNewFile && !rec.TooLarge {
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read file for snapshot: %w", err)
		}
	}

	// Store blob if file existed and not too large.
	if !rec.WasNewFile && !rec.TooLarge {
		hash := sha256.Sum256(data)
		rec.ContentHash = hex.EncodeToString(hash[:])

		blobPath := filepath.Join(s.sessionDir, rec.ContentHash)
		// Content-addressed: skip if blob already exists (deduplication).
		if _, err := os.Stat(blobPath); os.IsNotExist(err) {
			if err := os.WriteFile(blobPath, data, 0o600); err != nil {
				return nil, fmt.Errorf("write snapshot blob: %w", err)
			}
		}
	}

	// Append record to manifest (append-only).
	manifestPath := filepath.Join(s.sessionDir, "manifest.jsonl")
	f, err := os.OpenFile(manifestPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open manifest: %w", err)
	}
	line, _ := json.Marshal(rec)
	_, writeErr := fmt.Fprintf(f, "%s\n", line)
	closeErr := f.Close()
	if writeErr != nil {
		return nil, fmt.Errorf("write manifest: %w", writeErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close manifest: %w", closeErr)
	}

	s.records = append(s.records, rec)
	return &rec, nil
}

// Records returns a copy of all snapshot records.
func (s *Snapshotter) Records() []SnapshotRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SnapshotRecord, len(s.records))
	copy(out, s.records)
	return out
}

// RestoreInteraction restores all files changed in a given interaction
// to their pre-modification state. Files that were newly created (WasNewFile)
// are deleted. Returns the list of restored file paths.
//
// Records are processed in reverse chronological order and deduplicated
// by path so that the earliest snapshot (original state) is used.
func (s *Snapshotter) RestoreInteraction(interactionID string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Collect records for this interaction.
	var matching []SnapshotRecord
	for _, rec := range s.records {
		if rec.InteractionID == interactionID {
			matching = append(matching, rec)
		}
	}
	if len(matching) == 0 {
		return nil, fmt.Errorf("no snapshots found for interaction %s", interactionID)
	}

	// Sort by timestamp ascending so that the first snapshot per path
	// represents the original state before any modifications.
	sort.Slice(matching, func(i, j int) bool {
		return matching[i].Timestamp.Before(matching[j].Timestamp)
	})

	// Deduplicate by path: keep only the first record per path
	// (the original state before this interaction modified it).
	seen := make(map[string]bool)
	var unique []SnapshotRecord
	for _, rec := range matching {
		if seen[rec.Path] {
			continue
		}
		seen[rec.Path] = true
		unique = append(unique, rec)
	}

	var restored []string
	for _, rec := range unique {
		if rec.WasNewFile {
			// File didn't exist before — remove it.
			if err := os.Remove(rec.Path); err != nil && !os.IsNotExist(err) {
				return restored, fmt.Errorf("remove new file %s: %w", rec.Path, err)
			}
		} else if rec.TooLarge {
			// File was too large to snapshot — cannot restore.
			return restored, fmt.Errorf("cannot restore %s: file was too large to snapshot", rec.Path)
		} else {
			// Restore original content from blob.
			data, err := s.readBlob(rec.ContentHash)
			if err != nil {
				return restored, fmt.Errorf("read blob for %s: %w", rec.Path, err)
			}
			if err := os.MkdirAll(filepath.Dir(rec.Path), 0o700); err != nil {
				return restored, fmt.Errorf("mkdir for %s: %w", rec.Path, err)
			}
			mode := rec.FileMode
			if mode == 0 {
				mode = 0o644 // fallback for records without stored mode
			}
			if err := os.WriteFile(rec.Path, data, mode); err != nil {
				return restored, fmt.Errorf("write %s: %w", rec.Path, err)
			}
		}
		restored = append(restored, rec.Path)
	}

	return restored, nil
}

// ReadSnapshotContent reads the content of a snapshot blob by its hash.
func (s *Snapshotter) ReadSnapshotContent(hash string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readBlob(hash)
}

// readBlob reads a content-addressed blob. Caller must hold s.mu.
func (s *Snapshotter) readBlob(hash string) ([]byte, error) {
	if hash == "" {
		return nil, fmt.Errorf("empty content hash (file was new)")
	}
	blobPath := filepath.Join(s.sessionDir, hash)
	data, err := os.ReadFile(blobPath)
	if err != nil {
		return nil, fmt.Errorf("read blob %s: %w", hash, err)
	}
	return data, nil
}
