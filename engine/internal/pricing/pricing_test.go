package pricing

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t.Add(12 * time.Hour)
}

func testTable() *Table {
	return &Table{
		Updated: MustParseDate("2026-08-27"),
		Models: map[string]*Model{
			// A model that took an 80% cut mid-window — the gpt-5.6-luna case.
			"luna": {ID: "luna", FirstParty: true, Rates: []Rate{
				{From: MustParseDate("2026-01-01"), In: 1.00, Out: 6.00, CacheRead: 0.10},
				{From: MustParseDate("2026-08-15"), In: 0.20, Out: 1.20, CacheRead: 0.02},
			}},
			"tiered": {ID: "tiered", Rates: []Rate{{
				From: MustParseDate("2026-01-01"), In: 2, Out: 12, CacheRead: 0.2,
				TierThreshold: 272000, TierIn: 4, TierOut: 18, TierCacheRead: 0.4,
			}}},
		},
		ByProvider: map[string]*Model{
			"together\x00luna": {ID: "luna", Rates: []Rate{
				{From: MustParseDate("2026-01-01"), In: 1.40, Out: 4.40},
			}},
		},
		Aliases: map[string]string{"luna-v2": "luna"},
	}
}

// A price cut must not retroactively reprice earlier usage. This is the whole
// reason the schema carries dates.
func TestRateIsEffectiveDated(t *testing.T) {
	tbl := testTable()
	for _, tc := range []struct {
		when   string
		wantIn float64
	}{
		{"2026-06-01", 1.00}, // before the cut
		{"2026-08-14", 1.00}, // day before
		{"2026-08-15", 0.20}, // day of
		{"2026-08-27", 0.20}, // after
	} {
		r, conf, ok := tbl.Lookup("luna", "", day(tc.when))
		if !ok || conf != ConfidenceExact {
			t.Fatalf("%s: lookup failed (conf=%v)", tc.when, conf)
		}
		if r.In != tc.wantIn {
			t.Errorf("%s: input rate = %v, want %v", tc.when, r.In, tc.wantIn)
		}
	}
}

// Usage older than any rate we hold is priced at the earliest known rate rather
// than refused, but callers are told via RateNewerThanCall (see package cost).
func TestPreHistoryUsesEarliestRate(t *testing.T) {
	tbl := testTable()
	r, _, ok := tbl.Lookup("luna", "", day("2025-03-01"))
	if !ok || r.In != 1.00 {
		t.Fatalf("pre-history rate = %v (ok=%v), want 1.00", r.In, ok)
	}
}

// A recorded provider wins, because it says who actually billed the call.
func TestProviderOverridesFlatTable(t *testing.T) {
	tbl := testTable()
	r, conf, ok := tbl.Lookup("luna", "together", day("2026-08-27"))
	if !ok || conf != ConfidenceProvider {
		t.Fatalf("conf = %v, want provider", conf)
	}
	if r.In != 1.40 {
		t.Errorf("input rate = %v, want 1.40 (Together's markup)", r.In)
	}
}

// The long-context surcharge must apply above the threshold and not below.
func TestContextTierApplies(t *testing.T) {
	tbl := testTable()
	r, _, _ := tbl.Lookup("tiered", "", day("2026-08-27"))

	in, out, cr, _ := r.ForContext(100_000)
	if in != 2 || out != 12 || cr != 0.2 {
		t.Errorf("under threshold = %v/%v/%v, want 2/12/0.2", in, out, cr)
	}
	in, out, cr, _ = r.ForContext(300_000)
	if in != 4 || out != 18 || cr != 0.4 {
		t.Errorf("over threshold = %v/%v/%v, want 4/18/0.4", in, out, cr)
	}
}

// An unknown model must resolve to nothing. The old table fell back to a
// substring scan and then a $2/$10 default, which billed a free model $380.
func TestUnknownModelIsUnpricedNotGuessed(t *testing.T) {
	tbl := testTable()
	for _, id := range []string{"totally-made-up", "luna-turbo-9000", "codex-auto-review"} {
		if _, conf, ok := tbl.Lookup(id, "", day("2026-08-27")); ok || conf != ConfidenceUnpriced {
			t.Errorf("%s: resolved to %v, want unpriced", id, conf)
		}
	}
}

