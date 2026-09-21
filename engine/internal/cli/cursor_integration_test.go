package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/report"
)

func TestCursorCSVThroughCLI(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("XDG_CACHE_HOME", root)
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("APPDATA", root)
	t.Setenv("T3CODE_HOME", root)
	t.Setenv("TT_OFFLINE", "1")
	t.Setenv("TT_PRICING_FILE", filepath.Join(root, "missing-pricing.json"))
	t.Setenv("TT_PLANS_FILE", filepath.Join(root, "missing-plans.json"))
	path := filepath.Join(root, "usage.csv")
	t.Setenv("TT_CURSOR_CSV", path)
	header := "Date,Model,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens,Cost\n"
	// Identical rows can be separate requests. CSV Cost is a recorded charge,
	// not a model rate, so it cannot price an otherwise unknown model.
	row := "2026-09-01T12:00:00Z,cursor-fixture-unknown,5,10,20,7,42,999\n"
	for _, tc := range []struct {
		name string
		rows int
	}{{"two identical requests", 2}, {"replacement snapshot", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(header+strings.Repeat(row, tc.rows)), 0600); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := runCapturedCLI(t, "daily", "--agent", "cursor", "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			var rep report.Report
			if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
				t.Fatalf("invalid report: %v\n%s", err, stdout)
			}
			n := int64(tc.rows)
			u := rep.Totals.Usage
			if u.Input != 10*n || u.Output != 7*n || u.CacheRead != 20*n || u.CacheWrite != 5*n || u.Total() != 42*n || rep.Totals.Turns != tc.rows {
				t.Fatalf("incorrect CSV accounting: %+v", rep.Totals)
			}
			if rep.Totals.Cost != 0 || rep.Totals.UnpricedTurns != tc.rows || len(rep.ByAgent) != 1 || rep.ByAgent[0].Key != "cursor" {
				t.Fatalf("incorrect Cursor attribution or pricing: %+v", rep)
			}
		})
	}
}

func TestCLIRejectsOverlappingCursorImports(t *testing.T) {
	for _, args := range [][]string{
		{"daily", "--agent", "cursor,cursor-agent", "--json"},
		{"summary", "--agent", "CURSOR-AGENT", "--agent", "Cursor"},
	} {
		code, stdout, stderr := runCapturedCLI(t, args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "select --agent cursor or --agent cursor-agent separately") {
			t.Fatalf("overlapping imports accepted: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	}
}

// Tests calling Main must not run in parallel because it uses process streams.
func runCapturedCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	root := t.TempDir()
	stdout, err := os.Create(filepath.Join(root, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(root, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	defer func() { os.Stdout, os.Stderr = origOut, origErr }()
	code := Main(append([]string{"tokentelemetry"}, args...))
	out, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(out), string(diagnostics)
}
