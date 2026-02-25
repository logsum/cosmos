package core

import (
	"cosmos/core/provider"
	"sync"
	"testing"
)

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		input  int
		output int
		want   string
	}{
		{0, 0, "▲0 ▼0"},
		{999, 0, "▲999 ▼0"},
		{1000, 0, "▲1K ▼0"},
		{1500, 0, "▲1.5K ▼0"},
		{10000, 0, "▲10K ▼0"},
		{999999, 0, "▲1M ▼0"},
		{1000000, 0, "▲1M ▼0"},
		{1500000, 0, "▲1.5M ▼0"},
		{0, 800, "▲0 ▼800"},
		{1200, 800, "▲1.2K ▼800"},
	}

	for _, tt := range tests {
		snap := CostSnapshot{
			TotalInputTokens:  tt.input,
			TotalOutputTokens: tt.output,
		}
		got := snap.FormatTokens()
		if got != tt.want {
			t.Errorf("FormatTokens(%d, %d) = %q, want %q", tt.input, tt.output, got, tt.want)
		}
	}
}

func TestFormatCost(t *testing.T) {
	tests := []struct {
		cost float64
		want string
	}{
		{0.00, "$ 0.00"},
		{0.001, "$ 0.0010"},
		{0.0012, "$ 0.0012"},
		{0.05, "$ 0.05"},
		{1.23, "$ 1.23"},
		{12.345, "$ 12.35"},
		{0.0099, "$ 0.0099"},
		{0.01, "$ 0.01"},
	}

	for _, tt := range tests {
		snap := CostSnapshot{TotalCost: tt.cost}
		got := snap.FormatCost()
		if got != tt.want {
			t.Errorf("FormatCost(%v) = %q, want %q", tt.cost, got, tt.want)
		}
	}
}

func modelInfo(id, name string, inputCost, outputCost float64) provider.ModelInfo {
	return provider.ModelInfo{
		ID:              id,
		Name:            name,
		ContextWindow:   200000,
		InputCostPer1M:  inputCost,
		OutputCostPer1M: outputCost,
	}
}

func TestRecordSingleModel(t *testing.T) {
	tracker := NewTracker(nil, nil)

	model := modelInfo("opus-4", "Claude Opus 4", 15.0, 75.0)
	tracker.Record(model, provider.Usage{InputTokens: 1000, OutputTokens: 500}, SourcePrompt)
	tracker.Record(model, provider.Usage{InputTokens: 2000, OutputTokens: 1000}, SourcePrompt)

	snap := tracker.Snapshot()

	if snap.TotalInputTokens != 3000 {
		t.Errorf("TotalInputTokens = %d, want 3000", snap.TotalInputTokens)
	}
	if snap.TotalOutputTokens != 1500 {
		t.Errorf("TotalOutputTokens = %d, want 1500", snap.TotalOutputTokens)
	}

	// Cost: (3000 * 15.0 / 1_000_000) + (1500 * 75.0 / 1_000_000) = 0.045 + 0.1125 = 0.1575
	wantCost := 0.1575
	if diff := snap.TotalCost - wantCost; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TotalCost = %f, want %f", snap.TotalCost, wantCost)
	}

	if len(snap.Models) != 1 {
		t.Fatalf("len(Models) = %d, want 1", len(snap.Models))
	}
	if snap.Models[0].ModelID != "opus-4" {
		t.Errorf("ModelID = %q, want %q", snap.Models[0].ModelID, "opus-4")
	}
}

func TestRecordMultipleModels(t *testing.T) {
	tracker := NewTracker(nil, nil)

	opus := modelInfo("opus-4", "Claude Opus 4", 15.0, 75.0)
	haiku := modelInfo("haiku-3", "Claude Haiku 3", 0.25, 1.25)

	tracker.Record(opus, provider.Usage{InputTokens: 1000, OutputTokens: 500}, SourcePrompt)
	tracker.Record(haiku, provider.Usage{InputTokens: 2000, OutputTokens: 1000}, SourcePrompt)

	snap := tracker.Snapshot()

	if snap.TotalInputTokens != 3000 {
		t.Errorf("TotalInputTokens = %d, want 3000", snap.TotalInputTokens)
	}
	if snap.TotalOutputTokens != 1500 {
		t.Errorf("TotalOutputTokens = %d, want 1500", snap.TotalOutputTokens)
	}
	if len(snap.Models) != 2 {
		t.Fatalf("len(Models) = %d, want 2", len(snap.Models))
	}

	// Verify per-model costs independently
	opusCost := float64(1000)*15.0/1_000_000 + float64(500)*75.0/1_000_000   // 0.015 + 0.0375 = 0.0525
	haikuCost := float64(2000)*0.25/1_000_000 + float64(1000)*1.25/1_000_000 // 0.0005 + 0.00125 = 0.00175
	wantTotal := opusCost + haikuCost

	if diff := snap.TotalCost - wantTotal; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("TotalCost = %f, want %f", snap.TotalCost, wantTotal)
	}
}

