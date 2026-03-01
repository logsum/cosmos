package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cosmos/engine/manifest"

	"github.com/bmatcuk/doublestar/v4"
)

// Effect is the evaluated outcome of a permission check.
type Effect int

const (
	EffectAllow        Effect = iota // Permission granted silently.
	EffectDeny                       // Permission blocked silently.
	EffectPromptOnce                 // Prompt user; remember decision per project.
	EffectPromptAlways               // Prompt user every time.
)

func (e Effect) String() string {
	switch e {
	case EffectAllow:
		return "allow"
	case EffectDeny:
		return "deny"
	case EffectPromptOnce:
		return "prompt_once"
	case EffectPromptAlways:
		return "prompt_always"
	default:
		return fmt.Sprintf("Effect(%d)", int(e))
	}
}

// DecisionSource identifies where a decision came from.
type DecisionSource int

const (
	SourceManifest       DecisionSource = iota // Rule matched in manifest.
	SourcePolicyOverride                       // Team override in policy.json.
	SourcePersistedGrant                       // User grant for request_once.
	SourceDefaultDeny                          // No rule matched.
)

func (s DecisionSource) String() string {
	switch s {
	case SourceManifest:
		return "manifest"
	case SourcePolicyOverride:
		return "policy_override"
	case SourcePersistedGrant:
		return "persisted_grant"
	case SourceDefaultDeny:
		return "default_deny"
	default:
		return fmt.Sprintf("DecisionSource(%d)", int(s))
	}
}

// Decision is the result of evaluating a permission request.
type Decision struct {
	Effect      Effect
	MatchedRule *manifest.PermissionRule // nil for default-deny
	Source      DecisionSource
}

// PolicyFile is the on-disk format of .cosmos/policy.json.
type PolicyFile struct {
	Version   int                                    `json:"version"`
	Overrides map[string]map[string]PolicyEntry `json:"overrides"` // agentName -> permKey -> entry
}

// PolicyEntry is a single override or persisted grant.
type PolicyEntry struct {
	Effect    string `json:"effect"`              // "allow" or "deny"
	Reason    string `json:"reason"`              // "override" or "user_grant"
	Timestamp string `json:"timestamp,omitempty"` // ISO 8601
}

const policyFileVersion = 1

// Evaluator checks permission requests against manifest rules and
// per-project policy overrides.
type Evaluator struct {
	homeDir    string
	mu         sync.Mutex
	policyPath string
	overrides  map[string]map[string]PolicyEntry
}

// NewEvaluator creates an evaluator that loads overrides from policyPath.
// A missing policy file is not an error — it means no overrides exist yet.
func NewEvaluator(policyPath string) (*Evaluator, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	e := &Evaluator{
		homeDir:    home,
		policyPath: policyPath,
		overrides:  make(map[string]map[string]PolicyEntry),
	}
	if err := e.LoadPolicy(); err != nil {
		return nil, err
	}
	return e, nil
}

// newEvaluatorForTest creates an evaluator with an explicit homeDir (for tests).
func newEvaluatorForTest(policyPath, homeDir string) *Evaluator {
	e := &Evaluator{
		homeDir:    homeDir,
		policyPath: policyPath,
		overrides:  make(map[string]map[string]PolicyEntry),
	}
	// Best-effort load; tests that need specific overrides will write them first.
	_ = e.LoadPolicy()
	return e
}

// LoadPolicy (re)loads the policy file from disk. Safe for concurrent use.
func (e *Evaluator) LoadPolicy() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loadPolicyLocked()
}

func (e *Evaluator) loadPolicyLocked() error {
	data, err := os.ReadFile(e.policyPath)
	if errors.Is(err, os.ErrNotExist) {
		e.overrides = make(map[string]map[string]PolicyEntry)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read policy file: %w", err)
	}

	var pf PolicyFile
	if err := json.Unmarshal(data, &pf); err != nil {
		return fmt.Errorf("parse policy file: %w", err)
	}
	if pf.Version != policyFileVersion {
		return fmt.Errorf("unsupported policy file version %d (expected %d)", pf.Version, policyFileVersion)
	}
	if pf.Overrides == nil {
		pf.Overrides = make(map[string]map[string]PolicyEntry)
	}
	if err := validatePolicyOverrides(pf.Overrides); err != nil {
		return err
	}
	e.overrides = pf.Overrides
	return nil
}

