package bedrock

import (
	"context"
	"cosmos/core/provider"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awspricing "github.com/aws/aws-sdk-go-v2/service/pricing"
	"github.com/aws/aws-sdk-go-v2/service/pricing/types"
)

var serviceCodes = []string{"AmazonBedrockFoundationModels", "AmazonBedrock"}

var families = []string{
	"anthropic_haiku",
	"anthropic_sonnet",
	"anthropic_opus",
	"moonshot_kimi",
}

var tierRank = map[string]int{
	"standard": 0,
	"priority": 1,
	"flex":     2,
	"batch":    3,
	"cache":    4,
	"reserved": 5,
	"other":    6,
}

var nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)
var anthropicProviderRe = regexp.MustCompile(`\b(?:anthropic|claude)\b`)
var inputTokenRe = regexp.MustCompile(`\binput[-_ ]tokens?\b`)
var outputTokenRe = regexp.MustCompile(`\boutput[-_ ]tokens?\b`)
var versionNumberRe = regexp.MustCompile(`\d+`)

// familyRegexRules keeps pattern matching extensible and less brittle than substring checks.
var familyRegexRules = []struct {
	family   string
	patterns []*regexp.Regexp
}{
	{
		family: "moonshot_kimi",
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`\b(?:moonshot\b.*\bkimi|\bkimi\b.*\bmoonshot)\b`),
			regexp.MustCompile(`\bkimi(?:[-_ ]?\d+(?:\.\d+)*)?\b`),
		},
	},
	{family: "anthropic_haiku", patterns: []*regexp.Regexp{regexp.MustCompile(`\bhaiku\b`)}},
	{family: "anthropic_sonnet", patterns: []*regexp.Regexp{regexp.MustCompile(`\bsonnet\b`)}},
	{family: "anthropic_opus", patterns: []*regexp.Regexp{regexp.MustCompile(`\bopus\b`)}},
}

type pricingItem struct {
	Product struct {
		Attributes map[string]string `json:"attributes"`
	} `json:"product"`
	Terms struct {
		OnDemand map[string]termBody `json:"OnDemand"`
	} `json:"terms"`
}

type termBody struct {
	EffectiveDate   string                    `json:"effectiveDate"`
	PriceDimensions map[string]priceDimension `json:"priceDimensions"`
}

type priceDimension struct {
	PricePerUnit map[string]string `json:"pricePerUnit"`
	Unit         string            `json:"unit"`
	Description  string            `json:"description"`
}

type row struct {
	Family        string
	ServiceCode   string
	ModelName     string
	VersionTuple  []int
	Version       string
	Location      string
	RegionCode    string
	UsageType     string
	IOType        string
	Tier          string
	IsGlobal      bool
	IsLongContext bool
	USD           string
	Unit          string
	Description   string
	EffectiveDate string
	RateCode      string
}

type comparison struct {
	Family                   string  `json:"family"`
	LatestVersion            string  `json:"latest_version"`
	ModelName                *string `json:"model_name"`
	Location                 *string `json:"location"`
	RegionCode               *string `json:"region_code"`
	InputUSDPerUnit          *string `json:"input_usd_per_unit"`
	OutputUSDPerUnit         *string `json:"output_usd_per_unit"`
	DifferenceUSDPerUnit     *string `json:"difference_usd_per_unit"`
	InputUSDPer1MTokens      *string `json:"input_usd_per_1m_tokens"`
	OutputUSDPer1MTokens     *string `json:"output_usd_per_1m_tokens"`
	DifferenceUSDPer1MTokens *string `json:"difference_usd_per_1m_tokens"`
	Multiplier               *string `json:"multiplier"`
	OutputEstimatedWithX5    bool    `json:"output_estimated_with_x5"`
	InputUnit                *string `json:"input_unit"`
	InputTier                *string `json:"input_tier"`
	OutputTier               *string `json:"output_tier"`
	InputIsGlobal            *bool   `json:"input_is_global"`
	OutputIsGlobal           *bool   `json:"output_is_global"`
	InputUsageType           *string `json:"input_usagetype"`
	OutputUsageType          *string `json:"output_usagetype"`
}

