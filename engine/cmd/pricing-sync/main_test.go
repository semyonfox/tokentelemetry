package main

import (
	"testing"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/pricing"
)

func TestMergePreservesProviderHistory(t *testing.T) {
	old := pricing.Rate{From: pricing.MustParseDate("2026-01-01"), In: 1, Out: 2, TierCacheRead: 0.1}
	fresh := pricing.Rate{From: pricing.MustParseDate("2026-08-01"), In: 1, Out: 2, TierCacheRead: 0.2}
	prev := &dataset{Models: map[string]*pricing.Model{}, ByProvider: map[string]*pricing.Model{
		"p\x00m":    {ID: "m", Rates: []pricing.Rate{old}},
		"p\x00gone": {ID: "gone", Rates: []pricing.Rate{old}},
	}}
	next := &dataset{Models: map[string]*pricing.Model{}, ByProvider: map[string]*pricing.Model{
		"p\x00m": {ID: "m", Rates: []pricing.Rate{fresh}},
	}}

	merge(prev, next, pricing.MustParseDate("2026-08-28"))
	if got := len(next.ByProvider["p\x00m"].Rates); got != 2 {
		t.Fatalf("changed provider history has %d rates, want 2", got)
	}
	if _, ok := next.ByProvider["p\x00gone"]; !ok {
		t.Error("vanished provider history was discarded")
	}
}