func TestRecordMultipleSources(t *testing.T) {
	tracker := NewTracker(nil, nil)

	model := modelInfo("opus-4", "Claude Opus 4", 15.0, 75.0)
	tracker.Record(model, provider.Usage{InputTokens: 1000, OutputTokens: 500}, SourcePrompt)
	tracker.Record(model, provider.Usage{InputTokens: 200, OutputTokens: 100}, Source("code-analyzer.analyzeFile"))

	snap := tracker.Snapshot()

	if snap.TotalInputTokens != 1200 {
		t.Errorf("TotalInputTokens = %d, want 1200", snap.TotalInputTokens)
	}
	if snap.TotalOutputTokens != 600 {
		t.Errorf("TotalOutputTokens = %d, want 600", snap.TotalOutputTokens)
	}

	if len(snap.Models) != 1 {
		t.Fatalf("len(Models) = %d, want 1", len(snap.Models))
	}
	if len(snap.Models[0].Sources) != 2 {
		t.Fatalf("len(Sources) = %d, want 2", len(snap.Models[0].Sources))
	}

	sourceMap := make(map[Source]SourceUsage)
	for _, su := range snap.Models[0].Sources {
		sourceMap[su.Source] = su
	}

	prompt := sourceMap[SourcePrompt]
	if prompt.InputTokens != 1000 || prompt.OutputTokens != 500 {
		t.Errorf("SourcePrompt tokens = (%d, %d), want (1000, 500)", prompt.InputTokens, prompt.OutputTokens)
	}

	analyzer := sourceMap[Source("code-analyzer.analyzeFile")]
	if analyzer.InputTokens != 200 || analyzer.OutputTokens != 100 {
		t.Errorf("Analyzer tokens = (%d, %d), want (200, 100)", analyzer.InputTokens, analyzer.OutputTokens)
	}
}

func TestCallbackInvoked(t *testing.T) {
	var mu sync.Mutex
	var snapshots []CostSnapshot

	tracker := NewTracker(func(snap CostSnapshot) {
		mu.Lock()
		snapshots = append(snapshots, snap)
		mu.Unlock()
	}, nil)

	model := modelInfo("opus-4", "Claude Opus 4", 15.0, 75.0)
	tracker.Record(model, provider.Usage{InputTokens: 100, OutputTokens: 50}, SourcePrompt)
	tracker.Record(model, provider.Usage{InputTokens: 200, OutputTokens: 100}, SourcePrompt)

	mu.Lock()
	defer mu.Unlock()

	if len(snapshots) != 2 {
		t.Fatalf("callback called %d times, want 2", len(snapshots))
	}

	// First callback: 100 in, 50 out
	if snapshots[0].TotalInputTokens != 100 || snapshots[0].TotalOutputTokens != 50 {
		t.Errorf("first snapshot = (%d, %d), want (100, 50)",
			snapshots[0].TotalInputTokens, snapshots[0].TotalOutputTokens)
	}

	// Second callback: 300 in, 150 out (cumulative)
	if snapshots[1].TotalInputTokens != 300 || snapshots[1].TotalOutputTokens != 150 {
		t.Errorf("second snapshot = (%d, %d), want (300, 150)",
			snapshots[1].TotalInputTokens, snapshots[1].TotalOutputTokens)
	}
}

func TestNilCallback(t *testing.T) {
	tracker := NewTracker(nil, nil)
	model := modelInfo("opus-4", "Claude Opus 4", 15.0, 75.0)

	// Should not panic
	tracker.Record(model, provider.Usage{InputTokens: 100, OutputTokens: 50}, SourcePrompt)

	snap := tracker.Snapshot()
	if snap.TotalInputTokens != 100 {
		t.Errorf("TotalInputTokens = %d, want 100", snap.TotalInputTokens)
	}
}

func TestSnapshotIsolation(t *testing.T) {
	tracker := NewTracker(nil, nil)
	model := modelInfo("opus-4", "Claude Opus 4", 15.0, 75.0)
	tracker.Record(model, provider.Usage{InputTokens: 100, OutputTokens: 50}, SourcePrompt)

	snap := tracker.Snapshot()

	// Mutate the returned snapshot
	snap.TotalInputTokens = 999999
	snap.Models[0].InputTokens = 888888
	snap.Models[0].Sources[0].InputTokens = 777777

	// Tracker state should be unaffected
	snap2 := tracker.Snapshot()
	if snap2.TotalInputTokens != 100 {
		t.Errorf("TotalInputTokens = %d after mutation, want 100", snap2.TotalInputTokens)
	}
	if snap2.Models[0].InputTokens != 100 {
		t.Errorf("Models[0].InputTokens = %d after mutation, want 100", snap2.Models[0].InputTokens)
	}
	if snap2.Models[0].Sources[0].InputTokens != 100 {
		t.Errorf("Sources[0].InputTokens = %d after mutation, want 100", snap2.Models[0].Sources[0].InputTokens)
	}
}