// BedrockPricingOptions holds configuration for pricing fetch operations.
type BedrockPricingOptions struct {
	Location     string
	MaxPages     int
	Debug        bool
	CacheDir     string
	CacheTTL     int  // Check interval in hours (how often to check for updates)
	ForceRefresh bool // Force refresh even if cache is valid
}

// BedrockPricingReport contains pricing data organized by model family and region.
type BedrockPricingReport struct {
	SelectedLatestVersions map[string]*string         `json:"selected_latest_versions"`
	Comparisons            []comparison `json:"comparisons"`
	FallbackRule           string                     `json:"fallback_rule"`
	SourceServiceCodes     []string                   `json:"source_service_codes"`
}

// BedrockPricingEngine fetches and processes AWS Bedrock pricing data.
type BedrockPricingEngine struct {
	pricingClient *awspricing.Client
}

type cacheMetadata struct {
	FetchedAt        time.Time `json:"fetched_at"`
	ServiceCode      string    `json:"service_code"`
	LocationHash     string    `json:"location_hash"`
	MaxEffectiveDate time.Time `json:"max_effective_date"`
	LastCheckedAt    time.Time `json:"last_checked_at"`
}

type cachedData struct {
	Metadata cacheMetadata `json:"metadata"`
	Rows     []row         `json:"rows"`
}

// NewBedrockPricingEngine creates a pricing engine with the given AWS Pricing client.
func NewBedrockPricingEngine(pricingClient *awspricing.Client) *BedrockPricingEngine {
	return &BedrockPricingEngine{pricingClient: pricingClient}
}

func getCacheFilePath(cacheDir, serviceCode, locationFilter string) string {
	hash := sha256.Sum256([]byte(locationFilter))
	hashStr := fmt.Sprintf("%x", hash[:8])
	filename := fmt.Sprintf("%s_%s.json", serviceCode, hashStr)
	return filepath.Join(cacheDir, filename)
}

func readCache(filePath string) (*cachedData, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var cache cachedData
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}

	return &cache, nil
}

func getMaxEffectiveDate(rows []row) time.Time {
	var maxDate time.Time
	for _, r := range rows {
		if r.EffectiveDate == "" {
			continue
		}
		effectiveDate, err := time.Parse(time.RFC3339, r.EffectiveDate)
		if err != nil {
			continue
		}
		if effectiveDate.After(maxDate) {
			maxDate = effectiveDate
		}
	}
	return maxDate
}

func writeCache(filePath string, serviceCode, locationFilter string, rows []row) error {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	hash := sha256.Sum256([]byte(locationFilter))
	hashStr := fmt.Sprintf("%x", hash[:8])

	now := time.Now()
	cache := cachedData{
		Metadata: cacheMetadata{
			FetchedAt:        now,
			ServiceCode:      serviceCode,
			LocationHash:     hashStr,
			MaxEffectiveDate: getMaxEffectiveDate(rows),
			LastCheckedAt:    now,
		},
		Rows: rows,
	}

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0644)
}

func shouldCheckForUpdates(cache *cachedData, cacheTTL int) bool {
	if cache == nil || cacheTTL <= 0 {
		return false
	}
	age := time.Since(cache.Metadata.LastCheckedAt)
	return age.Hours() >= float64(cacheTTL)
}

func quickCheckForNewerPrices(ctx context.Context, pricingClient *awspricing.Client, serviceCode, locationFilter string, cachedMaxDate time.Time) (bool, error) {
	filters := make([]types.Filter, 0)
	if locationFilter != "" {
		filters = append(filters, types.Filter{
			Type:  types.FilterTypeTermMatch,
			Field: aws.String("location"),
			Value: aws.String(locationFilter),
		})
	}

	input := &awspricing.GetProductsInput{
		ServiceCode:   aws.String(serviceCode),
		FormatVersion: aws.String("aws_v1"),
		MaxResults:    aws.Int32(100),
	}
	if len(filters) > 0 {
		input.Filters = filters
	}

	output, err := pricingClient.GetProducts(ctx, input)
	if err != nil {
		return false, err
	}

	// Check if any items have newer effective dates
	for _, raw := range output.PriceList {
		var item pricingItem
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			continue
		}

		for _, term := range item.Terms.OnDemand {
			effectiveDate, err := time.Parse(time.RFC3339, term.EffectiveDate)
			if err != nil {
				continue
			}
			if effectiveDate.After(cachedMaxDate) {
				return true, nil
			}
		}
	}

	return false, nil
}

