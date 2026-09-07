package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
	"github.com/VasiHemanth/tokentelemetry/engine/internal/report"
)

func TestOverviewLimitsDoNotHideTotalsOrUnpricedUsage(t *testing.T) {
	t.Setenv("TT_PLANS_FILE", t.TempDir()+"/missing.json")
	t.Setenv("COLUMNS", "80")
	rep := &report.Report{
		MatchedTurns: 3,
		Totals:       report.Totals{Cost: 12, Turns: 3, UnpricedTurns: 1, UnpricedModels: []string{"unknown"}},
		Series:       []report.Bucket{{Key: "2026-08-01", Usage: model.Usage{Input: 100}}, {Key: "2026-08-02", Usage: model.Usage{Input: 200}, Unpriced: 1}},
		ByModel:      []report.Bucket{{Key: "expensive", Cost: 12}, {Key: "unknown", Unpriced: 1}},
		ByAgent:      []report.Bucket{{Key: "claude", Cost: 12}},
	}
	var out bytes.Buffer
	renderOverview(&out, rep, 1, false, false, true, nil)
	for _, want := range []string{"$12.00", "unknown", "latest 1 of 2", "top 1 of 2", "2026-08-02", "####################", "Includes unpriced usage"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "2026-08-01") {
		t.Fatal("display limit ignored")
	}
	data, err := json.Marshal(view("summary", rep))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"series"`, `"by_model"`, `"by_agent"`, "2026-08-01"} {
		if !strings.Contains(string(data), key) {
			t.Errorf("summary JSON missing %s", key)
		}
	}
	if len(rep.Series) != 2 {
		t.Fatal("render mutated report")
	}
}

func TestOverviewPlainNarrowAndZeroCost(t *testing.T) {
	t.Setenv("TT_PLANS_FILE", t.TempDir()+"/missing.json")
	rep := &report.Report{MatchedTurns: 1, ByModel: []report.Bucket{{Key: "free\nmodel\x1b", Unpriced: 1}}}
	for _, tc := range []struct {
		columns string
		bars    bool
	}{{"80", false}, {"40", true}, {"80", true}} {
		t.Setenv("COLUMNS", tc.columns)
		var out bytes.Buffer
		renderOverview(&out, rep, 0, false, false, tc.bars, nil)
		if strings.Contains(out.String(), "#") || strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "NaN") {
			t.Fatalf("invalid zero cost/plain output: %q", out.String())
		}
	}
}

func TestReportRejectsInvalidArgumentsBeforeScanning(t *testing.T) {
	for _, args := range [][]string{{"--since", "2026-09-02", "--until", "2026-09-01"}, {"--limit", "-1"}, {"unexpected"}} {
		if got := cmdReport("summary", args); got != 2 {
			t.Errorf("%v: exit %d", args, got)
		}
	}
}

func TestOverviewDisclosesApproximateAccounting(t *testing.T) {
	t.Setenv("TT_PLANS_FILE", t.TempDir()+"/missing.json")
	rep := &report.Report{MatchedTurns: 2, Totals: report.Totals{Turns: 2, AggregateRecords: 1, AggregateTokens: 100, HeuristicRecords: 1}}
	var out bytes.Buffer
	renderOverview(&out, rep, 0, false, false, false, nil)
	for _, want := range []string{"Usage records", "dates and costs approximate", "timing-based replay detection"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
}
