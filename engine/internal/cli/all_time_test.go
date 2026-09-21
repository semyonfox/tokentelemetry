package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/report"
)

func TestAllTimeReportsIncludeOldAndUndatedUsage(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "APPDATA", "LOCALAPPDATA", "T3CODE_HOME"} {
		t.Setenv(name, root)
	}
	t.Setenv("CLAUDE_CONFIG_DIRS", "")
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	t.Setenv("TT_OFFLINE", "1")
	t.Setenv("TT_PRICING_FILE", filepath.Join(root, "missing-pricing.json"))
	t.Setenv("TT_PLANS_FILE", filepath.Join(root, "missing-plans.json"))
	project := filepath.Join(root, "projects", "fixture")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.AddDate(-1, 0, 0)
	recent := now.AddDate(0, 0, -1)
	var records []string
	for _, row := range []struct {
		id, stamp string
		tokens    int
	}{
		{"old", old.Format(time.RFC3339), 100},
		{"recent", recent.Format(time.RFC3339), 200},
		{"undated", "", 300},
	} {
		records = append(records, fmt.Sprintf(`{"type":"assistant","timestamp":%q,"requestId":%q,"sessionId":"fixture","message":{"id":%q,"model":%q,"usage":{"input_tokens":%d}}}`, row.stamp, row.id, row.id, "fixture-"+row.id, row.tokens))
	}
	if err := os.WriteFile(filepath.Join(project, "session.jsonl"), []byte(strings.Join(records, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	type reportCase struct {
		name   string
		args   []string
		tokens int64
		turns  int
	}
	cases := []reportCase{
		{"summary default", []string{"summary"}, 200, 1},
		{"implicit summary", []string{"--all-time"}, 600, 3},
		{"short flag", []string{"-a"}, 600, 3},
		{"disabled flag", []string{"summary", "--all-time=false"}, 200, 1},
		{"preserve model filter", []string{"summary", "-a", "--model", "fixture-old"}, 100, 1},
		{"explicit date window", []string{"summary", "--since", old.Format("2006-01-02"), "--until", old.Format("2006-01-02")}, 100, 1},
	}
	for _, cmd := range []string{"summary", "daily", "weekly", "monthly", "session", "model", "project"} {
		cases = append(cases, reportCase{cmd + " all time", []string{cmd, "--all-time"}, 600, 3})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string(nil), tc.args...), "--agent", "claude", "--json")
			code, stdout, stderr := runCapturedCLI(t, args...)
			if code != 0 || stderr != "" {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
			var rep report.Report
			if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
				t.Fatal(err)
			}
			if rep.Totals.Usage.Total() != tc.tokens || rep.Totals.Turns != tc.turns {
				t.Fatalf("totals=%+v, want %d tokens across %d turns", rep.Totals, tc.tokens, tc.turns)
			}
		})
	}
}

func TestAllTimeRejectsDateBoundsBeforeScanning(t *testing.T) {
	for _, cmd := range []string{"summary", "daily"} {
		for _, flag := range []string{"--since", "--from", "--until", "--to"} {
			for _, allTime := range []string{"--all-time", "-a"} {
				code, stdout, stderr := runCapturedCLI(t, cmd, allTime, flag, "2026-01-01")
				if code != 2 || stdout != "" || !strings.Contains(stderr, "--all-time cannot be combined") {
					t.Fatalf("%s %s %s: exit=%d stdout=%q stderr=%q", cmd, allTime, flag, code, stdout, stderr)
				}
			}
		}
	}
}