func updateCacheTimestamp(filePath string) error {
	cache, err := readCache(filePath)
	if err != nil {
		return err
	}

	cache.Metadata.LastCheckedAt = time.Now()

	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, data, 0644)
}

// GenerateBedrockPricingReport fetches and processes Bedrock pricing data from AWS.
func (e *BedrockPricingEngine) GenerateBedrockPricingReport(
	ctx context.Context,
	opts BedrockPricingOptions,
) (*BedrockPricingReport, error) {
	if e == nil || e.pricingClient == nil {
		return nil, fmt.Errorf("pricing client is required")
	}
	rows, err := fetchRows(ctx, e.pricingClient, opts.Location, opts.MaxPages, opts.CacheDir, opts.CacheTTL, opts.ForceRefresh)
	if err != nil {
		return nil, err
	}

	latestVersions, comparisons := buildReport(rows)
	return &BedrockPricingReport{
		SelectedLatestVersions: latestVersions,
		Comparisons:            comparisons,
		FallbackRule:           "If output is missing, output_usd_per_unit = input_usd_per_unit * 5.",
		SourceServiceCodes:     append([]string(nil), serviceCodes...),
	}, nil
}

func normalizeBlob(parts ...string) string {
	joined := strings.ToLower(strings.Join(parts, " "))
	return strings.TrimSpace(nonAlnumRe.ReplaceAllString(joined, " "))
}

func detectFamily(serviceCode, modelName, usageType string) string {
	blob := normalizeBlob(serviceCode, modelName, usageType)
	hasAnthropicHint := anthropicProviderRe.MatchString(blob)

	for _, rule := range familyRegexRules {
		for _, pattern := range rule.patterns {
			if !pattern.MatchString(blob) {
				continue
			}
			if strings.HasPrefix(rule.family, "anthropic_") && hasAnthropicHint {
				return rule.family
			}
			if rule.family == "moonshot_kimi" {
				return rule.family
			}
		}
	}

	// Fallback for future Anthropic naming changes where "claude" might disappear.
	for _, rule := range familyRegexRules {
		if !strings.HasPrefix(rule.family, "anthropic_") {
			continue
		}
		for _, pattern := range rule.patterns {
			if pattern.MatchString(blob) {
				return rule.family
			}
		}
	}

	return ""
}

func detectIOType(attributes map[string]string, usageType, description string) string {
	blob := strings.ToLower(strings.Join([]string{attributes["inferenceType"], usageType, description}, " "))
	if strings.Contains(blob, "inputtokencount") || inputTokenRe.MatchString(blob) {
		return "input"
	}
	if strings.Contains(blob, "outputtokencount") || outputTokenRe.MatchString(blob) {
		return "output"
	}
	if strings.Contains(blob, "input") && strings.Contains(blob, "token") {
		return "input"
	}
	if strings.Contains(blob, "output") && strings.Contains(blob, "token") {
		return "output"
	}
	return ""
}

func detectTier(attributes map[string]string, usageType, description string) string {
	blob := strings.ToLower(strings.Join([]string{attributes["inferenceType"], usageType, description}, " "))
	switch {
	case strings.Contains(blob, "reserved"):
		return "reserved"
	case strings.Contains(blob, "cache"):
		return "cache"
	case strings.Contains(blob, "batch"):
		return "batch"
	case strings.Contains(blob, "priority"):
		return "priority"
	case strings.Contains(blob, "flex"):
		return "flex"
	default:
		return "standard"
	}
}