// Aliases are explicit equivalences, never substring guesses.
func TestAliasResolves(t *testing.T) {
	tbl := testTable()
	r, conf, ok := tbl.Lookup("luna-v2", "", day("2026-08-27"))
	if !ok || conf != ConfidenceAlias || r.In != 0.20 {
		t.Errorf("alias lookup = %v/%v/%v, want alias@0.20", r.In, conf, ok)
	}
}

func TestQuantisedModelPricesAsBase(t *testing.T) {
	tbl := testTable()
	for _, id := range []string{"luna-q4", "luna-q4_k_m", "luna-iq4_xs", "luna-bf16"} {
		r, conf, ok := tbl.Lookup(id, "", day("2026-08-27"))
		if !ok || conf != ConfidenceAlias || r.In != 0.20 {
			t.Errorf("%s = %v/%v/%v, want alias@0.20", id, r.In, conf, ok)
		}
		m, resolved, found := tbl.Find(id)
		if !found || m.ID != "luna" || resolved != id {
			t.Errorf("Find(%s) = %v/%s/%v, want luna/%s/true", id, m, resolved, found, id)
		}
	}
	for _, id := range []string{"luna-q4x", "made-up-q4", "luna-4q", "luna-q4_madeup"} {
		if _, _, ok := tbl.Lookup(id, "", day("2026-08-27")); ok {
			t.Errorf("%s resolved, want unpriced", id)
		}
	}
}

func TestLocalModelFallbackPreservesExactRatesAndSizes(t *testing.T) {
	base := &Model{ID: "local-model-7b", Rates: []Rate{{From: MustParseDate("2026-01-01"), In: 1}}}
	quant := &Model{ID: "local-model-7b-q4", Rates: []Rate{{From: MustParseDate("2026-01-01"), In: 9}}}
	for _, tc := range []struct {
		name, query, provider, wantID string
		models, byProvider            map[string]*Model
		wantIn                        float64
		wantConfidence                Confidence
	}{
		{name: "size tag", query: "local-model:7b", models: map[string]*Model{base.ID: base}, wantID: base.ID, wantIn: 1, wantConfidence: ConfidenceAlias},
		{name: "size and quantisation", query: "local-model:7b-q4", models: map[string]*Model{base.ID: base}, wantID: base.ID, wantIn: 1, wantConfidence: ConfidenceAlias},
		{name: "unknown size", query: "local-model:3b", models: map[string]*Model{"local-model": base, base.ID: base}, wantID: "local-model:3b", wantConfidence: ConfidenceUnpriced},
		{name: "exact quantisation", query: quant.ID, models: map[string]*Model{base.ID: base, quant.ID: quant}, wantID: quant.ID, wantIn: 9, wantConfidence: ConfidenceExact},
		{name: "exact tagged model", query: "local-model:7b", models: map[string]*Model{base.ID: base, "local-model:7b": quant}, wantID: "local-model:7b", wantIn: 9, wantConfidence: ConfidenceExact},
		{name: "exact provider quantisation", query: quant.ID, provider: "vendor", models: map[string]*Model{base.ID: base}, byProvider: map[string]*Model{providerKey("vendor", quant.ID): quant}, wantID: quant.ID, wantIn: 9, wantConfidence: ConfidenceProvider},
		{name: "provider only base", query: quant.ID, provider: "vendor", byProvider: map[string]*Model{providerKey("vendor", base.ID): base}, wantID: base.ID, wantIn: 1, wantConfidence: ConfidenceProvider},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tbl := &Table{Models: tc.models, ByProvider: tc.byProvider}
			r, confidence, priced := tbl.Lookup(tc.query, tc.provider, day("2026-08-27"))
			if r.In != tc.wantIn || confidence != tc.wantConfidence || priced != (tc.wantConfidence != ConfidenceUnpriced) {
				t.Fatalf("Lookup = %v/%v/%v, want %v/%v", r.In, confidence, priced, tc.wantIn, tc.wantConfidence)
			}
			id, _ := tbl.Canonical(tc.query, tc.provider)
			if id != tc.wantID {
				t.Fatalf("Canonical = %q, want %q", id, tc.wantID)
			}
		})
	}
}

