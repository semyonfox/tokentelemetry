package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/ingest"
	"github.com/VasiHemanth/tokentelemetry/engine/internal/pricing"
	"github.com/VasiHemanth/tokentelemetry/engine/internal/report"
)

func TestSummaryFromSyntheticTranscripts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	t.Setenv("TT_PLANS_FILE", filepath.Join(root, "missing.json"))
	t.Setenv("COLUMNS", "80")
	dir := filepath.Join(root, "projects", "example")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// The first two records are streamed snapshots of one call. The final
	// record is an unknown model: keep its tokens without assigning a price.
	lines := []string{
		`{"type":"assistant","timestamp":"2026-09-01T12:00:00Z","requestId":"r1","sessionId":"s1","message":{"id":"m1","model":"fixture-model","usage":{"input_tokens":1000000,"output_tokens":100000}}}`,
		`{"type":"assistant","timestamp":"2026-09-01T12:00:00Z","requestId":"r1","sessionId":"s1","message":{"id":"m1","model":"fixture-model","usage":{"input_tokens":1000000,"output_tokens":2000000,"cache_read_input_tokens":3000000,"cache_creation_input_tokens":1000000}}}`,
		`{"type":"assistant","timestamp":"2026-09-02T12:00:00Z","requestId":"r2","sessionId":"s2","message":{"id":"m2","model":"fixture-unknown","usage":{"input_tokens":100}}}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := ingest.Run(context.Background(), []ingest.Scanner{ingest.NewClaude()})
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("scan: %v %v", err, res.Errors)
	}
	table := &pricing.Table{Updated: pricing.MustParseDate("2026-09-01"), Models: map[string]*pricing.Model{
		"fixture-model": {ID: "fixture-model", Rates: []pricing.Rate{{From: pricing.MustParseDate("2026-01-01"), In: 2, Out: 4, CacheRead: 1, CacheWrite: 3}}},
	}}
	rep := report.Build(res.Turns, table, report.Filter{}, report.Daily, res.Duplicates, nil)
	// Independently: 1M*2 + 2M*4 + 3M*1 + 1M*3 = $16. Tokens are
	// 1M+2M+3M+1M+100 = 7,000,100. The partial snapshot contributes nothing.
	if rep.Totals.Cost != 16 || rep.Totals.Usage.Total() != 7_000_100 || rep.Totals.Turns != 2 || rep.Totals.UnpricedTurns != 1 {
		t.Fatalf("unexpected accounting: %+v", rep.Totals)
	}
	var out bytes.Buffer
	renderOverview(&out, rep, 0, false, true, true, nil)
	for _, want := range []string{"$16.00", "fixture-unknown", "7.0M", "2 sessions"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %s in %s", want, out.String())
		}
	}
	t.Log("Synthetic report, independently verified at $16 and 7,000,100 tokens:\n" + out.String())
}
