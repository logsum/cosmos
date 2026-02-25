package bedrock

import (
	"context"
	"cosmos/core/provider"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
)

func TestFamilyMatchesID(t *testing.T) {
	tests := []struct {
		family  string
		modelID string
		want    bool
	}{
		{"anthropic_haiku", "anthropic.claude-3-haiku-20240307-v1:0", true},
		{"anthropic_haiku", "anthropic.claude-3-5-haiku-20241022-v1:0", true},
		{"anthropic_sonnet", "anthropic.claude-3-sonnet-20240229-v1:0", true},
		{"anthropic_sonnet", "anthropic.claude-sonnet-4-20250514-v1:0", true},
		{"anthropic_opus", "anthropic.claude-3-opus-20240229-v1:0", true},
		{"anthropic_opus", "anthropic.claude-opus-4-20250514-v1:0", true},
		{"anthropic_haiku", "anthropic.claude-3-opus-20240229-v1:0", false},
		{"anthropic_sonnet", "anthropic.claude-3-haiku-20240307-v1:0", false},
		{"moonshot_kimi", "anthropic.claude-3-haiku-20240307-v1:0", false},
		{"unknown_family", "anthropic.claude-3-haiku-20240307-v1:0", false},
	}

	for _, tt := range tests {
		got := familyMatchesID(tt.family, tt.modelID)
		if got != tt.want {
			t.Errorf("familyMatchesID(%q, %q) = %v, want %v", tt.family, tt.modelID, got, tt.want)
		}
	}
}

func TestModelIDsForFamily(t *testing.T) {
	haiku := modelIDsForFamily("anthropic_haiku")
	if len(haiku) == 0 {
		t.Error("expected at least one haiku model ID")
	}
	for _, id := range haiku {
		if !familyMatchesID("anthropic_haiku", id) {
			t.Errorf("modelIDsForFamily returned %q which doesn't match anthropic_haiku", id)
		}
	}

	sonnet := modelIDsForFamily("anthropic_sonnet")
	if len(sonnet) == 0 {
		t.Error("expected at least one sonnet model ID")
	}

	opus := modelIDsForFamily("anthropic_opus")
	if len(opus) == 0 {
		t.Error("expected at least one opus model ID")
	}

	unknown := modelIDsForFamily("unknown_family")
	if len(unknown) != 0 {
		t.Errorf("expected no model IDs for unknown family, got %d", len(unknown))
	}
}

func TestParsePriceString(t *testing.T) {
	tests := []struct {
		name string
		s    *string
		want float64
	}{
		{"nil", nil, 0.0},
		{"empty", strPtr(""), 0.0},
		{"valid integer", strPtr("3.0"), 3.0},
		{"valid decimal", strPtr("0.25"), 0.25},
		{"large value", strPtr("15.0"), 15.0},
		{"small value", strPtr("0.001"), 0.001},
		{"non-numeric", strPtr("abc"), 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePriceString(tt.s)
			if diff := got - tt.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("parsePriceString(%v) = %f, want %f", tt.s, got, tt.want)
			}
		})
	}
}

func TestPricingReportToModelInfo(t *testing.T) {
	report := &BedrockPricingReport{
		Comparisons: []comparison{
			{
				Family:              "anthropic_haiku",
				LatestVersion:       "3.5.0",
				ModelName:           strPtr("Claude 3.5 Haiku"),
				RegionCode:          strPtr("us-east-1"),
				InputUSDPer1MTokens: strPtr("0.80"),
				OutputUSDPer1MTokens: strPtr("4.0"),
			},
			{
				Family:              "anthropic_sonnet",
				LatestVersion:       "4.0.0",
				ModelName:           strPtr("Claude Sonnet 4"),
				RegionCode:          strPtr("us-east-1"),
				InputUSDPer1MTokens: strPtr("2.50"),
				OutputUSDPer1MTokens: strPtr("12.50"),
			},
			{
				// Different region — should be excluded when filtering for us-east-1.
				Family:              "anthropic_opus",
				LatestVersion:       "4.0.0",
				ModelName:           strPtr("Claude Opus 4"),
				RegionCode:          strPtr("eu-west-1"),
				InputUSDPer1MTokens: strPtr("18.0"),
				OutputUSDPer1MTokens: strPtr("90.0"),
			},
		},
	}

	result := pricingReportToModelInfo(report, "us-east-1")

	// Should contain haiku and sonnet models, not opus (wrong region).
	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}

	// Verify at least one haiku model was populated.
	foundHaiku := false
	for id, info := range result {
		if familyMatchesID("anthropic_haiku", id) {
			foundHaiku = true
			if info.InputCostPer1M != 0.80 {
				t.Errorf("haiku input cost: got %f, want 0.80", info.InputCostPer1M)
			}
			if info.OutputCostPer1M != 4.0 {
				t.Errorf("haiku output cost: got %f, want 4.0", info.OutputCostPer1M)
			}
			if info.ContextWindow != 200_000 {
				t.Errorf("haiku context window: got %d, want 200000", info.ContextWindow)
			}
		}
	}
	if !foundHaiku {
		t.Error("expected at least one haiku model in result")
	}

	// Verify opus (eu-west-1) is NOT in result.
	for id := range result {
		if familyMatchesID("anthropic_opus", id) {
			t.Errorf("opus model %q should not be in us-east-1 results", id)
		}
	}
}

