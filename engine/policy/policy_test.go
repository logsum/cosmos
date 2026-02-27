package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"cosmos/engine/manifest"
)

// --- helpers ---

func mustParseKey(t *testing.T, raw string) manifest.PermissionKey {
	t.Helper()
	k, err := manifest.ParsePermissionKey(raw)
	if err != nil {
		t.Fatalf("ParsePermissionKey(%q): %v", raw, err)
	}
	return k
}

func rule(t *testing.T, raw string, mode manifest.PermissionMode) manifest.PermissionRule {
	t.Helper()
	return manifest.PermissionRule{Key: mustParseKey(t, raw), Mode: mode}
}

func testEvaluator(t *testing.T) (*Evaluator, string) {
	t.Helper()
	dir := t.TempDir()
	policyPath := filepath.Join(dir, ".cosmos", "policy.json")
	homeDir := filepath.Join(dir, "fakehome")
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return newEvaluatorForTest(policyPath, homeDir), homeDir
}

// --- Basic matching ---

func TestMatchExactTarget(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/main.go", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/main.go"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("want EffectAllow, got %v", d.Effect)
	}
	if d.Source != SourceManifest {
		t.Fatalf("want SourceManifest, got %v", d.Source)
	}
}

func TestMatchGlobNested(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/pkg/foo.go"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("want EffectAllow, got %v", d.Effect)
	}
}

func TestGlobNoMatch(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./docs/readme.md"), rules)
	if d.Effect != EffectDeny {
		t.Fatalf("want EffectDeny (default), got %v", d.Effect)
	}
	if d.Source != SourceDefaultDeny {
		t.Fatalf("want SourceDefaultDeny, got %v", d.Source)
	}
}

func TestBroadMatchNoTarget(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionRequestOnce),
	}

	d := e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectPromptOnce {
		t.Fatalf("want EffectPromptOnce, got %v", d.Effect)
	}
}

// --- Default deny ---

func TestDefaultDenyUndeclared(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "docker:run"), rules)
	if d.Effect != EffectDeny {
		t.Fatalf("want EffectDeny, got %v", d.Effect)
	}
	if d.Source != SourceDefaultDeny {
		t.Fatalf("want SourceDefaultDeny, got %v", d.Source)
	}
	if d.MatchedRule != nil {
		t.Fatal("want nil MatchedRule for default deny")
	}
}

func TestDefaultDenyEmptyRules(t *testing.T) {
	e, _ := testEvaluator(t)
	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./file.txt"), nil)
	if d.Effect != EffectDeny || d.Source != SourceDefaultDeny {
		t.Fatalf("want default deny, got %v/%v", d.Effect, d.Source)
	}
}

func TestDefaultDenyWrongResource(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "net:read:./src/main.go"), rules)
	if d.Effect != EffectDeny || d.Source != SourceDefaultDeny {
		t.Fatalf("want default deny, got %v/%v", d.Effect, d.Source)
	}
}

// --- Specificity ---

func TestSpecificityExactBeatsGlob(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
		rule(t, "fs:read:./src/secret.go", manifest.PermissionDeny),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/secret.go"), rules)
	if d.Effect != EffectDeny {
		t.Fatalf("exact rule should win: want EffectDeny, got %v", d.Effect)
	}
}

func TestSpecificityGlobBeatsBroad(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read", manifest.PermissionDeny),
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/main.go"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("glob should beat broad: want EffectAllow, got %v", d.Effect)
	}
}

