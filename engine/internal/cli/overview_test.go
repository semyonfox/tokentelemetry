package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
	"github.com/semyonfox/tokentelemetry/engine/internal/report"
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
	out.Reset()
	renderOverview(&out, rep, 0, false, false, true, nil)
	if !strings.Contains(out.String(), "2026-08-01") || !strings.Contains(out.String(), "latest 2 of 2") {
		t.Fatalf("--limit 0 should show every row:\n%s", out.String())
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

func TestOverviewStacksDailyCostByAgent(t *testing.T) {
	t.Setenv("TT_PLANS_FILE", t.TempDir()+"/missing.json")
	t.Setenv("COLUMNS", "100")
	t.Setenv("TERM", "xterm-256color")
	grey, orange := ansi256(245), ansi256(208)
	rep := &report.Report{
		MatchedTurns: 3,
		Totals:       report.Totals{Cost: 10, Turns: 3},
		Series: []report.Bucket{
			{Key: "2026-08-01", Cost: 10, CostByAgent: map[string]float64{"codex": 7.5, "claude": 2.5}, Usage: model.Usage{Output: 1000}},
			{Key: "2026-08-02", Usage: model.Usage{Output: 5}},
		},
		ByModel: []report.Bucket{
			{Key: "gpt", Cost: 7.5, CostByAgent: map[string]float64{"codex": 7.5}},
			{Key: "opus", Cost: 2.5, CostByAgent: map[string]float64{"claude": 2.5}},
		},
		ByAgent: []report.Bucket{{Key: "codex", Cost: 7.5}, {Key: "claude", Cost: 2.5}},
	}
	// without colour the stack is one plain bar and there is no legend to read
	var out bytes.Buffer
	renderOverview(&out, rep, 0, false, false, true, nil)
	s := out.String()
	for _, want := range []string{"Daily list cost by agent", "####################     $10.00", "$7.50"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Contains(s, "## codex") || strings.Contains(s, "\x1b") {
		t.Fatalf("legend or escapes without colour:\n%s", s)
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "  2026-08-02") && strings.Contains(line, "#") {
			t.Errorf("zero-cost day drew a bar: %q", line)
		}
	}

	// with colour: 20 cells, codex's 7.5 of 10 ends at cell 15, claude fills the rest
	out.Reset()
	renderOverview(&out, rep, 0, true, false, true, nil)
	s = out.String()
	// codex draws in its grey and claude in orange, the brand-nearest colours
	for _, want := range []string{
		grey + "###############" + ansiReset + orange + "#####" + ansiReset,
		grey + "##" + ansiReset + " codex   " + orange + "##" + ansiReset + " claude",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%q", want, s)
		}
	}
	// models scale to the top model: opus is 2.5 of gpt's 7.5, so 7 of 20 cells
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "  opus") && !strings.Contains(line, orange+"#######"+ansiReset) {
			t.Errorf("model should take its agent's colour: %q", line)
		}
	}
}

func TestAgentPaletteBrandsAndClashes(t *testing.T) {
	agents := []report.Bucket{{Key: "gemini"}, {Key: "ibm-bob"}, {Key: "copilot"}, {Key: "qwen"}, {Key: "hermes"}, {Key: "claude"}}
	distinct := func(p agentPalette) {
		t.Helper()
		seen := map[string]bool{}
		for agent, c := range p {
			if seen[c] {
				t.Fatalf("%s shares a colour: %v", agent, p)
			}
			seen[c] = true
		}
	}

	// basic 16: copilot and qwen both map to bright magenta, so the costlier
	// keeps it and qwen takes the first free colour; hermes has no brand
	t.Setenv("TERM", "xterm")
	t.Setenv("COLORTERM", "")
	p := newAgentPalette(agents)
	for agent, want := range map[string]string{"gemini": ansiBlue, "ibm-bob": ansiBrightBlue, "copilot": ansiBrightMagenta, "qwen": ansiCyan, "hermes": ansiGreen, "claude": ansiYellow} {
		if p.color(agent) != want {
			t.Errorf("basic %s = %q, want %q", agent, p.color(agent), want)
		}
	}
	distinct(p)

	// 256 colours: every brand has its own shade, so nothing moves
	t.Setenv("COLORTERM", "truecolor")
	p = newAgentPalette(agents)
	for agent, want := range map[string]string{"gemini": ansi256(33), "ibm-bob": ansi256(25), "copilot": ansi256(99), "qwen": ansi256(141), "hermes": ansiCyan, "claude": ansi256(208)} {
		if p.color(agent) != want {
			t.Errorf("rich %s = %q, want %q", agent, p.color(agent), want)
		}
	}
	distinct(p)
}