// Evaluate checks a permission request against manifest rules and policy overrides.
func (e *Evaluator) Evaluate(agentName string, requested manifest.PermissionKey, rules []manifest.PermissionRule) Decision {
	e.mu.Lock()
	defer e.mu.Unlock()

	// 1. Check team overrides (reason: "override").
	// Normalize the requested key so that semantically equivalent paths
	// (e.g., ./src/pkg/../main.go vs ./src/main.go) hit the same override.
	normalizedRaw := e.normalizeKeyRaw(requested)
	if agentOverrides, ok := e.overrides[agentName]; ok {
		if entry, ok := agentOverrides[normalizedRaw]; ok && entry.Reason == "override" {
			return Decision{
				Effect: parseEntryEffect(entry.Effect),
				Source: SourcePolicyOverride,
			}
		}
		// Backward-compatibility: allow legacy non-normalized keys in policy.json.
		if normalizedRaw != requested.Raw {
			if entry, ok := agentOverrides[requested.Raw]; ok && entry.Reason == "override" {
				return Decision{
					Effect: parseEntryEffect(entry.Effect),
					Source: SourcePolicyOverride,
				}
			}
		}
	}

	// 2. Match manifest rules.
	bestRule, bestTier, bestLen := (*manifest.PermissionRule)(nil), -1, 0
	for i := range rules {
		rule := &rules[i]
		tier, matchLen := e.matchRule(rule.Key, requested)
		if tier < 0 {
			continue
		}
		if bestRule == nil || tier > bestTier ||
			(tier == bestTier && matchLen > bestLen) ||
			(tier == bestTier && matchLen == bestLen && modeRestrictiveness(rule.Mode) > modeRestrictiveness(bestRule.Mode)) {
			bestRule = rule
			bestTier = tier
			bestLen = matchLen
		}
	}

	// 3. Default deny if no rule matches.
	if bestRule == nil {
		return Decision{
			Effect: EffectDeny,
			Source: SourceDefaultDeny,
		}
	}

	// 4. For request_once, check persisted grants.
	if bestRule.Mode == manifest.PermissionRequestOnce {
		if agentOverrides, ok := e.overrides[agentName]; ok {
			if entry, ok := agentOverrides[bestRule.Key.Raw]; ok && entry.Reason == "user_grant" {
				return Decision{
					Effect:      parseEntryEffect(entry.Effect),
					MatchedRule: bestRule,
					Source:      SourcePersistedGrant,
				}
			}
		}
	}

	// 5. Map permission mode to effect.
	return Decision{
		Effect:      modeToEffect(bestRule.Mode),
		MatchedRule: bestRule,
		Source:      SourceManifest,
	}
}

