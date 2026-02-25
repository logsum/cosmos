package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchRate(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("from"); got != "USD" {
			t.Errorf("from query mismatch: got %q", got)
		}
		if got := r.URL.Query().Get("to"); got != "EUR" {
			t.Errorf("to query mismatch: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"amount":1,"base":"USD","date":"2026-02-24","rates":{"EUR":0.9231}}`))
	}))
	defer ts.Close()

	engine := NewCurrencyEngineWithBaseURL(ts.Client(), ts.URL)
	rate, err := engine.FetchRate(context.Background(), "USD", "EUR")
	if err != nil {
		t.Fatalf("FetchRate() error = %v", err)
	}
	if rate != 0.9231 {
		t.Errorf("FetchRate() = %v, want 0.9231", rate)
	}
}

func TestFetchRateSameCurrency(t *testing.T) {
	t.Parallel()

	engine := NewCurrencyEngine(nil)
	rate, err := engine.FetchRate(context.Background(), "USD", "USD")
	if err != nil {
		t.Fatalf("FetchRate(USD, USD) error = %v", err)
	}
	if rate != 1.0 {
		t.Errorf("FetchRate(USD, USD) = %v, want 1.0", rate)
	}
}

func TestFetchRateHTTPError(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer ts.Close()

	engine := NewCurrencyEngineWithBaseURL(ts.Client(), ts.URL)
	_, err := engine.FetchRate(context.Background(), "USD", "EUR")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", http.StatusForbidden)) {
		t.Errorf("expected HTTP status in error, got: %v", err)
	}
}

func TestListSupportedCurrencies(t *testing.T) {
	t.Parallel()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/currencies" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"USD":"United States Dollar","EUR":"Euro","GBP":"British Pound"}`))
	}))
	defer ts.Close()

	engine := NewCurrencyEngineWithBaseURL(ts.Client(), ts.URL)
	currencies, err := engine.ListSupportedCurrencies(context.Background())
	if err != nil {
		t.Fatalf("ListSupportedCurrencies() error = %v", err)
	}
	if got := currencies["USD"]; got != "United States Dollar" {
		t.Errorf("USD = %q, want %q", got, "United States Dollar")
	}
	if got := currencies["EUR"]; got != "Euro" {
		t.Errorf("EUR = %q, want %q", got, "Euro")
	}
	if got := currencies["GBP"]; got != "British Pound" {
		t.Errorf("GBP = %q, want %q", got, "British Pound")
	}
}

func TestCurrencyFormatterUSD(t *testing.T) {
	f := DefaultCurrencyFormatter()
	tests := []struct {
		amount float64
		want   string
	}{
		{0.00, "$ 0.00"},
		{0.001, "$ 0.0010"},
		{0.05, "$ 0.05"},
		{1.23, "$ 1.23"},
		{12.345, "$ 12.35"},
	}
	for _, tt := range tests {
		got := f.Format(tt.amount)
		if got != tt.want {
			t.Errorf("Format(%v) = %q, want %q", tt.amount, got, tt.want)
		}
	}
}

func TestCurrencyFormatterEUR(t *testing.T) {
	f := NewCurrencyFormatter("EUR", "€", 0.92)
	// $1.00 USD * 0.92 = €0.92
	got := f.Format(1.00)
	want := "€ 0.92"
	if got != want {
		t.Errorf("Format(1.00) = %q, want %q", got, want)
	}

	// $10.00 USD * 0.92 = €9.20
	got = f.Format(10.00)
	want = "€ 9.20"
	if got != want {
		t.Errorf("Format(10.00) = %q, want %q", got, want)
	}
}

func TestCurrencyFormatterFallbackSymbol(t *testing.T) {
	// CHF has no entry in currencySymbols — CurrencySymbol should return "CHF"
	sym := CurrencySymbol("CHF")
	if sym != "CHF" {
		t.Errorf("CurrencySymbol(CHF) = %q, want %q", sym, "CHF")
	}

	f := NewCurrencyFormatter("CHF", CurrencySymbol("CHF"), 0.89)
	got := f.Format(1.00)
	want := "CHF 0.89"
	if got != want {
		t.Errorf("Format(1.00) = %q, want %q", got, want)
	}
}

func TestCurrencySymbolKnown(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"USD", "$"},
		{"EUR", "€"},
		{"GBP", "£"},
		{"JPY", "¥"},
		{"BRL", "R$"},
	}
	for _, tt := range tests {
		got := CurrencySymbol(tt.code)
		if got != tt.want {
			t.Errorf("CurrencySymbol(%q) = %q, want %q", tt.code, got, tt.want)
		}
	}
}