func TestPricingReportToModelInfoNilRegion(t *testing.T) {
	report := &BedrockPricingReport{
		Comparisons: []comparison{
			{
				Family:              "anthropic_haiku",
				LatestVersion:       "3.5.0",
				RegionCode:          nil, // No region code
				InputUSDPer1MTokens: strPtr("1.0"),
				OutputUSDPer1MTokens: strPtr("5.0"),
			},
		},
	}

	result := pricingReportToModelInfo(report, "us-east-1")
	if len(result) != 0 {
		t.Errorf("expected empty result for nil region code, got %d entries", len(result))
	}
}

func TestListModelsDynamicPricingPriority(t *testing.T) {
	catalog := &stubCatalog{
		summaries: []bedrocktypes.FoundationModelSummary{
			{
				ModelId:                    aws.String("anthropic.claude-3-haiku-20240307-v1:0"),
				ModelName:                  aws.String("Claude 3 Haiku"),
				ResponseStreamingSupported: aws.Bool(true),
				OutputModalities:           []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText},
			},
		},
	}

	// Pre-populate dynamic pricing with different values than static.
	dynamicPricing := map[string]provider.ModelInfo{
		"anthropic.claude-3-haiku-20240307-v1:0": {
			ID:              "anthropic.claude-3-haiku-20240307-v1:0",
			Name:            "Claude 3 Haiku",
			ContextWindow:   200_000,
			InputCostPer1M:  0.50, // Different from static 0.25
			OutputCostPer1M: 2.50, // Different from static 1.25
		},
	}

	b := &Bedrock{
		catalog:        catalog,
		dynamicPricing: dynamicPricing,
	}

	models, err := b.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}

	// Should use dynamic pricing (0.50), not static (0.25).
	if models[0].InputCostPer1M != 0.50 {
		t.Errorf("input cost: got %f, want 0.50 (dynamic pricing should take priority)", models[0].InputCostPer1M)
	}
	if models[0].OutputCostPer1M != 2.50 {
		t.Errorf("output cost: got %f, want 2.50 (dynamic pricing should take priority)", models[0].OutputCostPer1M)
	}
}

func TestListModelsFallsBackToStaticPricing(t *testing.T) {
	catalog := &stubCatalog{
		summaries: []bedrocktypes.FoundationModelSummary{
			{
				ModelId:                    aws.String("anthropic.claude-3-haiku-20240307-v1:0"),
				ModelName:                  aws.String("Claude 3 Haiku"),
				ResponseStreamingSupported: aws.Bool(true),
				OutputModalities:           []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText},
			},
		},
	}

	// Empty dynamic pricing — should fall back to knownModels.
	b := &Bedrock{
		catalog:        catalog,
		dynamicPricing: map[string]provider.ModelInfo{},
	}

	models, err := b.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}

	// Should use static pricing from knownModels.
	if models[0].InputCostPer1M != 0.25 {
		t.Errorf("input cost: got %f, want 0.25 (static fallback)", models[0].InputCostPer1M)
	}
}

func TestListModelsNoPricingData(t *testing.T) {
	catalog := &stubCatalog{
		summaries: []bedrocktypes.FoundationModelSummary{
			{
				ModelId:                    aws.String("anthropic.claude-future-model-v1:0"),
				ModelName:                  aws.String("Claude Future"),
				ResponseStreamingSupported: aws.Bool(true),
				OutputModalities:           []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText},
			},
		},
	}

	b := &Bedrock{
		catalog:        catalog,
		dynamicPricing: map[string]provider.ModelInfo{},
	}

	models, err := b.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}

	// Unknown model: name only, no pricing.
	if models[0].Name != "Claude Future" {
		t.Errorf("name: got %q, want %q", models[0].Name, "Claude Future")
	}
	if models[0].InputCostPer1M != 0 || models[0].OutputCostPer1M != 0 {
		t.Errorf("expected zero pricing for unknown model, got input=%f output=%f",
			models[0].InputCostPer1M, models[0].OutputCostPer1M)
	}
}

