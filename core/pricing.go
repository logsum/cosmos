// Package core contains the orchestration layer for Cosmos.
package core

import (
	"cosmos/core/provider"
	"fmt"
	"strings"
	"sync"
)

// Source identifies what triggered the LLM call.
// Use SourcePrompt for user-initiated prompts.
// Use tool/agent names for tool-triggered calls (e.g., "code-analyzer.analyzeFile").
type Source string

const SourcePrompt Source = "prompt"

// SourceUsage holds token counts and cost for one source within a model.
type SourceUsage struct {
	Source       Source
	InputTokens  int
	OutputTokens int
	Cost         float64
}

// ModelUsage holds cumulative token counts and cost for one model.
type ModelUsage struct {
	ModelID         string
	ModelName       string
	InputTokens     int
	OutputTokens    int
	Cost            float64
	InputCostPer1M  float64
	OutputCostPer1M float64
	ContextWindow   int // Context window size for percentage calculation
	Sources         []SourceUsage
}

// CostSnapshot is a point-in-time, deep-copied view of all accumulated usage.
type CostSnapshot struct {
	TotalInputTokens  int
	TotalOutputTokens int
	TotalCost         float64
	Models            []ModelUsage
	formatter         *CurrencyFormatter // set by Tracker during snapshot
}

type sourceAccum struct {
	inputTokens  int
	outputTokens int
}

type modelAccum struct {
	info    provider.ModelInfo
	sources map[Source]*sourceAccum
}

// Tracker accumulates token usage and cost across LLM calls.
type Tracker struct {
	mu        sync.Mutex
	models    map[string]*modelAccum // keyed by ModelInfo.ID
	onUpdate  func(CostSnapshot)     // optional callback, nil-safe
	formatter *CurrencyFormatter     // nil-safe, defaults to USD
}

// NewTracker creates a new cost tracker. The onUpdate callback, if non-nil,
// is called synchronously after each Record with a fresh snapshot.
// The formatter, if non-nil, is used for cost display formatting; nil defaults to USD.
func NewTracker(onUpdate func(CostSnapshot), formatter *CurrencyFormatter) *Tracker {
	return &Tracker{
		models:    make(map[string]*modelAccum),
		onUpdate:  onUpdate,
		formatter: formatter,
	}
}

// Record accumulates token usage for the given model and source,
// then invokes the onUpdate callback (if set) with a fresh snapshot.
func (t *Tracker) Record(model provider.ModelInfo, usage provider.Usage, source Source) {
	t.mu.Lock()

	ma, ok := t.models[model.ID]
	if !ok {
		ma = &modelAccum{
			info:    model,
			sources: make(map[Source]*sourceAccum),
		}
		t.models[model.ID] = ma
	}

	sa, ok := ma.sources[source]
	if !ok {
		sa = &sourceAccum{}
		ma.sources[source] = sa
	}

	sa.inputTokens += usage.InputTokens
	sa.outputTokens += usage.OutputTokens

	var snap CostSnapshot
	if t.onUpdate != nil {
		snap = t.snapshotLocked()
	}
	t.mu.Unlock()

	if t.onUpdate != nil {
		t.onUpdate(snap)
	}
}

// Snapshot returns a deep-copied view of all accumulated usage.
func (t *Tracker) Snapshot() CostSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked()
}