func isLongContext(modelName, usageType, description string) bool {
	blob := strings.ToLower(strings.Join([]string{modelName, usageType, description}, " "))
	return strings.Contains(blob, "long context") || strings.Contains(blob, "lctx")
}

func modelNameFromAttributes(serviceCode string, attributes map[string]string) string {
	if serviceCode == "AmazonBedrockFoundationModels" {
		return attributes["servicename"]
	}
	if model := attributes["model"]; model != "" {
		return model
	}
	return attributes["servicename"]
}

func parseVersionTuple(modelName string) []int {
	parts := versionNumberRe.FindAllString(modelName, -1)
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

func versionToString(version []int) string {
	if len(version) == 0 {
		return ""
	}
	parts := make([]string, 0, len(version))
	for _, n := range version {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ".")
}

func compareVersion(a, b []int) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for i := 0; i < limit; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

func equalVersion(a, b []int) bool {
	return compareVersion(a, b) == 0
}

func parseRat(text string) *big.Rat {
	s := strings.TrimSpace(text)
	if s == "" {
		return nil
	}
	r := new(big.Rat)
	if _, ok := r.SetString(s); !ok {
		return nil
	}
	return r
}

func formatRat(r *big.Rat) *string {
	if r == nil {
		return nil
	}
	var s string
	if r.IsInt() {
		s = r.Num().String() + ".0"
	} else {
		fracDigits := 30
		if !isTerminatingDecimal(r) {
			absNum := new(big.Int).Abs(r.Num())
			intPart := new(big.Int).Quo(absNum, r.Denom())
			if intPart.Sign() > 0 {
				// Match Python Decimal's default ~28 significant digits for repeating values.
				fracDigits = 28 - len(intPart.String())
				if fracDigits < 1 {
					fracDigits = 1
				}
			}
		}
		s = r.FloatString(fracDigits)
		s = strings.TrimRight(s, "0")
		if strings.HasSuffix(s, ".") {
			s += "0"
		}
		if !strings.Contains(s, ".") {
			s += ".0"
		}
	}
	return &s
}

func isTerminatingDecimal(r *big.Rat) bool {
	if r == nil || r.Denom() == nil {
		return false
	}
	d := new(big.Int).Set(r.Denom())
	two := big.NewInt(2)
	five := big.NewInt(5)
	zero := big.NewInt(0)

	for {
		quot, rem := new(big.Int).QuoRem(d, two, new(big.Int))
		if rem.Cmp(zero) != 0 {
			break
		}
		d = quot
	}
	for {
		quot, rem := new(big.Int).QuoRem(d, five, new(big.Int))
		if rem.Cmp(zero) != 0 {
			break
		}
		d = quot
	}
	return d.Cmp(big.NewInt(1)) == 0
}

func tokenScaleTo1M(unit, description string) *big.Rat {
	blob := strings.ToLower(unit + " " + description)
	switch {
	case (strings.Contains(blob, "million") && strings.Contains(blob, "token")) ||
		(strings.Contains(blob, "1m") && strings.Contains(blob, "token")):
		return big.NewRat(1, 1)
	case (strings.Contains(blob, "thousand") && strings.Contains(blob, "token")) ||
		(strings.Contains(blob, "1k") && strings.Contains(blob, "token")):
		return big.NewRat(1000, 1)
	case strings.Contains(blob, "per token") && strings.Contains(blob, "token"):
		return big.NewRat(1000000, 1)
	default:
		return nil
	}
}

func ratMul(a, b *big.Rat) *big.Rat {
	if a == nil || b == nil {
		return nil
	}
	return new(big.Rat).Mul(a, b)
}

func ratSub(a, b *big.Rat) *big.Rat {
	if a == nil || b == nil {
		return nil
	}
	return new(big.Rat).Sub(a, b)
}

func ratDiv(a, b *big.Rat) *big.Rat {
	if a == nil || b == nil || b.Sign() == 0 {
		return nil
	}
	return new(big.Rat).Quo(a, b)
}

func extractRowsFromItem(item pricingItem, serviceCode string) []row {
	attributes := item.Product.Attributes
	modelName := modelNameFromAttributes(serviceCode, attributes)
	usageType := attributes["usagetype"]
	family := detectFamily(serviceCode, modelName, usageType)
	if family == "" {
		return nil
	}

	versionTuple := parseVersionTuple(modelName)
	version := versionToString(versionTuple)

	rows := make([]row, 0)
	for _, term := range item.Terms.OnDemand {
		effectiveDate := term.EffectiveDate
		for rateCode, priceDim := range term.PriceDimensions {
			usd := strings.TrimSpace(priceDim.PricePerUnit["USD"])
			if usd == "" {
				continue
			}
			description := priceDim.Description
			ioType := detectIOType(attributes, usageType, description)
			if ioType == "" {
				continue
			}
			rows = append(rows, row{
				Family:        family,
				ServiceCode:   serviceCode,
				ModelName:     modelName,
				VersionTuple:  append([]int(nil), versionTuple...),
				Version:       version,
				Location:      attributes["location"],
				RegionCode:    attributes["regionCode"],
				UsageType:     usageType,
				IOType:        ioType,
				Tier:          detectTier(attributes, usageType, description),
				IsGlobal:      strings.Contains(strings.ToLower(usageType), "global"),
				IsLongContext: isLongContext(modelName, usageType, description),
				USD:           usd,
				Unit:          priceDim.Unit,
				Description:   description,
				EffectiveDate: effectiveDate,
				RateCode:      rateCode,
			})
		}
	}
	return rows
}

func fetchRowsForServiceCode(ctx context.Context, pricingClient *awspricing.Client, serviceCode, locationFilter string, maxPages int, cacheDir string, cacheTTL int, forceRefresh bool) ([]row, error) {
	// Try to use cache if enabled
	if cacheDir != "" && cacheTTL > 0 && !forceRefresh {
		cacheFile := getCacheFilePath(cacheDir, serviceCode, locationFilter)
		cache, err := readCache(cacheFile)

		if err == nil {
			// Check if we need to verify for updates
			if shouldCheckForUpdates(cache, cacheTTL) {
				// Do quick check to see if there are newer prices
				hasNewerPrices, checkErr := quickCheckForNewerPrices(ctx, pricingClient, serviceCode, locationFilter, cache.Metadata.MaxEffectiveDate)
				if checkErr != nil {
					return cache.Rows, nil
				}

				if !hasNewerPrices {
					// Update the last checked timestamp
					updateCacheTimestamp(cacheFile)
					return cache.Rows, nil
				}
				// Fall through to fetch new data
			} else {
				// Within check interval, use cache
				return cache.Rows, nil
			}
		}
	}

	// Fetch from API
	rows := make([]row, 0)
	filters := make([]types.Filter, 0)
	if locationFilter != "" {
		filters = append(filters, types.Filter{
			Type:  types.FilterTypeTermMatch,
			Field: aws.String("location"),
			Value: aws.String(locationFilter),
		})
	}

	pages := 0
	var nextToken *string
	for {
		input := &awspricing.GetProductsInput{
			ServiceCode:   aws.String(serviceCode),
			FormatVersion: aws.String("aws_v1"),
			MaxResults:    aws.Int32(100),
		}
		if len(filters) > 0 {
			input.Filters = filters
		}
		if nextToken != nil {
			input.NextToken = nextToken
		}

		output, err := pricingClient.GetProducts(ctx, input)
		if err != nil {
			return nil, err
		}
		pages++

		for _, raw := range output.PriceList {
			var item pricingItem
			if err := json.Unmarshal([]byte(raw), &item); err != nil {
				return nil, fmt.Errorf("failed to parse AWS pricing response: %w", err)
			}
			rows = append(rows, extractRowsFromItem(item, serviceCode)...)
		}

		if maxPages > 0 && pages >= maxPages {
			break
		}
		if output.NextToken == nil || *output.NextToken == "" {
			break
		}
		nextToken = output.NextToken
	}

	// Write to cache if enabled
	if cacheDir != "" {
		cacheFile := getCacheFilePath(cacheDir, serviceCode, locationFilter)
		writeCache(cacheFile, serviceCode, locationFilter, rows)
	}

	return rows, nil
}

func fetchRows(ctx context.Context, pricingClient *awspricing.Client, locationFilter string, maxPages int, cacheDir string, cacheTTL int, forceRefresh bool) ([]row, error) {
	type fetchResult struct {
		rows []row
		err  error
	}

	results := make(chan fetchResult, len(serviceCodes))

	// Fetch all service codes in parallel
	for _, serviceCode := range serviceCodes {
		go func(sc string) {
			rows, err := fetchRowsForServiceCode(ctx, pricingClient, sc, locationFilter, maxPages, cacheDir, cacheTTL, forceRefresh)
			results <- fetchResult{rows: rows, err: err}
		}(serviceCode)
	}

	// Collect results from all goroutines
	allRows := make([]row, 0)
	for i := 0; i < len(serviceCodes); i++ {
		result := <-results
		if result.err != nil {
			return nil, result.err
		}
		allRows = append(allRows, result.rows...)
	}

	deduped := make([]row, 0, len(allRows))
	seen := map[string]struct{}{}
	for _, r := range allRows {
		key := strings.Join([]string{
			r.Family,
			r.ModelName,
			r.Location,
			r.UsageType,
			r.IOType,
			r.Tier,
			strconv.FormatBool(r.IsGlobal),
			strconv.FormatBool(r.IsLongContext),
			r.RateCode,
			r.USD,
		}, "\x1f")
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, r)
	}

	return deduped, nil
}

