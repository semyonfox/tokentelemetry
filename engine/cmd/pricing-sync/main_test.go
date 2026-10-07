package main

import (
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/pricing"
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

func TestMissingOutputIsNotFree(t *testing.T) {
	input := 1.0
	if _, ok := toRate(mdCost{Input: &input}, "test", "test", pricing.MustParseDate("2026-09-07")); ok {
		t.Fatal("missing output rate accepted")
	}
}

func TestPaidToZeroRequiresReview(t *testing.T) {
	date := pricing.MustParseDate("2026-09-07")
	previous := &dataset{Models: map[string]*pricing.Model{"test": {ID: "test", Rates: []pricing.Rate{{From: date, In: 1, Out: 2}}}}}
	next := &dataset{Models: map[string]*pricing.Model{"test": {ID: "test", Rates: []pricing.Rate{{From: date}}}}}
	merge(previous, next, date)
	if next.Models["test"].Rates[0].In != 1 {
		t.Fatal("paid price silently replaced with zero")
	}
}

func TestSyncPreservesSchedules(t *testing.T) {
	old := &dataset{Schedules: map[string]pricing.Schedule{"peak": {Days: []int{1}}}, Aliases: map[string]string{"old-name": "model"}}
	next := &dataset{}
	merge(old, next, pricing.MustParseDate("2026-09-07"))
	if len(next.Schedules["peak"].Days) != 1 || next.Aliases["old-name"] != "model" {
		t.Fatal("lost historical pricing metadata")
	}
}

func TestAliasGenerationDeterministic(t *testing.T) {
	for range 100 {
		ds := &dataset{Models: map[string]*pricing.Model{"openai.gpt-5.4": {}, "openai-gpt-5.4": {}}, Aliases: map[string]string{}}
		buildAliases(ds)
		if ds.Aliases["openai-gpt-5-4"] != "openai-gpt-5.4" {
			t.Fatal("unstable alias target")
		}
	}
}

func TestSyncKeepsSupplementalRatesAndLocalModelAliases(t *testing.T) {
	id := "qwen2.5-coder-3b-instruct"
	old := &dataset{
		Models:  map[string]*pricing.Model{id: {ID: id, Rates: []pricing.Rate{{From: pricing.MustParseDate("2026-10-07"), In: 0.01, Out: 0.032, Source: "codelace"}}}},
		Aliases: map[string]string{"qwen2.5-coder:3b": id},
	}
	next := &dataset{Models: map[string]*pricing.Model{"qwen2-5-coder-7b-instruct": {}}, Aliases: map[string]string{}}
	buildAliases(next)
	merge(old, next, pricing.MustParseDate("2026-10-08"))
	for query, canonical := range map[string]string{"qwen2.5-coder:3b": id, "qwen2.5-coder:7b": "qwen2-5-coder-7b-instruct"} {
		if next.Aliases[query] != canonical {
			t.Fatalf("alias %s = %s, want %s", query, next.Aliases[query], canonical)
		}
	}
	if r := next.Models[id].Rates[0]; r.From != pricing.MustParseDate("2026-10-07") || r.In != 0.01 || r.Out != 0.032 || r.Source != "codelace" {
		t.Fatalf("supplemental price history changed: %+v", r)
	}
}
