package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.AWSRegion != "us-east-1" {
		t.Errorf("AWSRegion = %q, want %q", cfg.AWSRegion, "us-east-1")
	}
	if cfg.AWSProfile != "" {
		t.Errorf("AWSProfile = %q, want empty", cfg.AWSProfile)
	}
	if cfg.DefaultModel != "us.anthropic.claude-3-5-sonnet-20241022-v2:0" {
		t.Errorf("DefaultModel = %q, want %q", cfg.DefaultModel, "us.anthropic.claude-3-5-sonnet-20241022-v2:0")
	}
	if cfg.MaxToolTimeout != 5*time.Minute {
		t.Errorf("MaxToolTimeout = %v, want %v", cfg.MaxToolTimeout, 5*time.Minute)
	}

	// Sub-dirs should be children of CosmosDir.
	if filepath.Dir(cfg.SessionsDir) != cfg.CosmosDir {
		t.Errorf("SessionsDir %q is not a child of CosmosDir %q", cfg.SessionsDir, cfg.CosmosDir)
	}
	if filepath.Dir(cfg.AgentsDir) != cfg.CosmosDir {
		t.Errorf("AgentsDir %q is not a child of CosmosDir %q", cfg.AgentsDir, cfg.CosmosDir)
	}
}

func TestLoadNoFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "nonexistent.toml")
	defaults := testDefaults(tmp)

	cfg, warnings, err := LoadFrom(path, defaults)
	if err != nil {
		t.Fatalf("LoadFrom returned error for missing file: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}
	if cfg != defaults {
		t.Errorf("LoadFrom with missing file returned non-default config")
	}
}

func TestLoadValidFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")

	content := `aws_region = "eu-west-1"
default_model = "anthropic.claude-sonnet-4-20250514-v1:0"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	defaults := testDefaults(tmp)
	cfg, warnings, err := LoadFrom(path, defaults)
	if err != nil {
		t.Fatalf("LoadFrom returned error: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings for valid keys, got %v", warnings)
	}

	if cfg.AWSRegion != "eu-west-1" {
		t.Errorf("AWSRegion = %q, want %q", cfg.AWSRegion, "eu-west-1")
	}
	if cfg.DefaultModel != "anthropic.claude-sonnet-4-20250514-v1:0" {
		t.Errorf("DefaultModel = %q, want %q", cfg.DefaultModel, "anthropic.claude-sonnet-4-20250514-v1:0")
	}
	// Non-overridden fields keep defaults.
	if cfg.AWSProfile != defaults.AWSProfile {
		t.Errorf("AWSProfile = %q, want default %q", cfg.AWSProfile, defaults.AWSProfile)
	}
	if cfg.SessionsDir != defaults.SessionsDir {
		t.Errorf("SessionsDir = %q, want default %q", cfg.SessionsDir, defaults.SessionsDir)
	}
	// Non-TOML fields preserved.
	if cfg.MaxToolTimeout != defaults.MaxToolTimeout {
		t.Errorf("MaxToolTimeout = %v, want %v", cfg.MaxToolTimeout, defaults.MaxToolTimeout)
	}
}

func TestLoadMalformedFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")

	if err := os.WriteFile(path, []byte("this is not [valid toml ="), 0644); err != nil {
		t.Fatal(err)
	}

	defaults := testDefaults(tmp)
	_, _, err := LoadFrom(path, defaults)
	if err == nil {
		t.Fatal("LoadFrom should return error for malformed TOML")
	}
}

func TestLoadUnknownKeys(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")

	content := `aws_region = "us-west-2"
aws_regoin = "typo"
defualt_model = "also-typo"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	defaults := testDefaults(tmp)
	cfg, warnings, err := LoadFrom(path, defaults)
	if err != nil {
		t.Fatalf("LoadFrom returned error: %v", err)
	}

	// Valid key should be applied.
	if cfg.AWSRegion != "us-west-2" {
		t.Errorf("AWSRegion = %q, want %q", cfg.AWSRegion, "us-west-2")
	}

	// Should have warnings for the two unknown keys.
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %d: %v", len(warnings), warnings)
	}
	// Verify the warnings mention the unknown keys.
	found := map[string]bool{"aws_regoin": false, "defualt_model": false}
	for _, w := range warnings {
		for key := range found {
			if len(w) > 0 && contains(w, key) {
				found[key] = true
			}
		}
	}
	for key, ok := range found {
		if !ok {
			t.Errorf("expected warning about %q, not found in %v", key, warnings)
		}
	}
}