func selectLatestVersions(rows []row) map[string][]int {
	latest := map[string][]int{}
	for _, family := range families {
		latest[family] = nil
	}
	for _, r := range rows {
		if len(r.VersionTuple) == 0 {
			continue
		}
		current := latest[r.Family]
		if current == nil || compareVersion(r.VersionTuple, current) > 0 {
			latest[r.Family] = append([]int(nil), r.VersionTuple...)
		}
	}
	return latest
}

func compareRows(a, b row) int {
	aTier := 99
	if v, ok := tierRank[a.Tier]; ok {
		aTier = v
	}
	bTier := 99
	if v, ok := tierRank[b.Tier]; ok {
		bTier = v
	}
	if aTier != bTier {
		if aTier < bTier {
			return -1
		}
		return 1
	}

	aGlobal := 0
	if a.IsGlobal {
		aGlobal = 1
	}
	bGlobal := 0
	if b.IsGlobal {
		bGlobal = 1
	}
	if aGlobal != bGlobal {
		if aGlobal < bGlobal {
			return -1
		}
		return 1
	}

	aLctx := 0
	if a.IsLongContext {
		aLctx = 1
	}
	bLctx := 0
	if b.IsLongContext {
		bLctx = 1
	}
	if aLctx != bLctx {
		if aLctx < bLctx {
			return -1
		}
		return 1
	}

	aPrice := parseRat(a.USD)
	bPrice := parseRat(b.USD)
	if aPrice == nil && bPrice != nil {
		return 1
	}
	if aPrice != nil && bPrice == nil {
		return -1
	}
	if aPrice != nil && bPrice != nil {
		cmp := aPrice.Cmp(bPrice)
		if cmp != 0 {
			return cmp
		}
	}
	return 0
}

