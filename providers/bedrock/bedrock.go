package bedrock

import (
	"context"
	"cosmos/core/provider"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrock/types"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/smithy-go"
)

// knownModels holds static metadata for Claude models on Bedrock.
// The ListFoundationModels API does not return context windows or pricing,
// so we maintain a static table for known models.
var knownModels = map[string]provider.ModelInfo{
	"anthropic.claude-3-haiku-20240307-v1:0": {
		ID: "anthropic.claude-3-haiku-20240307-v1:0", Name: "Claude 3 Haiku",
		ContextWindow: 200_000, InputCostPer1M: 0.25, OutputCostPer1M: 1.25,
	},
	"anthropic.claude-3-sonnet-20240229-v1:0": {
		ID: "anthropic.claude-3-sonnet-20240229-v1:0", Name: "Claude 3 Sonnet",
		ContextWindow: 200_000, InputCostPer1M: 3.0, OutputCostPer1M: 15.0,
	},
	"anthropic.claude-3-opus-20240229-v1:0": {
		ID: "anthropic.claude-3-opus-20240229-v1:0", Name: "Claude 3 Opus",
		ContextWindow: 200_000, InputCostPer1M: 15.0, OutputCostPer1M: 75.0,
	},
	"anthropic.claude-3-5-sonnet-20240620-v1:0": {
		ID: "anthropic.claude-3-5-sonnet-20240620-v1:0", Name: "Claude 3.5 Sonnet",
		ContextWindow: 200_000, InputCostPer1M: 3.0, OutputCostPer1M: 15.0,
	},
	"anthropic.claude-3-5-sonnet-20241022-v2:0": {
		ID: "anthropic.claude-3-5-sonnet-20241022-v2:0", Name: "Claude 3.5 Sonnet v2",
		ContextWindow: 200_000, InputCostPer1M: 3.0, OutputCostPer1M: 15.0,
	},
	"anthropic.claude-3-5-haiku-20241022-v1:0": {
		ID: "anthropic.claude-3-5-haiku-20241022-v1:0", Name: "Claude 3.5 Haiku",
		ContextWindow: 200_000, InputCostPer1M: 1.0, OutputCostPer1M: 5.0,
	},
	"anthropic.claude-sonnet-4-20250514-v1:0": {
		ID: "anthropic.claude-sonnet-4-20250514-v1:0", Name: "Claude Sonnet 4",
		ContextWindow: 200_000, InputCostPer1M: 3.0, OutputCostPer1M: 15.0,
	},
	"anthropic.claude-opus-4-20250514-v1:0": {
		ID: "anthropic.claude-opus-4-20250514-v1:0", Name: "Claude Opus 4",
		ContextWindow: 200_000, InputCostPer1M: 15.0, OutputCostPer1M: 75.0,
	},
}

// modelLister is the subset of bedrock.Client used for model discovery.
// Defined as an interface for testability.
type modelLister interface {
	ListFoundationModels(ctx context.Context, params *bedrock.ListFoundationModelsInput, optFns ...func(*bedrock.Options)) (*bedrock.ListFoundationModelsOutput, error)
}

// Bedrock implements Provider using AWS Bedrock's ConverseStream API.
type Bedrock struct {
	runtime        *bedrockruntime.Client
	catalog        modelLister
	pricingEngine  *BedrockPricingEngine
	dynamicPricing map[string]provider.ModelInfo // populated lazily from AWS Pricing API
	region         string
	pricingCfg     provider.PricingConfig
}

// NewBedrock creates a Bedrock provider configured for the given AWS region.
// If profile is non-empty, it is used to select a named AWS credentials profile.
// If pricingCfg.Enabled is true, pricing is fetched dynamically from the AWS Pricing API.
func NewBedrock(ctx context.Context, region, profile string, pricingCfg provider.PricingConfig) (*Bedrock, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(region),
	}
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}

	b := &Bedrock{
		runtime:    bedrockruntime.NewFromConfig(awsCfg),
		catalog:    bedrock.NewFromConfig(awsCfg),
		region:     region,
		pricingCfg: pricingCfg,
	}

	// Initialize pricing engine (non-fatal if fails)
	if pricingCfg.Enabled {
		pricingOpts := []func(*awsconfig.LoadOptions) error{
			awsconfig.WithRegion("us-east-1"), // Pricing API is us-east-1 only
		}
		if profile != "" {
			pricingOpts = append(pricingOpts, awsconfig.WithSharedConfigProfile(profile))
		}

		pricingAwsCfg, err := awsconfig.LoadDefaultConfig(ctx, pricingOpts...)
		if err == nil {
			b.pricingEngine = NewBedrockPricingEngine(awspricing.NewFromConfig(pricingAwsCfg))
		}
		// Errors here are non-fatal: will use static pricing
	}

	return b, nil
}

