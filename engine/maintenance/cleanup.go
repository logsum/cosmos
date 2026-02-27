package maintenance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CleanupOptions configures session data cleanup behavior.
type CleanupOptions struct {
	// CosmosDir is the project-local .cosmos directory path (default: ".cosmos")
	CosmosDir string

	// SessionsDir is the user-global sessions directory path (default: "~/.cosmos/sessions")
	SessionsDir string

	// MaxAge is the maximum age of session data to keep (default: 30 days)
	// Data older than this will be deleted.
	MaxAge time.Duration

	// DryRun when true will scan and report what would be deleted without actually deleting
	DryRun bool
}

// CleanupResult contains the results of a cleanup operation.
type CleanupResult struct {
	// DeletedAuditFiles is the count of audit log files deleted (.jsonl and .jsonl.old)
	DeletedAuditFiles int

	// DeletedSnapshotDirs is the count of snapshot directories deleted
	// (Phase 4 — not yet implemented)
	DeletedSnapshotDirs int

	// DeletedSessionFiles is the count of session state files deleted
	// (Phase 5 — not yet implemented)
	DeletedSessionFiles int

	// Errors is a list of non-fatal errors encountered during cleanup.
	// Fatal errors (e.g., directory access failures) are returned as the function error.
	Errors []string
}

// DefaultCleanupOptions returns cleanup options with sensible defaults.
func DefaultCleanupOptions() CleanupOptions {
	return CleanupOptions{
		CosmosDir:   ".cosmos",
		SessionsDir: filepath.Join(os.Getenv("HOME"), ".cosmos", "sessions"),
		MaxAge:      30 * 24 * time.Hour, // 30 days
		DryRun:      false,
	}
}

// CleanupSessionData deletes session-related data older than the configured max age.
// This includes:
//   - Audit logs: .cosmos/audit-*.jsonl and .cosmos/audit-*.jsonl.old (project-local)
//   - Snapshots: .cosmos/snapshots/{sessionID}/ (planned, Phase 4)
//   - Session state: ~/.cosmos/sessions/*.json (planned, Phase 5)
//
// Age is determined by file ModTime. Files and directories with ModTime older than
// MaxAge are deleted. Newly created sessions (even if created during cleanup) will
// not be deleted as they're too recent.
//
// Error handling:
//   - Fatal errors (directory access failures) are returned immediately
//   - Non-fatal errors (individual file deletion failures) are collected in result.Errors
//   - Missing directories (future data not yet implemented) are skipped gracefully
//
// The function is safe to call at any time and will not block for long (typically <100ms).
func CleanupSessionData(opts CleanupOptions) (CleanupResult, error) {
	// Apply defaults if not set
	if opts.CosmosDir == "" {
		opts.CosmosDir = ".cosmos"
	}
	if opts.SessionsDir == "" {
		opts.SessionsDir = filepath.Join(os.Getenv("HOME"), ".cosmos", "sessions")
	}
	if opts.MaxAge == 0 {
		opts.MaxAge = 30 * 24 * time.Hour
	}

	result := CleanupResult{}
	cutoff := time.Now().Add(-opts.MaxAge)

	// Clean audit logs in project-local .cosmos directory
	if err := cleanupAuditLogs(opts.CosmosDir, cutoff, opts.DryRun, &result); err != nil {
		return result, fmt.Errorf("cleanup audit logs: %w", err)
	}

	// Clean snapshots (Phase 4 — gracefully skip if not implemented yet)
	snapshotsDir := filepath.Join(opts.CosmosDir, "snapshots")
	if err := cleanupSnapshots(snapshotsDir, cutoff, opts.DryRun, &result); err != nil {
		// Non-fatal if directory doesn't exist (feature not yet implemented)
		if !os.IsNotExist(err) {
			return result, fmt.Errorf("cleanup snapshots: %w", err)
		}
	}

	// Clean session state files (Phase 5 — gracefully skip if not implemented yet)
	if err := cleanupSessionFiles(opts.SessionsDir, cutoff, opts.DryRun, &result); err != nil {
		// Non-fatal if directory doesn't exist (feature not yet implemented)
		if !os.IsNotExist(err) {
			return result, fmt.Errorf("cleanup session files: %w", err)
		}
	}

	return result, nil
}