func TestLoadCosmosDirOverride(t *testing.T) {
	tmp := t.TempDir()
	customDir := filepath.Join(tmp, "custom-cosmos")
	path := filepath.Join(tmp, "config.toml")

	content := `cosmos_dir = "` + customDir + `"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	defaults := testDefaults(tmp)
	cfg, _, err := LoadFrom(path, defaults)
	if err != nil {
		t.Fatalf("LoadFrom returned error: %v", err)
	}

	if cfg.CosmosDir != customDir {
		t.Errorf("CosmosDir = %q, want %q", cfg.CosmosDir, customDir)
	}
	// Sub-dirs should auto-adjust to new CosmosDir.
	wantSessions := filepath.Join(customDir, "sessions")
	if cfg.SessionsDir != wantSessions {
		t.Errorf("SessionsDir = %q, want %q", cfg.SessionsDir, wantSessions)
	}
	wantAgents := filepath.Join(customDir, "agents")
	if cfg.AgentsDir != wantAgents {
		t.Errorf("AgentsDir = %q, want %q", cfg.AgentsDir, wantAgents)
	}
}

func TestLoadExplicitSubDirs(t *testing.T) {
	tmp := t.TempDir()
	customDir := filepath.Join(tmp, "custom-cosmos")
	customSessions := filepath.Join(tmp, "my-sessions")
	path := filepath.Join(tmp, "config.toml")

	content := `cosmos_dir = "` + customDir + `"
sessions_dir = "` + customSessions + `"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	defaults := testDefaults(tmp)
	cfg, _, err := LoadFrom(path, defaults)
	if err != nil {
		t.Fatalf("LoadFrom returned error: %v", err)
	}

	// sessions_dir was explicitly set — should NOT be auto-adjusted.
	if cfg.SessionsDir != customSessions {
		t.Errorf("SessionsDir = %q, want %q", cfg.SessionsDir, customSessions)
	}
	// agents_dir was NOT set — should auto-adjust to new CosmosDir.
	wantAgents := filepath.Join(customDir, "agents")
	if cfg.AgentsDir != wantAgents {
		t.Errorf("AgentsDir = %q, want %q", cfg.AgentsDir, wantAgents)
	}
}

func TestEnsureDirs(t *testing.T) {
	tmp := t.TempDir()
	cfg := testDefaults(tmp)

	// First call creates directories.
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs failed: %v", err)
	}

	for _, dir := range []string{cfg.CosmosDir, cfg.SessionsDir, cfg.AgentsDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("directory %q not created: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", dir)
		}
	}

	// Second call is idempotent.
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs (idempotent) failed: %v", err)
	}
}

func TestEnsureDirsPermissions(t *testing.T) {
	tmp := t.TempDir()
	cfg := testDefaults(tmp)

	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs failed: %v", err)
	}

	for _, dir := range []string{cfg.CosmosDir, cfg.SessionsDir, cfg.AgentsDir} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat %q: %v", dir, err)
		}
		perm := info.Mode().Perm()
		if perm != 0700 {
			t.Errorf("directory %q has mode %o, want %o", dir, perm, 0700)
		}
	}
}

func TestConfigFilePath(t *testing.T) {
	tmp := t.TempDir()
	cfg := testDefaults(tmp)

	want := filepath.Join(cfg.CosmosDir, "config.toml")
	if got := cfg.ConfigFilePath(); got != want {
		t.Errorf("ConfigFilePath() = %q, want %q", got, want)
	}
}

// testDefaults returns a Config rooted in a temp directory instead of $HOME.
func testDefaults(tmpDir string) Config {
	cosmosDir := filepath.Join(tmpDir, ".cosmos")
	return Config{
		AWSRegion:      "us-east-1",
		AWSProfile:     "",
		DefaultModel:   "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
		CosmosDir:      cosmosDir,
		SessionsDir:    filepath.Join(cosmosDir, "sessions"),
		AgentsDir:      filepath.Join(cosmosDir, "agents"),
		AuditFile:      filepath.Join(".cosmos", "audit.jsonl"),
		PolicyFile:     filepath.Join(".cosmos", "policy.json"),
		MaxToolTimeout: 5 * time.Minute,
	}
}

// contains checks if s contains substr (simple helper to avoid strings import).
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