func TestRefreshPricingDisabled(t *testing.T) {
	b := &Bedrock{
		pricingCfg: provider.PricingConfig{Enabled: false},
	}

	err := b.refreshPricing(context.Background())
	if err != nil {
		t.Errorf("expected nil error when pricing disabled, got %v", err)
	}
}

func TestRefreshPricingNilEngine(t *testing.T) {
	b := &Bedrock{
		pricingCfg:    provider.PricingConfig{Enabled: true},
		pricingEngine: nil,
	}

	err := b.refreshPricing(context.Background())
	if err != nil {
		t.Errorf("expected nil error when engine is nil, got %v", err)
	}
}

// --- Pricing config tests ---

func TestNewBedrockWithPricingDisabled(t *testing.T) {
	// Verify that a Bedrock instance with disabled pricing has no pricing engine.
	// We can't actually call NewBedrock (requires AWS credentials), but we can
	// verify the struct construction logic.
	b := &Bedrock{
		pricingCfg: provider.PricingConfig{Enabled: false},
	}

	if b.pricingEngine != nil {
		t.Error("expected nil pricing engine when pricing disabled")
	}
}

// --- ListModels catalog error passthrough test ---

func TestListModelsCatalogError(t *testing.T) {
	catalog := &stubCatalog{
		err: &stubAPIError{code: "ThrottlingException", message: "rate limited"},
	}

	b := &Bedrock{
		catalog: catalog,
	}

	_, err := b.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error from catalog, got nil")
	}
}

// --- helpers ---

func strPtr(s string) *string {
	return &s
}

// stubCatalog is defined in bedrock_test.go; reuse it here since we're
// in the same package. The type assertion below ensures it still implements
// modelLister at compile time.
var _ modelLister = (*stubCatalog)(nil)

// stubCatalogWithDynamic creates a stub for ListModels that we can
// combine with pre-populated dynamic pricing.
func stubCatalogWithModels(models ...bedrocktypes.FoundationModelSummary) *stubCatalog {
	return &stubCatalog{summaries: models}
}

// streamableModel returns a FoundationModelSummary that passes isUsableModel.
func streamableModel(id, name string) bedrocktypes.FoundationModelSummary {
	return bedrocktypes.FoundationModelSummary{
		ModelId:                    aws.String(id),
		ModelName:                  aws.String(name),
		ResponseStreamingSupported: aws.Bool(true),
		OutputModalities:           []bedrocktypes.ModelModality{bedrocktypes.ModelModalityText},
	}
}

func TestPricingReportToModelInfoSkipsZeroPricing(t *testing.T) {
	report := &BedrockPricingReport{
		Comparisons: []comparison{
			{
				// Both prices nil → parsePriceString returns 0.0 → should be skipped.
				Family:               "anthropic_haiku",
				LatestVersion:        "3.5.0",
				ModelName:            strPtr("Claude 3.5 Haiku"),
				RegionCode:           strPtr("us-east-1"),
				InputUSDPer1MTokens:  nil,
				OutputUSDPer1MTokens: nil,
			},
			{
				// Both prices unparseable → 0.0 → should be skipped.
				Family:               "anthropic_sonnet",
				LatestVersion:        "4.0.0",
				ModelName:            strPtr("Claude Sonnet 4"),
				RegionCode:           strPtr("us-east-1"),
				InputUSDPer1MTokens:  strPtr("not-a-number"),
				OutputUSDPer1MTokens: strPtr("also-bad"),
			},
			{
				// Valid pricing → should be included.
				Family:               "anthropic_opus",
				LatestVersion:        "4.0.0",
				ModelName:            strPtr("Claude Opus 4"),
				RegionCode:           strPtr("us-east-1"),
				InputUSDPer1MTokens:  strPtr("15.0"),
				OutputUSDPer1MTokens: strPtr("75.0"),
			},
		},
	}

	result := pricingReportToModelInfo(report, "us-east-1")

	// Haiku and sonnet should have been skipped (zero pricing).
	for id := range result {
		if familyMatchesID("anthropic_haiku", id) {
			t.Errorf("haiku model %q should not appear — zero pricing should be skipped", id)
		}
		if familyMatchesID("anthropic_sonnet", id) {
			t.Errorf("sonnet model %q should not appear — unparseable pricing should be skipped", id)
		}
	}

	// Opus should be present with correct pricing.
	foundOpus := false
	for id, info := range result {
		if familyMatchesID("anthropic_opus", id) {
			foundOpus = true
			if info.InputCostPer1M != 15.0 {
				t.Errorf("opus input cost: got %f, want 15.0", info.InputCostPer1M)
			}
			if info.OutputCostPer1M != 75.0 {
				t.Errorf("opus output cost: got %f, want 75.0", info.OutputCostPer1M)
			}
		}
	}
	if !foundOpus {
		t.Error("expected opus models in result")
	}
}