// Send starts a streaming conversation with the model specified in req.
func (b *Bedrock) Send(ctx context.Context, req provider.Request) (provider.StreamIterator, error) {
	input, err := buildConverseStreamInput(req)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	out, err := b.runtime.ConverseStream(ctx, input)
	if err != nil {
		return nil, classifyErr(err)
	}

	stream := out.GetStream()
	return &bedrockIterator{
		stream: stream,
		events: stream.Events(),
	}, nil
}

// ListModels returns available Anthropic models from the Bedrock catalog,
// enriched with pricing metadata (dynamic or static fallback).
func (b *Bedrock) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	// Lazy pricing fetch on first call
	if b.pricingEngine != nil && b.dynamicPricing == nil {
		_ = b.refreshPricing(ctx) // Non-fatal, ignore errors
	}

	// Fetch catalog from Bedrock
	out, err := b.catalog.ListFoundationModels(ctx, &bedrock.ListFoundationModelsInput{
		ByProvider: aws.String("Anthropic"),
	})
	if err != nil {
		return nil, classifyErr(err)
	}

	// Enrich with pricing: dynamic → static → none
	var models []provider.ModelInfo
	for _, summary := range out.ModelSummaries {
		if !isUsableModel(summary) {
			continue
		}

		id := aws.ToString(summary.ModelId)

		// Priority 1: Dynamic pricing from cache
		if info, ok := b.dynamicPricing[id]; ok {
			models = append(models, info)
			continue
		}

		// Priority 2: Static pricing from knownModels
		if known, ok := knownModels[id]; ok {
			models = append(models, known)
			continue
		}

		// Priority 3: No pricing data
		models = append(models, provider.ModelInfo{
			ID:   id,
			Name: aws.ToString(summary.ModelName),
		})
	}

	return models, nil
}

// isUsableModel returns true if the model supports on-demand text streaming.
func isUsableModel(s bedrocktypes.FoundationModelSummary) bool {
	if s.ResponseStreamingSupported == nil || !*s.ResponseStreamingSupported {
		return false
	}
	return slices.Contains(s.OutputModalities, bedrocktypes.ModelModalityText)
}

// classifyErr wraps AWS API errors into provider-level sentinels.
func classifyErr(err error) error {
	if err == nil {
		return nil
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "ThrottlingException":
			return fmt.Errorf("%w: %s", provider.ErrThrottled, apiErr.ErrorMessage())
		case "AccessDeniedException":
			return fmt.Errorf("%w: %s", provider.ErrAccessDenied, apiErr.ErrorMessage())
		case "ResourceNotFoundException", "ModelNotFoundException":
			return fmt.Errorf("%w: %s", provider.ErrModelNotFound, apiErr.ErrorMessage())
		case "ModelNotReadyException":
			return fmt.Errorf("%w: %s", provider.ErrModelNotReady, apiErr.ErrorMessage())
		case "ValidationException":
			return fmt.Errorf("bedrock validation: %s: %w", apiErr.ErrorMessage(), err)
		}
	}

	return fmt.Errorf("bedrock: %w", err)
}

// refreshPricing fetches pricing from AWS and populates the dynamic pricing map.
// Returns an error if fetching fails, but errors are non-fatal in callers.
func (b *Bedrock) refreshPricing(ctx context.Context) error {
	if !b.pricingCfg.Enabled || b.pricingEngine == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	opts := BedrockPricingOptions{
		CacheDir: b.pricingCfg.CacheDir,
		CacheTTL: b.pricingCfg.CacheTTL,
	}

	report, err := b.pricingEngine.GenerateBedrockPricingReport(ctx, opts)
	if err != nil {
		b.dynamicPricing = make(map[string]provider.ModelInfo) // Prevent retry loop
		return fmt.Errorf("fetching pricing: %w", err)
	}

	b.dynamicPricing = pricingReportToModelInfo(report, b.region)
	return nil
}

// Compile-time check that Bedrock implements provider.Provider
var _ provider.Provider = (*Bedrock)(nil)