func preferredRow(rows []row) *row {
	if len(rows) == 0 {
		return nil
	}
	best := rows[0]
	for i := 1; i < len(rows); i++ {
		if compareRows(rows[i], best) < 0 {
			best = rows[i]
		}
	}
	out := best
	return &out
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

func boolPtr(v bool) *bool {
	b := v
	return &b
}

func buildReport(rows []row) (map[string]*string, []comparison) {
	latest := selectLatestVersions(rows)
	latestVersions := map[string]*string{}
	for _, family := range families {
		if v := versionToString(latest[family]); v != "" {
			latestVersions[family] = strPtrOrNil(v)
		} else {
			latestVersions[family] = nil
		}
	}

	comparisons := make([]comparison, 0)
	for _, family := range families {
		versionTuple := latest[family]
		if len(versionTuple) == 0 {
			continue
		}

		familyRows := make([]row, 0)
		for _, r := range rows {
			if r.Family == family && equalVersion(r.VersionTuple, versionTuple) {
				familyRows = append(familyRows, r)
			}
		}

		type locationKey struct {
			location   string
			regionCode string
		}
		byLocation := map[locationKey][]row{}
		for _, r := range familyRows {
			key := locationKey{location: r.Location, regionCode: r.RegionCode}
			byLocation[key] = append(byLocation[key], r)
		}

		for key, locRows := range byLocation {
			inputRows := make([]row, 0)
			outputRows := make([]row, 0)
			for _, r := range locRows {
				if r.IOType == "input" {
					inputRows = append(inputRows, r)
				}
				if r.IOType == "output" {
					outputRows = append(outputRows, r)
				}
			}
			inputRow := preferredRow(inputRows)
			outputRow := preferredRow(outputRows)

			var inputUSD *big.Rat
			var outputUSD *big.Rat
			if inputRow != nil {
				inputUSD = parseRat(inputRow.USD)
			}
			if outputRow != nil {
				outputUSD = parseRat(outputRow.USD)
			}

			outputEstimated := false
			if outputUSD == nil && inputUSD != nil {
				outputUSD = ratMul(inputUSD, big.NewRat(5, 1))
				outputEstimated = true
			}

			var inputScale *big.Rat
			var outputScale *big.Rat
			if inputRow != nil {
				inputScale = tokenScaleTo1M(inputRow.Unit, inputRow.Description)
			}
			if outputRow != nil {
				outputScale = tokenScaleTo1M(outputRow.Unit, outputRow.Description)
			}
			if outputEstimated && outputScale == nil {
				outputScale = inputScale
			}

			inputUSDPer1M := ratMul(inputUSD, inputScale)
			outputUSDPer1M := ratMul(outputUSD, outputScale)

			differenceUSD := ratSub(outputUSD, inputUSD)
			multiplier := ratDiv(outputUSD, inputUSD)
			differenceUSDPer1M := ratSub(outputUSDPer1M, inputUSDPer1M)

			var modelName string
			switch {
			case inputRow != nil && inputRow.ModelName != "":
				modelName = inputRow.ModelName
			case outputRow != nil && outputRow.ModelName != "":
				modelName = outputRow.ModelName
			case len(locRows) > 0:
				modelName = locRows[0].ModelName
			}

			var inputUnit *string
			if inputRow != nil {
				inputUnit = strPtrOrNil(inputRow.Unit)
			} else if outputRow != nil {
				inputUnit = strPtrOrNil(outputRow.Unit)
			}

			var inputTier *string
			if inputRow != nil {
				inputTier = strPtrOrNil(inputRow.Tier)
			}
			var outputTier *string
			if outputRow != nil {
				outputTier = strPtrOrNil(outputRow.Tier)
			}

			var inputIsGlobal *bool
			if inputRow != nil {
				inputIsGlobal = boolPtr(inputRow.IsGlobal)
			}
			var outputIsGlobal *bool
			if outputRow != nil {
				outputIsGlobal = boolPtr(outputRow.IsGlobal)
			}

			var inputUsage *string
			if inputRow != nil {
				inputUsage = strPtrOrNil(inputRow.UsageType)
			}
			var outputUsage *string
			if outputRow != nil {
				outputUsage = strPtrOrNil(outputRow.UsageType)
			}

			comparisons = append(comparisons, comparison{
				Family:                   family,
				LatestVersion:            versionToString(versionTuple),
				ModelName:                strPtrOrNil(modelName),
				Location:                 strPtrOrNil(key.location),
				RegionCode:               strPtrOrNil(key.regionCode),
				InputUSDPerUnit:          formatRat(inputUSD),
				OutputUSDPerUnit:         formatRat(outputUSD),
				DifferenceUSDPerUnit:     formatRat(differenceUSD),
				InputUSDPer1MTokens:      formatRat(inputUSDPer1M),
				OutputUSDPer1MTokens:     formatRat(outputUSDPer1M),
				DifferenceUSDPer1MTokens: formatRat(differenceUSDPer1M),
				Multiplier:               formatRat(multiplier),
				OutputEstimatedWithX5:    outputEstimated,
				InputUnit:                inputUnit,
				InputTier:                inputTier,
				OutputTier:               outputTier,
				InputIsGlobal:            inputIsGlobal,
				OutputIsGlobal:           outputIsGlobal,
				InputUsageType:           inputUsage,
				OutputUsageType:          outputUsage,
			})
		}
	}

	sort.Slice(comparisons, func(i, j int) bool {
		left := comparisons[i]
		right := comparisons[j]
		if left.Family != right.Family {
			return left.Family < right.Family
		}
		leftLoc := ""
		if left.Location != nil {
			leftLoc = *left.Location
		}
		rightLoc := ""
		if right.Location != nil {
			rightLoc = *right.Location
		}
		if leftLoc != rightLoc {
			return leftLoc < rightLoc
		}
		leftRegion := ""
		if left.RegionCode != nil {
			leftRegion = *left.RegionCode
		}
		rightRegion := ""
		if right.RegionCode != nil {
			rightRegion = *right.RegionCode
		}
		return leftRegion < rightRegion
	})

	return latestVersions, comparisons
}

// pricingReportToModelInfo converts a BedrockPricingReport to a map of ModelInfo.
// Maps model families to Bedrock model IDs and extracts pricing per region.
func pricingReportToModelInfo(report *BedrockPricingReport, regionCode string) map[string]provider.ModelInfo {
	cache := make(map[string]provider.ModelInfo)

	// Filter comparisons to target region
	for _, comp := range report.Comparisons {
		if comp.RegionCode == nil || *comp.RegionCode != regionCode {
			continue
		}

		// Map family to model IDs
		modelIDs := modelIDsForFamily(comp.Family)

		// Parse pricing from strings to float64
		inputCost := parsePriceString(comp.InputUSDPer1MTokens)
		outputCost := parsePriceString(comp.OutputUSDPer1MTokens)

		// Skip entries with no usable pricing — let static fallback handle them.
		if inputCost == 0 && outputCost == 0 {
			continue
		}

		// Populate cache for all matching model IDs
		for _, id := range modelIDs {
			// Use static knownModels for ContextWindow (not in pricing API)
			contextWindow := 200_000 // default
			modelName := id
			if known, ok := knownModels[id]; ok {
				contextWindow = known.ContextWindow
				modelName = known.Name
			}

			cache[id] = provider.ModelInfo{
				ID:              id,
				Name:            modelName,
				ContextWindow:   contextWindow,
				InputCostPer1M:  inputCost,
				OutputCostPer1M: outputCost,
			}
		}
	}

	return cache
}

// modelIDsForFamily maps a pricing family to known Bedrock model IDs.
// Returns all model IDs in knownModels that match the given family pattern.
func modelIDsForFamily(family string) []string {
	var matches []string
	for id := range knownModels {
		if familyMatchesID(family, id) {
			matches = append(matches, id)
		}
	}
	return matches
}

// familyMatchesID checks if a model family matches a Bedrock model ID.
func familyMatchesID(family, modelID string) bool {
	modelLower := strings.ToLower(modelID)
	switch family {
	case "anthropic_haiku":
		return strings.Contains(modelLower, "haiku")
	case "anthropic_sonnet":
		return strings.Contains(modelLower, "sonnet")
	case "anthropic_opus":
		return strings.Contains(modelLower, "opus")
	case "moonshot_kimi":
		return strings.Contains(modelLower, "kimi") || strings.Contains(modelLower, "moonshot")
	default:
		return false
	}
}

// parsePriceString converts a pricing report string (from big.Rat formatting) to float64.
// Returns 0.0 if the string is nil or cannot be parsed.
func parsePriceString(s *string) float64 {
	if s == nil {
		return 0.0
	}
	var f float64
	_, err := fmt.Sscanf(*s, "%f", &f)
	if err != nil {
		return 0.0
	}
	return f
}
