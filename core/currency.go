package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const defaultFrankfurterBaseURL = "https://api.frankfurter.app"

// currencySymbols maps ISO 4217 codes to display symbols for common currencies.
var currencySymbols = map[string]string{
	"USD": "$",
	"EUR": "€",
	"GBP": "£",
	"JPY": "¥",
	"CNY": "¥",
	"KRW": "₩",
	"INR": "₹",
	"BRL": "R$",
	"TRY": "₺",
	"THB": "฿",
	"PLN": "zł",
	"ILS": "₪",
	"SEK": "kr",
	"NOK": "kr",
	"DKK": "kr",
	"CZK": "Kč",
}

// CurrencySymbol returns the display symbol for the given ISO 4217 code.
// Falls back to the code itself (e.g., "CHF") if no symbol is mapped.
func CurrencySymbol(code string) string {
	upper := strings.ToUpper(strings.TrimSpace(code))
	if sym, ok := currencySymbols[upper]; ok {
		return sym
	}
	return upper
}

// CurrencyEngine provides methods for fetching exchange rates and listing
// supported currencies via the Frankfurter API.
type CurrencyEngine struct {
	baseURL    string
	httpClient *http.Client
	userAgent  string
}

// NewCurrencyEngine creates a CurrencyEngine using the default Frankfurter base URL.
func NewCurrencyEngine(httpClient *http.Client) *CurrencyEngine {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &CurrencyEngine{
		baseURL:    defaultFrankfurterBaseURL,
		httpClient: httpClient,
		userAgent:  "cosmos-currency/1.0",
	}
}

// NewCurrencyEngineWithBaseURL creates a CurrencyEngine with a custom base URL.
// Useful for testing with httptest.
func NewCurrencyEngineWithBaseURL(httpClient *http.Client, baseURL string) *CurrencyEngine {
	engine := NewCurrencyEngine(httpClient)
	if trimmed := strings.TrimSpace(baseURL); trimmed != "" {
		engine.baseURL = strings.TrimRight(trimmed, "/")
	}
	return engine
}

// FetchRate fetches the exchange rate from one currency to another.
// Returns the rate as a float64 (e.g., 0.92 for USD→EUR).
func (e *CurrencyEngine) FetchRate(ctx context.Context, from, to string) (float64, error) {
	if e == nil || e.httpClient == nil {
		return 0, fmt.Errorf("currency engine is not initialized")
	}
	from = strings.ToUpper(strings.TrimSpace(from))
	to = strings.ToUpper(strings.TrimSpace(to))
	if from == "" || to == "" {
		return 0, fmt.Errorf("from and to currency codes are required")
	}
	if from == to {
		return 1.0, nil
	}

	params := url.Values{}
	params.Set("amount", "1")
	params.Set("from", from)
	params.Set("to", to)

	endpoint := e.baseURL + "/latest?" + params.Encode()

	var out struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := e.getJSON(ctx, endpoint, &out); err != nil {
		return 0, err
	}
	rate, ok := out.Rates[to]
	if !ok {
		return 0, fmt.Errorf("currency API did not return rate for %s", to)
	}
	return rate, nil
}

// ListSupportedCurrencies returns a map of ISO 4217 code → currency name.
func (e *CurrencyEngine) ListSupportedCurrencies(ctx context.Context) (map[string]string, error) {
	if e == nil || e.httpClient == nil {
		return nil, fmt.Errorf("currency engine is not initialized")
	}

	endpoint := e.baseURL + "/currencies"
	var out map[string]string
	if err := e.getJSON(ctx, endpoint, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("currency API returned empty currencies payload")
	}
	return out, nil
}

// getJSON performs an HTTP GET and JSON-decodes the response body into dst.
func (e *CurrencyEngine) getJSON(ctx context.Context, endpoint string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", e.userAgent)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("currency API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return fmt.Errorf("currency API returned HTTP %d: %s", resp.StatusCode, msg)
	}

	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("failed to decode currency API response: %w", err)
	}
	return nil
}

// CurrencyFormatter holds the active currency configuration and cached rate
// for converting USD amounts to the display currency.
type CurrencyFormatter struct {
	Code   string  // ISO 4217 code (e.g., "EUR")
	Symbol string  // Display symbol (e.g., "€")
	Rate   float64 // Exchange rate from USD (e.g., 0.92)
}

// NewCurrencyFormatter creates a formatter with the given currency code, symbol, and rate.
func NewCurrencyFormatter(code, symbol string, rate float64) *CurrencyFormatter {
	return &CurrencyFormatter{
		Code:   strings.ToUpper(strings.TrimSpace(code)),
		Symbol: symbol,
		Rate:   rate,
	}
}

// DefaultCurrencyFormatter returns a USD formatter (no conversion).
func DefaultCurrencyFormatter() *CurrencyFormatter {
	return NewCurrencyFormatter("USD", "$", 1.0)
}

// Format converts a USD amount using the cached rate and formats it for display.
// Uses 4 decimal places for amounts between 0 (exclusive) and 0.01 (exclusive),
// and 2 decimal places otherwise.
func (f *CurrencyFormatter) Format(usdAmount float64) string {
	converted := usdAmount * f.Rate
	if converted > 0 && converted < 0.01 {
		return fmt.Sprintf("%s %.4f", f.Symbol, converted)
	}
	return fmt.Sprintf("%s %.2f", f.Symbol, converted)
}
