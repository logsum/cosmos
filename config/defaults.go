package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

// Config holds all Cosmos configuration values.
type Config struct {
	AWSRegion    string `toml:"aws_region"`
	AWSProfile   string `toml:"aws_profile"`
	DefaultModel string `toml:"default_model"`

	CosmosDir   string `toml:"cosmos_dir"`
	SessionsDir string `toml:"sessions_dir"`
	AgentsDir   string `toml:"agents_dir"`

	// Pricing configuration
	PricingCacheDir string `toml:"pricing_cache_dir"`
	PricingCacheTTL int    `toml:"pricing_cache_ttl"`
	PricingEnabled  bool   `toml:"pricing_enabled"`

	// Display currency (ISO 4217 code). AWS pricing is always USD;
	// this controls the display currency with conversion via Frankfurter API.
	Currency string `toml:"currency"`

	// Permission timeout (seconds). How long to wait for user response to
	// permission prompts before applying the default decision.
	PermissionTimeout int `toml:"permission_timeout"`

	// Project-local paths — not TOML-configurable.
	// These are intentionally relative (to the project working directory).
	// They will be anchored to a discovered project root once that mechanism
	// exists (Phase 2: engine/policy). Until then, they resolve relative to CWD.
	AuditFile      string        `toml:"-"`
	PolicyFile     string        `toml:"-"`
	MaxToolTimeout time.Duration `toml:"-"`
}

// DefaultConfig returns a Config with all defaults populated.
func DefaultConfig() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	cosmosDir := filepath.Join(home, ".cosmos")

	return Config{
		AWSRegion:       "us-east-1",
		AWSProfile:      "",
		DefaultModel:    "us.anthropic.claude-3-5-sonnet-20241022-v2:0",
		CosmosDir:       cosmosDir,
		SessionsDir:     filepath.Join(cosmosDir, "sessions"),
		AgentsDir:       filepath.Join(cosmosDir, "agents"),
		PricingCacheDir: filepath.Join(cosmosDir, "cache", "pricing"),
		PricingCacheTTL:   168, // 1 week in hours
		PricingEnabled:    true,
		Currency:          "USD",
		PermissionTimeout: 30, // seconds
		// AuditFile documents the pattern - actual files are per-session: audit-<session-id>.jsonl
		AuditFile:       filepath.Join(".cosmos", "audit-{session-id}.jsonl"),
		PolicyFile:      filepath.Join(".cosmos", "policy.json"),
		MaxToolTimeout:  5 * time.Minute,
	}
}

// ConfigFilePath returns the path to the config file inside CosmosDir.
func (c Config) ConfigFilePath() string {
	return filepath.Join(c.CosmosDir, "config.toml")
}

// Load loads configuration from the default location (~/.cosmos/config.toml),
// falling back to defaults if the file does not exist.
// Warnings are returned for unrecognized TOML keys (likely typos).
func Load() (Config, []string, error) {
	defaults := DefaultConfig()
	return LoadFrom(defaults.ConfigFilePath(), defaults)
}

// LoadFrom loads configuration from the given path, overlaying TOML values
// onto the provided defaults. If the file does not exist, defaults are returned
// without error (first-run case). If the file exists but is malformed, an error
// is returned. Warnings are returned for unrecognized TOML keys.
func LoadFrom(path string, defaults Config) (Config, []string, error) {
	cfg := defaults

	meta, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		if os.IsNotExist(err) {
			return defaults, nil, nil
		}
		return Config{}, nil, fmt.Errorf("loading config %s: %w", path, err)
	}

	// If cosmos_dir was overridden but sub-dirs were not, re-derive them.
	if meta.IsDefined("cosmos_dir") {
		if !meta.IsDefined("sessions_dir") {
			cfg.SessionsDir = filepath.Join(cfg.CosmosDir, "sessions")
		}
		if !meta.IsDefined("agents_dir") {
			cfg.AgentsDir = filepath.Join(cfg.CosmosDir, "agents")
		}
		if !meta.IsDefined("pricing_cache_dir") {
			cfg.PricingCacheDir = filepath.Join(cfg.CosmosDir, "cache", "pricing")
		}
	}

	// Restore non-TOML fields from defaults.
	cfg.AuditFile = defaults.AuditFile
	cfg.PolicyFile = defaults.PolicyFile
	cfg.MaxToolTimeout = defaults.MaxToolTimeout

	// Warn about unrecognized keys — likely typos.
	var warnings []string
	for _, key := range meta.Undecoded() {
		warnings = append(warnings, fmt.Sprintf("unknown config key: %s", key))
	}

	return cfg, warnings, nil
}

// EnsureDirs creates CosmosDir, SessionsDir, AgentsDir, and PricingCacheDir if they do not exist.
func (c Config) EnsureDirs() error {
	for _, dir := range []string{c.CosmosDir, c.SessionsDir, c.AgentsDir, c.PricingCacheDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}
	return nil
}