func TestAliasUsesCanonicalProviderRate(t *testing.T) {
	tbl := testTable()
	r, conf, ok := tbl.Lookup("luna-v2", "together", day("2026-08-27"))
	if !ok || conf != ConfidenceProvider || r.In != 1.40 {
		t.Errorf("provider alias lookup = %v/%v/%v, want provider@1.40", r.In, conf, ok)
	}
}

func TestAliasOverridesAnExistingRecordedModelID(t *testing.T) {
	tbl := &Table{
		Models: map[string]*Model{
			"masked": {ID: "masked", Rates: []Rate{{From: MustParseDate("2026-01-01"), Out: 1}}},
			"actual": {ID: "actual", Rates: []Rate{{From: MustParseDate("2026-01-01"), Out: 9}}},
		},
		ByProvider: map[string]*Model{},
		Aliases:    map[string]string{"masked": "actual"},
	}
	r, confidence, ok := tbl.Lookup("masked", "", day("2026-08-27"))
	if !ok || confidence != ConfidenceAlias || r.Out != 9 {
		t.Errorf("alias lookup = %v/%v/%v, want actual@9", r.Out, confidence, ok)
	}
	m, resolved, ok := tbl.Find("masked")
	if !ok || resolved != "masked" || m.ID != "actual" {
		t.Errorf("Find(masked) = %v/%q/%v, want actual/masked/true", m, resolved, ok)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5":                      "claude-opus-5",
		"CLAUDE-OPUS-5":                      "claude-opus-5",
		"  claude-opus-5  ":                  "claude-opus-5",
		"openrouter/anthropic/claude-opus-5": "claude-opus-5",
		"moonshotai/kimi-k2":                 "kimi-k2",
		"claude-opus-5-v1:0":                 "claude-opus-5-v1",
		"grok-4.3-latest":                    "grok-4.3",
		"qwen2.5-coder:3b":                   "qwen2.5-coder:3b",
		"qwen2.5-coder:7b":                   "qwen2.5-coder:7b",
		"qwen2.5-coder:latest":               "qwen2.5-coder",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// The shipped dataset must load, be current-schema, and carry the first-party
// rates rather than a reseller's. This is the regression guard for the
// alphabetical-provider bug.
func TestEmbeddedDatasetIsFirstParty(t *testing.T) {
	t.Setenv("TT_OFFLINE", "1")
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TT_PRICING_FILE", filepath.Join(t.TempDir(), "missing.json"))
	tbl, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(tbl.Models) < 100 {
		t.Fatalf("dataset has only %d models, expected the full table", len(tbl.Models))
	}
	for _, id := range []string{"codex-auto-review", "gemini-default", "gemini-pro-default", "antigravity-model-1020"} {
		if _, confidence, priced := tbl.Lookup(id, "", day("2026-10-07")); priced || confidence != ConfidenceUnpriced {
			t.Errorf("%s resolved without a verified model or rate", id)
		}
	}
	for query, want := range map[string]struct {
		id, source string
		in, out    float64
	}{
		"qwen2.5-coder:3b": {"qwen2.5-coder-3b-instruct", "codelace", 0.01, 0.032},
		"qwen2.5-coder:7b": {"qwen2-5-coder-7b-instruct", "alibaba-cn", 0.144, 0.287},
		"qwen3-30b-a3b-q4": {"qwen3-30b-a3b", "deepinfra", 0.12, 0.5},
	} {
		id, _ := tbl.Canonical(query, "")
		r, confidence, priced := tbl.Lookup(query, "", day("2026-10-07"))
		if !priced || confidence != ConfidenceAlias || id != want.id || r.Source != want.source || r.In != want.in || r.Out != want.out {
			t.Errorf("%s = %s/%v/%v/%v, want %v", query, id, r, confidence, priced, want)
		}
	}
	for _, id := range []string{"gpt-5.6-luna", "gpt-5.6-terra", "claude-opus-5"} {
		m, ok := tbl.Models[id]
		if !ok {
			t.Errorf("%s missing from the shipped dataset", id)
			continue
		}
		if !m.FirstParty {
			t.Errorf("%s priced from %q, expected a first-party vendor", id, m.Rates[0].Source)
		}
	}
}

// A user override must win outright. Provider-keyed rates are consulted before
// the flat table, so a shipped one would silently beat the override for any
// agent that records its provider — making the override look broken.
func TestUserOverrideBeatsProviderKeyedRate(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/pricing.json"
	body := `{"schema":2,"models":{"m1":{"id":"m1","rates":[{"from":"2026-01-01","in":9,"out":99}]}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TT_PRICING_FILE", path)

	tbl := &Table{
		Models:     map[string]*Model{"m1": {ID: "m1", Rates: []Rate{{From: MustParseDate("2026-01-01"), In: 1, Out: 1}}}},
		ByProvider: map[string]*Model{"anthropic\x00m1": {ID: "m1", Rates: []Rate{{From: MustParseDate("2026-01-01"), In: 2, Out: 2}}}},
		Aliases:    map[string]string{},
	}
	applyOverride(tbl)

	r, _, ok := tbl.Lookup("m1", "anthropic", day("2026-06-01"))
	if !ok || r.Out != 99 {
		t.Errorf("output rate = %v (ok=%v), want 99 from the override, not the provider table", r.Out, ok)
	}
}

// A rate carrying a Schedule prices at the peak figures inside the schedule's
// windows and the OffPeak figures outside them — this is DeepSeek's
// peak/off-peak split, effective 2026-08-16, where off-peak is half of peak.
func TestScheduleSubstitutesOffPeakRatesOutsideWindows(t *testing.T) {
	tbl := &Table{
		Models: map[string]*Model{
			"deepseek-v4-flash": {
				ID: "deepseek-v4-flash",
				Rates: []Rate{{
					From: MustParseDate("2026-08-16"),
					In:   0.44, Out: 1.32, CacheRead: 0.014, CacheWrite: 0.44,
					Schedule: "deepseek_peak",
					OffPeak:  &OffPeakRate{In: 0.22, Out: 0.66, CacheRead: 0.007, CacheWrite: 0.22},
				}},
			},
		},
		ByProvider: map[string]*Model{},
		Aliases:    map[string]string{},
		Schedules: map[string]Schedule{
			"deepseek_peak": {
				Days:    []int{0, 1, 2, 3, 4}, // Monday-Friday
				Windows: [][]string{{"01:00", "04:00"}, {"06:00", "10:00"}},
			},
		},
	}

	// Tuesday 02:00 UTC — inside the first peak window.
	peak := time.Date(2026, 8, 18, 2, 0, 0, 0, time.UTC)
	r, _, ok := tbl.Lookup("deepseek-v4-flash", "", peak)
	if !ok || r.Out != 1.32 {
		t.Errorf("peak output = %v (ok=%v), want 1.32", r.Out, ok)
	}

	// Tuesday 05:00 UTC — the gap between the two peak windows.
	offPeak := time.Date(2026, 8, 18, 5, 0, 0, 0, time.UTC)
	r, _, ok = tbl.Lookup("deepseek-v4-flash", "", offPeak)
	if !ok || r.Out != 0.66 || r.CacheRead != 0.007 {
		t.Errorf("off-peak output/cacheRead = %v/%v (ok=%v), want 0.66/0.007", r.Out, r.CacheRead, ok)
	}

	// Saturday — not a scheduled day at all, so every hour is off-peak.
	weekend := time.Date(2026, 8, 22, 2, 0, 0, 0, time.UTC)
	r, _, ok = tbl.Lookup("deepseek-v4-flash", "", weekend)
	if !ok || r.Out != 0.66 {
		t.Errorf("weekend output = %v (ok=%v), want 0.66 (off-peak all day)", r.Out, ok)
	}

	// A zero time (unknown timestamp) takes the safer, cheaper off-peak side
	// rather than guessing peak.
	r, _, ok = tbl.Lookup("deepseek-v4-flash", "", time.Time{})
	if !ok || r.Out != 0.66 {
		t.Errorf("zero-time output = %v (ok=%v), want 0.66 (off-peak default)", r.Out, ok)
	}
}