// cleanupAuditLogs removes audit log files (.jsonl and .jsonl.old) older than cutoff.
func cleanupAuditLogs(cosmosDir string, cutoff time.Time, dryRun bool, result *CleanupResult) error {
	// Check if .cosmos directory exists
	if _, err := os.Stat(cosmosDir); err != nil {
		if os.IsNotExist(err) {
			return nil // Gracefully skip if .cosmos doesn't exist yet
		}
		return fmt.Errorf("stat cosmos directory: %w", err)
	}

	// Find all audit-*.jsonl and audit-*.jsonl.old files
	pattern := filepath.Join(cosmosDir, "audit-*.jsonl*")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob audit files: %w", err)
	}

	for _, path := range matches {
		// Only match audit-*.jsonl and audit-*.jsonl.old (not other patterns)
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "audit-") {
			continue
		}
		if !strings.HasSuffix(base, ".jsonl") && !strings.HasSuffix(base, ".jsonl.old") {
			continue
		}

		// Check file age
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				// File deleted between glob and stat (race condition with concurrent session)
				continue
			}
			result.Errors = append(result.Errors, fmt.Sprintf("stat %s: %v", path, err))
			continue
		}

		if info.ModTime().Before(cutoff) {
			if dryRun {
				result.DeletedAuditFiles++
				continue
			}

			if err := os.Remove(path); err != nil {
				if os.IsNotExist(err) {
					// File deleted between stat and remove (race condition)
					continue
				}
				result.Errors = append(result.Errors, fmt.Sprintf("remove %s: %v", path, err))
				continue
			}
			result.DeletedAuditFiles++
		}
	}

	return nil
}

// cleanupSnapshots removes snapshot directories older than cutoff (Phase 4 implementation).
func cleanupSnapshots(snapshotsDir string, cutoff time.Time, dryRun bool, result *CleanupResult) error {
	// Check if snapshots directory exists
	if _, err := os.Stat(snapshotsDir); err != nil {
		return err // Return error to caller (will be checked for IsNotExist)
	}

	// List session directories
	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		return fmt.Errorf("read snapshots directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		path := filepath.Join(snapshotsDir, entry.Name())

		// Check directory age
		info, err := entry.Info()
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("stat %s: %v", path, err))
			continue
		}

		if info.ModTime().Before(cutoff) {
			if dryRun {
				result.DeletedSnapshotDirs++
				continue
			}

			if err := os.RemoveAll(path); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("remove %s: %v", path, err))
				continue
			}
			result.DeletedSnapshotDirs++
		}
	}

	return nil
}

// cleanupSessionFiles removes session state files older than cutoff (Phase 5 implementation).
func cleanupSessionFiles(sessionsDir string, cutoff time.Time, dryRun bool, result *CleanupResult) error {
	// Check if sessions directory exists
	if _, err := os.Stat(sessionsDir); err != nil {
		return err // Return error to caller (will be checked for IsNotExist)
	}

	// Find all session JSON files
	pattern := filepath.Join(sessionsDir, "*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Errorf("glob session files: %w", err)
	}

	for _, path := range matches {
		// Check file age
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			result.Errors = append(result.Errors, fmt.Sprintf("stat %s: %v", path, err))
			continue
		}

		if info.ModTime().Before(cutoff) {
			if dryRun {
				result.DeletedSessionFiles++
				continue
			}

			if err := os.Remove(path); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				result.Errors = append(result.Errors, fmt.Sprintf("remove %s: %v", path, err))
				continue
			}
			result.DeletedSessionFiles++
		}
	}

	return nil
}