func TestSpecificityNarrowerGlobWins(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./**", manifest.PermissionAllow),
		rule(t, "fs:read:./src/internal/**", manifest.PermissionDeny),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/internal/secret.go"), rules)
	if d.Effect != EffectDeny {
		t.Fatalf("narrower glob should win: want EffectDeny, got %v", d.Effect)
	}
}

func TestTieBreakingMostRestrictiveWins(t *testing.T) {
	e, _ := testEvaluator(t)
	// Two glob rules with same target length — most restrictive should win.
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./abc/**", manifest.PermissionAllow),
		rule(t, "fs:read:./abc/**", manifest.PermissionDeny),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./abc/file.txt"), rules)
	if d.Effect != EffectDeny {
		t.Fatalf("most restrictive should win: want EffectDeny, got %v", d.Effect)
	}
}

// --- Tilde expansion ---

func TestTildeExpansionInRule(t *testing.T) {
	e, homeDir := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:~/.config/**", manifest.PermissionAllow),
	}

	reqTarget := filepath.Join(homeDir, ".config", "cosmos.toml")
	d := e.Evaluate("agent", mustParseKey(t, "fs:read:"+reqTarget), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("tilde rule should match absolute request: want EffectAllow, got %v", d.Effect)
	}
}

func TestTildeExpansionInRequest(t *testing.T) {
	e, homeDir := testEvaluator(t)
	ruleTarget := filepath.Join(homeDir, ".config", "**")
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:"+ruleTarget, manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:~/.config/cosmos.toml"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("tilde request should match absolute rule: want EffectAllow, got %v", d.Effect)
	}
}

func TestTildeExpansionInBoth(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:~/.config/**", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:~/.config/cosmos.toml"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("both tilde should match: want EffectAllow, got %v", d.Effect)
	}
}

func TestNoTilde(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./config/**", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./config/file.txt"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("non-tilde paths should match: want EffectAllow, got %v", d.Effect)
	}
}

// --- All four modes ---

func TestAllModes(t *testing.T) {
	e, _ := testEvaluator(t)
	cases := []struct {
		mode manifest.PermissionMode
		want Effect
	}{
		{manifest.PermissionAllow, EffectAllow},
		{manifest.PermissionDeny, EffectDeny},
		{manifest.PermissionRequestOnce, EffectPromptOnce},
		{manifest.PermissionRequestAlways, EffectPromptAlways},
	}

	for _, tc := range cases {
		rules := []manifest.PermissionRule{
			rule(t, "net:http", tc.mode),
		}
		d := e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
		if d.Effect != tc.want {
			t.Fatalf("mode %s: want %v, got %v", tc.mode, tc.want, d.Effect)
		}
	}
}

// --- Policy overrides ---

func TestTeamOverrideTakesPrecedence(t *testing.T) {
	e, _ := testEvaluator(t)
	// Inject a team override directly.
	e.overrides["agent"] = map[string]PolicyEntry{
		"net:http": {Effect: "deny", Reason: "override"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectDeny {
		t.Fatalf("team override should win: want EffectDeny, got %v", d.Effect)
	}
	if d.Source != SourcePolicyOverride {
		t.Fatalf("want SourcePolicyOverride, got %v", d.Source)
	}
}

func TestTeamOverrideAllowOverridesDeny(t *testing.T) {
	e, _ := testEvaluator(t)
	e.overrides["agent"] = map[string]PolicyEntry{
		"docker:run": {Effect: "allow", Reason: "override"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "docker:run", manifest.PermissionDeny),
	}

	d := e.Evaluate("agent", mustParseKey(t, "docker:run"), rules)
	if d.Effect != EffectAllow || d.Source != SourcePolicyOverride {
		t.Fatalf("want allow/policy_override, got %v/%v", d.Effect, d.Source)
	}
}

func TestOverrideOnlyAffectsMatchingAgent(t *testing.T) {
	e, _ := testEvaluator(t)
	e.overrides["agent-a"] = map[string]PolicyEntry{
		"net:http": {Effect: "deny", Reason: "override"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionAllow),
	}

	// Different agent should not be affected.
	d := e.Evaluate("agent-b", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("override for agent-a should not affect agent-b: want EffectAllow, got %v", d.Effect)
	}
}

// --- Persisted grants ---

func TestPersistedGrantAllowForRequestOnce(t *testing.T) {
	e, _ := testEvaluator(t)
	e.overrides["agent"] = map[string]PolicyEntry{
		"net:http": {Effect: "allow", Reason: "user_grant"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionRequestOnce),
	}

	d := e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("persisted grant should resolve to allow: want EffectAllow, got %v", d.Effect)
	}
	if d.Source != SourcePersistedGrant {
		t.Fatalf("want SourcePersistedGrant, got %v", d.Source)
	}
}

func TestPersistedGrantDenyForRequestOnce(t *testing.T) {
	e, _ := testEvaluator(t)
	e.overrides["agent"] = map[string]PolicyEntry{
		"net:http": {Effect: "deny", Reason: "user_grant"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionRequestOnce),
	}

	d := e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectDeny {
		t.Fatalf("persisted deny grant: want EffectDeny, got %v", d.Effect)
	}
	if d.Source != SourcePersistedGrant {
		t.Fatalf("want SourcePersistedGrant, got %v", d.Source)
	}
}

func TestPersistedGrantIgnoredForRequestAlways(t *testing.T) {
	e, _ := testEvaluator(t)
	e.overrides["agent"] = map[string]PolicyEntry{
		"docker:run": {Effect: "allow", Reason: "user_grant"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "docker:run", manifest.PermissionRequestAlways),
	}

	d := e.Evaluate("agent", mustParseKey(t, "docker:run"), rules)
	if d.Effect != EffectPromptAlways {
		t.Fatalf("request_always ignores grants: want EffectPromptAlways, got %v", d.Effect)
	}
	if d.Source != SourceManifest {
		t.Fatalf("want SourceManifest, got %v", d.Source)
	}
}

func TestPersistedGrantKeyedByRuleNotRequest(t *testing.T) {
	e, _ := testEvaluator(t)
	// Grant keyed by the manifest rule's raw key "net:http" (broad).
	e.overrides["agent"] = map[string]PolicyEntry{
		"net:http": {Effect: "allow", Reason: "user_grant"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionRequestOnce),
	}

	// Request with a specific target — should still use the broad rule's grant.
	d := e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectAllow || d.Source != SourcePersistedGrant {
		t.Fatalf("grant keyed by rule should match: want allow/persisted_grant, got %v/%v", d.Effect, d.Source)
	}
}

// --- RecordOnceDecision ---

func TestRecordOnceDecisionAndReEvaluate(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionRequestOnce),
	}

	// Before recording: should prompt.
	d := e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectPromptOnce {
		t.Fatalf("before grant: want EffectPromptOnce, got %v", d.Effect)
	}

	// Record allow.
	if err := e.RecordOnceDecision("agent", "net:http", true); err != nil {
		t.Fatalf("RecordOnceDecision: %v", err)
	}

	// After recording: should allow.
	d = e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectAllow || d.Source != SourcePersistedGrant {
		t.Fatalf("after grant: want allow/persisted_grant, got %v/%v", d.Effect, d.Source)
	}
}

func TestRecordOnceDecisionDeny(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionRequestOnce),
	}

	if err := e.RecordOnceDecision("agent", "net:http", false); err != nil {
		t.Fatalf("RecordOnceDecision: %v", err)
	}

	d := e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectDeny || d.Source != SourcePersistedGrant {
		t.Fatalf("deny grant: want deny/persisted_grant, got %v/%v", d.Effect, d.Source)
	}
}

func TestRecordOnceDecisionWritesFile(t *testing.T) {
	e, _ := testEvaluator(t)

	if err := e.RecordOnceDecision("agent", "net:http", true); err != nil {
		t.Fatalf("RecordOnceDecision: %v", err)
	}

	data, err := os.ReadFile(e.policyPath)
	if err != nil {
		t.Fatalf("read policy file: %v", err)
	}

	var pf PolicyFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("unmarshal policy file: %v", err)
	}
	if pf.Version != 1 {
		t.Fatalf("want version 1, got %d", pf.Version)
	}
	entry, ok := pf.Overrides["agent"]["net:http"]
	if !ok {
		t.Fatal("expected entry for agent/net:http")
	}
	if entry.Effect != "allow" || entry.Reason != "user_grant" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.Timestamp == "" {
		t.Fatal("expected non-empty timestamp")
	}
}

// --- Policy file I/O ---

func TestLoadPolicyNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "does-not-exist", "policy.json")
	e := newEvaluatorForTest(policyPath, dir)

	// Should succeed with empty overrides.
	if len(e.overrides) != 0 {
		t.Fatalf("expected empty overrides, got %d entries", len(e.overrides))
	}
}

func TestLoadPolicyValidFile(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	pf := PolicyFile{
		Version: 1,
		Overrides: map[string]map[string]PolicyEntry{
			"myagent": {
				"net:http": {Effect: "deny", Reason: "override"},
			},
		},
	}
	data, _ := json.Marshal(pf)
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	e := newEvaluatorForTest(policyPath, dir)
	entry, ok := e.overrides["myagent"]["net:http"]
	if !ok {
		t.Fatal("expected override entry")
	}
	if entry.Effect != "deny" || entry.Reason != "override" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

func TestLoadPolicyMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policyPath, []byte("{malformed"), 0o600); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	e := &Evaluator{policyPath: policyPath, overrides: make(map[string]map[string]PolicyEntry)}
	err := e.LoadPolicy()
	if err == nil || !strings.Contains(err.Error(), "parse policy file") {
		t.Fatalf("want parse error, got %v", err)
	}
}

func TestLoadPolicyUnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	pf := PolicyFile{Version: 99, Overrides: map[string]map[string]PolicyEntry{}}
	data, _ := json.Marshal(pf)
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	e := &Evaluator{policyPath: policyPath, overrides: make(map[string]map[string]PolicyEntry)}
	err := e.LoadPolicy()
	if err == nil || !strings.Contains(err.Error(), "unsupported policy file version") {
		t.Fatalf("want version error, got %v", err)
	}
}

func TestLoadPolicyInvalidEffect(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	pf := PolicyFile{
		Version: 1,
		Overrides: map[string]map[string]PolicyEntry{
			"agent": {
				"net:http": {Effect: "alllow", Reason: "override"},
			},
		},
	}
	data, _ := json.Marshal(pf)
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	e := &Evaluator{policyPath: policyPath, overrides: make(map[string]map[string]PolicyEntry)}
	err := e.LoadPolicy()
	if err == nil || !strings.Contains(err.Error(), "invalid policy effect") {
		t.Fatalf("want invalid effect error, got %v", err)
	}
}

func TestLoadPolicyInvalidReason(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	pf := PolicyFile{
		Version: 1,
		Overrides: map[string]map[string]PolicyEntry{
			"agent": {
				"net:http": {Effect: "allow", Reason: "overide"},
			},
		},
	}
	data, _ := json.Marshal(pf)
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	e := &Evaluator{policyPath: policyPath, overrides: make(map[string]map[string]PolicyEntry)}
	err := e.LoadPolicy()
	if err == nil || !strings.Contains(err.Error(), "invalid policy reason") {
		t.Fatalf("want invalid reason error, got %v", err)
	}
}

func TestAtomicWriteCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "deep", "nested", "policy.json")
	e := newEvaluatorForTest(policyPath, dir)

	if err := e.RecordOnceDecision("agent", "net:http", true); err != nil {
		t.Fatalf("RecordOnceDecision: %v", err)
	}

	if _, err := os.Stat(policyPath); err != nil {
		t.Fatalf("policy file should exist: %v", err)
	}
}

func TestPolicyFilePermissions(t *testing.T) {
	e, _ := testEvaluator(t)
	if err := e.RecordOnceDecision("agent", "net:http", true); err != nil {
		t.Fatalf("RecordOnceDecision: %v", err)
	}

	info, err := os.Stat(e.policyPath)
	if err != nil {
		t.Fatalf("stat policy file: %v", err)
	}
	// Check file is not world-readable (at least owner-only).
	perm := info.Mode().Perm()
	if perm&0o077 != 0 {
		t.Fatalf("policy file should be owner-only, got %o", perm)
	}
}

// --- Concurrency ---

func TestConcurrentEvaluateAndWrite(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionRequestOnce),
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
			e.Evaluate("agent", mustParseKey(t, "fs:read:./src/main.go"), rules)
			if i%10 == 0 {
				_ = e.RecordOnceDecision("agent", "net:http", i%2 == 0)
			}
		}(i)
	}
	wg.Wait()

	// Verify policy file is valid JSON after concurrent writes.
	data, err := os.ReadFile(e.policyPath)
	if err != nil {
		t.Fatalf("read policy file: %v", err)
	}
	var pf PolicyFile
	if err := json.Unmarshal(data, &pf); err != nil {
		t.Fatalf("policy file corrupted: %v", err)
	}
}

// --- Edge cases ---

func TestSingleStarDoesNotMatchNested(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/*", manifest.PermissionAllow),
	}

	// Single * should match files in src/ but not nested directories.
	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/main.go"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("single * should match direct child: want EffectAllow, got %v", d.Effect)
	}

	d = e.Evaluate("agent", mustParseKey(t, "fs:read:./src/pkg/foo.go"), rules)
	if d.Effect != EffectDeny {
		t.Fatalf("single * should not match nested: want EffectDeny (default), got %v", d.Effect)
	}
}

func TestDoubleStarMatchesNested(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/a/b/c/deep.go"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("** should match deeply nested: want EffectAllow, got %v", d.Effect)
	}
}

func TestRequestWithoutTargetVsTargetedRule(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	// Request without target should not match a targeted rule.
	d := e.Evaluate("agent", mustParseKey(t, "fs:read"), rules)
	if d.Effect != EffectDeny || d.Source != SourceDefaultDeny {
		t.Fatalf("untargeted request vs targeted rule: want deny/default_deny, got %v/%v", d.Effect, d.Source)
	}
}

func TestCleanPathNormalization(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	// Request with ../ segments that normalize to ./src/.
	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/pkg/../main.go"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("path normalization should still match: want EffectAllow, got %v", d.Effect)
	}
}

func TestReloadPolicyAfterExternalChange(t *testing.T) {
	e, _ := testEvaluator(t)
	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionRequestOnce),
	}

	// Initially, no grants.
	d := e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectPromptOnce {
		t.Fatalf("before reload: want EffectPromptOnce, got %v", d.Effect)
	}

	// Write policy file externally.
	pf := PolicyFile{
		Version: 1,
		Overrides: map[string]map[string]PolicyEntry{
			"agent": {
				"net:http": {Effect: "allow", Reason: "user_grant"},
			},
		},
	}
	data, _ := json.Marshal(pf)
	if err := os.MkdirAll(filepath.Dir(e.policyPath), 0o700); err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	if err := os.WriteFile(e.policyPath, data, 0o600); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	if err := e.LoadPolicy(); err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	d = e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectAllow || d.Source != SourcePersistedGrant {
		t.Fatalf("after reload: want allow/persisted_grant, got %v/%v", d.Effect, d.Source)
	}
}

func TestEffectString(t *testing.T) {
	cases := []struct {
		e    Effect
		want string
	}{
		{EffectAllow, "allow"},
		{EffectDeny, "deny"},
		{EffectPromptOnce, "prompt_once"},
		{EffectPromptAlways, "prompt_always"},
		{Effect(99), "Effect(99)"},
	}
	for _, tc := range cases {
		if got := tc.e.String(); got != tc.want {
			t.Fatalf("Effect(%d).String() = %q, want %q", int(tc.e), got, tc.want)
		}
	}
}

func TestDecisionSourceString(t *testing.T) {
	cases := []struct {
		s    DecisionSource
		want string
	}{
		{SourceManifest, "manifest"},
		{SourcePolicyOverride, "policy_override"},
		{SourcePersistedGrant, "persisted_grant"},
		{SourceDefaultDeny, "default_deny"},
		{DecisionSource(99), "DecisionSource(99)"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Fatalf("DecisionSource(%d).String() = %q, want %q", int(tc.s), got, tc.want)
		}
	}
}

func TestNewEvaluatorWithExistingPolicy(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	pf := PolicyFile{
		Version: 1,
		Overrides: map[string]map[string]PolicyEntry{
			"agent": {
				"net:http": {Effect: "allow", Reason: "user_grant"},
			},
		},
	}
	data, _ := json.Marshal(pf)
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	e, err := NewEvaluator(policyPath)
	if err != nil {
		t.Fatalf("NewEvaluator: %v", err)
	}

	rules := []manifest.PermissionRule{
		rule(t, "net:http", manifest.PermissionRequestOnce),
	}
	d := e.Evaluate("agent", mustParseKey(t, "net:http"), rules)
	if d.Effect != EffectAllow || d.Source != SourcePersistedGrant {
		t.Fatalf("NewEvaluator should load existing grants: want allow/persisted_grant, got %v/%v", d.Effect, d.Source)
	}
}

func TestNewEvaluatorWithBadPolicy(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(policyPath, []byte("garbage"), 0o600); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	_, err := NewEvaluator(policyPath)
	if err == nil {
		t.Fatal("expected error for malformed policy file")
	}
}

func TestTeamOverrideMatchesExactRequestKey(t *testing.T) {
	e, _ := testEvaluator(t)
	// Override is keyed by the normalized form of the requested key.
	// filepath.Clean("./src/main.go") = "src/main.go"
	e.overrides["agent"] = map[string]PolicyEntry{
		"fs:read:src/main.go": {Effect: "deny", Reason: "override"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/main.go"), rules)
	if d.Effect != EffectDeny || d.Source != SourcePolicyOverride {
		t.Fatalf("override by exact key: want deny/policy_override, got %v/%v", d.Effect, d.Source)
	}

	// Different file not overridden should still be allowed by glob.
	d = e.Evaluate("agent", mustParseKey(t, "fs:read:./src/other.go"), rules)
	if d.Effect != EffectAllow {
		t.Fatalf("non-overridden file should use manifest: want EffectAllow, got %v", d.Effect)
	}
}

func TestTeamOverrideCannotBypassWithNonCanonicalPath(t *testing.T) {
	e, _ := testEvaluator(t)
	// Override keyed with a clean path.
	e.overrides["agent"] = map[string]PolicyEntry{
		"fs:read:src/main.go": {Effect: "deny", Reason: "override"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	// Request with ../  segments that normalize to the same path — must still be denied.
	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/pkg/../main.go"), rules)
	if d.Effect != EffectDeny || d.Source != SourcePolicyOverride {
		t.Fatalf("non-canonical path should hit override: want deny/policy_override, got %v/%v", d.Effect, d.Source)
	}

	// Request with redundant ./ — must still be denied.
	d = e.Evaluate("agent", mustParseKey(t, "fs:read:./src/./main.go"), rules)
	if d.Effect != EffectDeny || d.Source != SourcePolicyOverride {
		t.Fatalf("./  path should hit override: want deny/policy_override, got %v/%v", d.Effect, d.Source)
	}
}

func TestTeamOverrideTildeNormalization(t *testing.T) {
	e, homeDir := testEvaluator(t)
	// Override keyed with the normalized absolute path.
	normalizedKey := "fs:read:" + filepath.Join(homeDir, ".config", "secret.toml")
	e.overrides["agent"] = map[string]PolicyEntry{
		normalizedKey: {Effect: "deny", Reason: "override"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "fs:read:~/.config/**", manifest.PermissionAllow),
	}

	// Request using tilde — should normalize and hit the override.
	d := e.Evaluate("agent", mustParseKey(t, "fs:read:~/.config/secret.toml"), rules)
	if d.Effect != EffectDeny || d.Source != SourcePolicyOverride {
		t.Fatalf("tilde request should hit override: want deny/policy_override, got %v/%v", d.Effect, d.Source)
	}
}

func TestTeamOverrideLegacyRawKeyStillMatches(t *testing.T) {
	e, _ := testEvaluator(t)
	// Legacy key format (non-normalized target) should continue to work.
	e.overrides["agent"] = map[string]PolicyEntry{
		"fs:read:./src/main.go": {Effect: "deny", Reason: "override"},
	}

	rules := []manifest.PermissionRule{
		rule(t, "fs:read:./src/**", manifest.PermissionAllow),
	}

	d := e.Evaluate("agent", mustParseKey(t, "fs:read:./src/main.go"), rules)
	if d.Effect != EffectDeny || d.Source != SourcePolicyOverride {
		t.Fatalf("legacy raw key should still hit override: want deny/policy_override, got %v/%v", d.Effect, d.Source)
	}
}