// RecordOnceDecision persists a user's request_once decision to the policy file.
// The ruleKey should be the manifest rule's raw key (e.g., "net:http"), not the
// specific requested path.
func (e *Evaluator) RecordOnceDecision(agentName, ruleKey string, granted bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	effect := "deny"
	if granted {
		effect = "allow"
	}

	if e.overrides[agentName] == nil {
		e.overrides[agentName] = make(map[string]PolicyEntry)
	}
	e.overrides[agentName][ruleKey] = PolicyEntry{
		Effect:    effect,
		Reason:    "user_grant",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	return e.writePolicyLocked()
}

// matchRule returns (tier, matchLen) if the rule matches the request, or (-1, 0)
// if it does not match.
//
// Tier values:
//
//	2 = exact target match
//	1 = glob target match
//	0 = broad match (rule has no target)
//
// matchLen is the length of the rule's target pattern (for glob tie-breaking).
func (e *Evaluator) matchRule(rule, requested manifest.PermissionKey) (tier int, matchLen int) {
	if rule.Resource != requested.Resource || rule.Action != requested.Action {
		return -1, 0
	}

	// Broad rule (no target) matches any request.
	if !rule.HasTarget {
		return 0, 0
	}

	// Rule has a target but request does not — no match.
	if !requested.HasTarget {
		return -1, 0
	}

	ruleTarget := filepath.Clean(expandTilde(rule.Target, e.homeDir))
	reqTarget := filepath.Clean(expandTilde(requested.Target, e.homeDir))

	// Security: Relative rules (e.g., ./src/**) should not match absolute
	// requests (e.g., /etc/passwd). Similarly, relative requests should
	// not escape the current directory via .. if the rule target is anchored
	// to the current directory (no .. prefix).
	ruleIsAbs := filepath.IsAbs(ruleTarget)
	reqIsAbs := filepath.IsAbs(reqTarget)

	if !ruleIsAbs && reqIsAbs {
		return -1, 0
	}
	if !ruleIsAbs && !reqIsAbs {
		ruleEscapes := strings.HasPrefix(ruleTarget, "..")
		reqEscapes := strings.HasPrefix(reqTarget, "..")
		if !ruleEscapes && reqEscapes {
			return -1, 0
		}
	}

	// Exact match.
	if ruleTarget == reqTarget {
		return 2, len(rule.Target)
	}

	// Glob match.
	if rule.HasGlob {
		matched, err := doublestar.Match(ruleTarget, reqTarget)
		if err == nil && matched {
			return 1, len(rule.Target)
		}
	}

	return -1, 0
}

// normalizeKeyRaw returns a canonical form of the permission key's raw string
// so that override lookups are path-normalized (tilde expansion + filepath.Clean).
func (e *Evaluator) normalizeKeyRaw(key manifest.PermissionKey) string {
	if !key.HasTarget {
		return key.Raw
	}
	normalized := filepath.Clean(expandTilde(key.Target, e.homeDir))
	return key.Resource + ":" + key.Action + ":" + normalized
}

func expandTilde(path, homeDir string) string {
	if homeDir == "" {
		return path
	}
	if path == "~" {
		return homeDir
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(homeDir, path[2:])
	}
	return path
}

func modeToEffect(mode manifest.PermissionMode) Effect {
	switch mode {
	case manifest.PermissionAllow:
		return EffectAllow
	case manifest.PermissionDeny:
		return EffectDeny
	case manifest.PermissionRequestOnce:
		return EffectPromptOnce
	case manifest.PermissionRequestAlways:
		return EffectPromptAlways
	default:
		return EffectDeny
	}
}

// modeRestrictiveness returns a numeric score for tie-breaking: higher = more restrictive.
func modeRestrictiveness(mode manifest.PermissionMode) int {
	switch mode {
	case manifest.PermissionAllow:
		return 0
	case manifest.PermissionRequestOnce:
		return 1
	case manifest.PermissionRequestAlways:
		return 2
	case manifest.PermissionDeny:
		return 3
	default:
		return 4
	}
}

func parseEntryEffect(effect string) Effect {
	switch effect {
	case "allow":
		return EffectAllow
	case "deny":
		return EffectDeny
	default:
		return EffectDeny
	}
}

func validatePolicyOverrides(overrides map[string]map[string]PolicyEntry) error {
	for agentName, entries := range overrides {
		for permKey, entry := range entries {
			if !isValidPolicyEffect(entry.Effect) {
				return fmt.Errorf("invalid policy effect for agent %q permission %q: %q", agentName, permKey, entry.Effect)
			}
			if !isValidPolicyReason(entry.Reason) {
				return fmt.Errorf("invalid policy reason for agent %q permission %q: %q", agentName, permKey, entry.Reason)
			}
		}
	}
	return nil
}

func isValidPolicyEffect(effect string) bool {
	switch effect {
	case "allow", "deny":
		return true
	default:
		return false
	}
}

func isValidPolicyReason(reason string) bool {
	switch reason {
	case "override", "user_grant":
		return true
	default:
		return false
	}
}

func (e *Evaluator) writePolicyLocked() error {
	pf := PolicyFile{
		Version:   policyFileVersion,
		Overrides: e.overrides,
	}
	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal policy file: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(e.policyPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create policy directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".policy-*.tmp")
	if err != nil {
		return fmt.Errorf("create policy temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("chmod policy temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write policy temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close policy temp file: %w", err)
	}
	if err := os.Rename(tmpPath, e.policyPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename policy file: %w", err)
	}
	return nil
}