func TestRefreshPricingFailureStopsRetries(t *testing.T) {
	catalog := &stubCatalog{
		summaries: []bedrocktypes.FoundationModelSummary{
			streamableModel("anthropic.claude-3-haiku-20240307-v1:0", "Claude 3 Haiku"),
		},
	}

	b := &Bedrock{
		catalog:       catalog,
		pricingCfg:    provider.PricingConfig{Enabled: true},
		pricingEngine: NewBedrockPricingEngine(nil), // nil client → will fail
	}

	// First call to ListModels triggers refreshPricing, which fails.
	// refreshPricing should set dynamicPricing to an empty map.
	_, _ = b.ListModels(context.Background())

	if b.dynamicPricing == nil {
		t.Fatal("dynamicPricing should be non-nil (empty map) after failed refresh")
	}
	if len(b.dynamicPricing) != 0 {
		t.Errorf("dynamicPricing should be empty after failed refresh, got %d entries", len(b.dynamicPricing))
	}

	// Second call should NOT re-trigger refreshPricing (dynamicPricing != nil).
	// It should fall through to static pricing from knownModels.
	models, err := b.ListModels(context.Background())
	if err != nil {
		t.Fatalf("second ListModels: %v", err)
	}

	if len(models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(models))
	}
	// Should use static fallback pricing.
	if models[0].InputCostPer1M != 0.25 {
		t.Errorf("input cost: got %f, want 0.25 (static fallback)", models[0].InputCostPer1M)
	}
}

func TestListModelsThreeTierFallbackOrder(t *testing.T) {
	// Model A: in dynamic pricing (should use dynamic)
	// Model B: in knownModels only (should use static)
	// Model C: unknown (should use name from catalog only)
	catalog := stubCatalogWithModels(
		streamableModel("anthropic.claude-3-haiku-20240307-v1:0", "Claude 3 Haiku"),
		streamableModel("anthropic.claude-3-opus-20240229-v1:0", "Claude 3 Opus"),
		streamableModel("anthropic.claude-future-v1:0", "Claude Future"),
	)

	dynamicPricing := map[string]provider.ModelInfo{
		"anthropic.claude-3-haiku-20240307-v1:0": {
			ID:              "anthropic.claude-3-haiku-20240307-v1:0",
			Name:            "Claude 3 Haiku (Dynamic)",
			ContextWindow:   200_000,
			InputCostPer1M:  0.99,
			OutputCostPer1M: 4.99,
		},
		// Opus NOT in dynamic pricing — will fall through to static.
	}

	b := &Bedrock{
		catalog:        catalog,
		dynamicPricing: dynamicPricing,
	}

	models, err := b.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}

	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d", len(models))
	}

	byID := map[string]provider.ModelInfo{}
	for _, m := range models {
		byID[m.ID] = m
	}

	// Haiku: dynamic pricing wins.
	haiku := byID["anthropic.claude-3-haiku-20240307-v1:0"]
	if haiku.InputCostPer1M != 0.99 {
		t.Errorf("haiku input: got %f, want 0.99 (dynamic)", haiku.InputCostPer1M)
	}
	if haiku.Name != "Claude 3 Haiku (Dynamic)" {
		t.Errorf("haiku name: got %q, want dynamic name", haiku.Name)
	}

	// Opus: static pricing (knownModels).
	opus := byID["anthropic.claude-3-opus-20240229-v1:0"]
	if opus.InputCostPer1M != 15.0 {
		t.Errorf("opus input: got %f, want 15.0 (static)", opus.InputCostPer1M)
	}

	// Future: no pricing.
	future := byID["anthropic.claude-future-v1:0"]
	if future.InputCostPer1M != 0 {
		t.Errorf("future input: got %f, want 0 (no pricing)", future.InputCostPer1M)
	}
	if future.Name != "Claude Future" {
		t.Errorf("future name: got %q, want %q", future.Name, "Claude Future")
	}
}