// snapshotLocked builds a CostSnapshot from current state. Caller must hold t.mu.
func (t *Tracker) snapshotLocked() CostSnapshot {
	var snap CostSnapshot
	snap.formatter = t.formatter

	for _, ma := range t.models {
		var mu ModelUsage
		mu.ModelID = ma.info.ID
		mu.ModelName = ma.info.Name
		mu.InputCostPer1M = ma.info.InputCostPer1M
		mu.OutputCostPer1M = ma.info.OutputCostPer1M
		mu.ContextWindow = ma.info.ContextWindow

		for src, sa := range ma.sources {
			srcCost := float64(sa.inputTokens)*ma.info.InputCostPer1M/1_000_000 +
				float64(sa.outputTokens)*ma.info.OutputCostPer1M/1_000_000
			mu.Sources = append(mu.Sources, SourceUsage{
				Source:       src,
				InputTokens:  sa.inputTokens,
				OutputTokens: sa.outputTokens,
				Cost:         srcCost,
			})
			mu.InputTokens += sa.inputTokens
			mu.OutputTokens += sa.outputTokens
		}

		mu.Cost = float64(mu.InputTokens)*ma.info.InputCostPer1M/1_000_000 +
			float64(mu.OutputTokens)*ma.info.OutputCostPer1M/1_000_000

		snap.TotalInputTokens += mu.InputTokens
		snap.TotalOutputTokens += mu.OutputTokens
		snap.TotalCost += mu.Cost
		snap.Models = append(snap.Models, mu)
	}

	return snap
}

// formatCount formats a token count with K/M abbreviations.
// Rules: 0–999 as-is, 1K–999K with one decimal (drop .0), 1M+ same pattern.
// Guard: if rounding would produce "1000.0K", display "1M" instead.
//
// NOTE: A duplicate of this function exists in ui/chat.go for display purposes.
// If you modify this logic, update the UI copy as well.
func formatCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		k := float64(n) / 1000
		// Guard: %.1f of 999.95+ rounds to "1000.0"
		if k >= 999.95 {
			return "1M"
		}
		s := fmt.Sprintf("%.1fK", k)
		return strings.Replace(s, ".0K", "K", 1)
	}
	m := float64(n) / 1_000_000
	s := fmt.Sprintf("%.1fM", m)
	return strings.Replace(s, ".0M", "M", 1)
}

// FormatTokens formats the total token counts as "▲<input> ▼<output>".
func (s CostSnapshot) FormatTokens() string {
	return fmt.Sprintf("▲%s ▼%s", formatCount(s.TotalInputTokens), formatCount(s.TotalOutputTokens))
}

// FormatCost formats the total cost in the configured display currency.
// Uses 4 decimal places when cost is between 0 (exclusive) and 0.01 (exclusive),
// and 2 decimal places otherwise. Falls back to USD formatting if no formatter is set.
func (s CostSnapshot) FormatCost() string {
	f := s.formatter
	if f == nil {
		f = DefaultCurrencyFormatter()
	}
	return f.Format(s.TotalCost)
}

// ContextUsagePercentage returns the percentage of context window used for the given model.
// Returns 0.0 if the model is not found or has no context window defined.
//
// Deprecated: This method computes percentage from cumulative tracker tokens, which
// are not the same as per-request context usage reported by the provider. Use
// per-response usage from provider.Usage instead (see processUserMessage in core/loop.go).
func (s CostSnapshot) ContextUsagePercentage(modelID string) float64 {
	for _, m := range s.Models {
		if m.ModelID == modelID && m.ContextWindow > 0 {
			totalTokens := m.InputTokens + m.OutputTokens
			return (float64(totalTokens) / float64(m.ContextWindow)) * 100.0
		}
	}
	return 0.0
}

// AdjustTokens sets absolute token counts for a model, used during compaction.
// This replaces accumulated counts with new values reflecting the compacted state.
// All existing source accumulations are replaced with a single "compacted" source.
// Fires the onUpdate callback (if set) so the status bar reflects the new state.
func (t *Tracker) AdjustTokens(modelID string, newInput, newOutput int) {
	t.mu.Lock()

	ma, ok := t.models[modelID]
	if !ok {
		// Model not tracked yet, nothing to adjust
		t.mu.Unlock()
		return
	}

	// Replace all source accumulations with a single "compacted" source
	ma.sources = map[Source]*sourceAccum{
		Source("compacted"): {
			inputTokens:  newInput,
			outputTokens: newOutput,
		},
	}

	var snap CostSnapshot
	if t.onUpdate != nil {
		snap = t.snapshotLocked()
	}
	t.mu.Unlock()

	if t.onUpdate != nil {
		t.onUpdate(snap)
	}
}