func TestContextUsagePercentage(t *testing.T) {
	tracker := NewTracker(nil, nil)

	model := provider.ModelInfo{
		ID:              "opus-4",
		Name:            "Claude Opus 4",
		ContextWindow:   200_000,
		InputCostPer1M:  15.0,
		OutputCostPer1M: 75.0,
	}
	tracker.Record(model, provider.Usage{InputTokens: 10_000, OutputTokens: 5_000}, SourcePrompt)

	snap := tracker.Snapshot()

	// 15000 / 200000 = 7.5%
	pct := snap.ContextUsagePercentage("opus-4")
	wantPct := 7.5
	if diff := pct - wantPct; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("ContextUsagePercentage = %f, want %f", pct, wantPct)
	}
}

func TestContextUsagePercentageUnknownModel(t *testing.T) {
	snap := CostSnapshot{}
	pct := snap.ContextUsagePercentage("nonexistent-model")
	if pct != 0.0 {
		t.Errorf("expected 0.0 for unknown model, got %f", pct)
	}
}

func TestCallbackDoesNotDeadlock(t *testing.T) {
	// The callback calls Snapshot(), which takes the mutex. If Record() held
	// the mutex while invoking the callback, this would deadlock.
	done := make(chan struct{})
	var tracker *Tracker

	tracker = NewTracker(func(snap CostSnapshot) {
		// This call takes t.mu — if Record still holds the lock, we deadlock.
		_ = tracker.Snapshot()
		close(done)
	}, nil)

	model := modelInfo("opus-4", "Claude Opus 4", 15.0, 75.0)
	tracker.Record(model, provider.Usage{InputTokens: 100, OutputTokens: 50}, SourcePrompt)

	// If we reach here, no deadlock occurred.
	<-done
}

func TestContextUsagePercentageZeroContextWindow(t *testing.T) {
	// Model with no context window defined.
	tracker := NewTracker(nil, nil)
	model := provider.ModelInfo{
		ID:              "no-window",
		Name:            "No Window",
		ContextWindow:   0,
		InputCostPer1M:  1.0,
		OutputCostPer1M: 5.0,
	}
	tracker.Record(model, provider.Usage{InputTokens: 1000, OutputTokens: 500}, SourcePrompt)

	snap := tracker.Snapshot()
	pct := snap.ContextUsagePercentage("no-window")
	if pct != 0.0 {
		t.Errorf("expected 0.0 for zero context window, got %f", pct)
	}
}

func TestContextWindowInSnapshot(t *testing.T) {
	tracker := NewTracker(nil, nil)
	model := provider.ModelInfo{
		ID:              "opus-4",
		Name:            "Claude Opus 4",
		ContextWindow:   200_000,
		InputCostPer1M:  15.0,
		OutputCostPer1M: 75.0,
	}
	tracker.Record(model, provider.Usage{InputTokens: 100, OutputTokens: 50}, SourcePrompt)

	snap := tracker.Snapshot()
	if len(snap.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(snap.Models))
	}
	if snap.Models[0].ContextWindow != 200_000 {
		t.Errorf("ContextWindow = %d, want 200000", snap.Models[0].ContextWindow)
	}
}

func TestZeroUsage(t *testing.T) {
	tracker := NewTracker(nil, nil)
	model := modelInfo("opus-4", "Claude Opus 4", 15.0, 75.0)

	tracker.Record(model, provider.Usage{InputTokens: 0, OutputTokens: 0}, SourcePrompt)

	snap := tracker.Snapshot()
	if snap.TotalInputTokens != 0 {
		t.Errorf("TotalInputTokens = %d, want 0", snap.TotalInputTokens)
	}
	if snap.TotalOutputTokens != 0 {
		t.Errorf("TotalOutputTokens = %d, want 0", snap.TotalOutputTokens)
	}
	if snap.TotalCost != 0 {
		t.Errorf("TotalCost = %f, want 0", snap.TotalCost)
	}
}

func TestFormatCostWithCurrency(t *testing.T) {
	eurFormatter := NewCurrencyFormatter("EUR", "€", 0.92)
	tracker := NewTracker(nil, eurFormatter)

	model := modelInfo("opus-4", "Claude Opus 4", 15.0, 75.0)
	// Record enough to produce a non-trivial cost.
	// Cost = (10000 * 15.0 / 1_000_000) + (5000 * 75.0 / 1_000_000) = 0.15 + 0.375 = 0.525 USD
	// Converted: 0.525 * 0.92 = 0.483 EUR
	tracker.Record(model, provider.Usage{InputTokens: 10000, OutputTokens: 5000}, SourcePrompt)

	snap := tracker.Snapshot()
	got := snap.FormatCost()
	want := "€ 0.48"
	if got != want {
		t.Errorf("FormatCost() with EUR = %q, want %q", got, want)
	}
}

func TestFormatCostNilFormatterDefaultsToUSD(t *testing.T) {
	tracker := NewTracker(nil, nil)
	model := modelInfo("opus-4", "Claude Opus 4", 15.0, 75.0)
	tracker.Record(model, provider.Usage{InputTokens: 10000, OutputTokens: 5000}, SourcePrompt)

	snap := tracker.Snapshot()
	got := snap.FormatCost()
	want := "$ 0.53"
	if got != want {
		t.Errorf("FormatCost() with nil formatter = %q, want %q", got, want)
	}
}
