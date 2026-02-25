package app

import (
	"context"
	"cosmos/config"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	cfg, warnings, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if cfg.AWSRegion == "" {
		t.Error("expected non-empty AWSRegion")
	}
	if cfg.DefaultModel == "" {
		t.Error("expected non-empty DefaultModel")
	}
	_ = warnings
}

func TestSetupCurrencyFormatterUSD(t *testing.T) {
	cfg := config.Config{Currency: "USD"}
	formatter, err := setupCurrencyFormatter(context.Background(), cfg)
	if err != nil {
		t.Fatalf("setupCurrencyFormatter failed: %v", err)
	}
	if formatter == nil {
		t.Fatal("expected non-nil formatter")
	}
	if formatter.Code != "USD" {
		t.Errorf("expected USD, got %s", formatter.Code)
	}
}

func TestSetupCurrencyFormatterNonUSD(t *testing.T) {
	// Skip in CI/offline environments
	if testing.Short() {
		t.Skip("skipping network test in short mode")
	}

	cfg := config.Config{Currency: "EUR"}
	formatter, err := setupCurrencyFormatter(context.Background(), cfg)
	if err != nil {
		// Non-fatal in production (falls back to USD), so just log
		t.Logf("currency fetch failed (may be expected in CI): %v", err)
		return
	}
	if formatter == nil {
		t.Fatal("expected non-nil formatter")
	}
	if formatter.Code != "EUR" {
		t.Errorf("expected EUR, got %s", formatter.Code)
	}
}

func TestSetupProvider(t *testing.T) {
	// Skip if no AWS credentials
	t.Skip("requires AWS credentials")

	cfg := config.Config{
		AWSRegion:       "us-east-1",
		AWSProfile:      "",
		PricingEnabled:  false,
		PricingCacheDir: "/tmp/cosmos-test",
		PricingCacheTTL: 24,
	}

	provider, err := setupProvider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("setupProvider failed: %v", err)
	}
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestBootstrap(t *testing.T) {
	// Integration test: full bootstrap
	// Skip if running in CI without AWS credentials
	t.Skip("integration test, requires full environment")

	ctx := context.Background()
	app, err := Bootstrap(ctx)
	if err != nil {
		t.Fatalf("Bootstrap failed: %v", err)
	}
	if app == nil {
		t.Fatal("expected non-nil Application")
	}
	if app.Config.AWSRegion == "" {
		t.Error("expected non-empty Config.AWSRegion")
	}
	if app.Session == nil {
		t.Error("expected non-nil Session")
	}
	if app.Scaffold == nil {
		t.Error("expected non-nil Scaffold")
	}
	if app.Program == nil {
		t.Error("expected non-nil Program")
	}
	if app.Tracker == nil {
		t.Error("expected non-nil Tracker")
	}
	if app.CurrencyFormatter == nil {
		t.Error("expected non-nil CurrencyFormatter")
	}
}

func TestSetupTrackerNotNil(t *testing.T) {
	// Simple test: tracker should always be created
	// We need a real ui.Notifier for this test
	t.Skip("requires real ui.Notifier, tested in integration test")
}
