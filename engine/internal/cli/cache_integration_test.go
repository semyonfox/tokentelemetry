package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/report"
)

func TestCLICacheMatchesFreshReportsAndInvalidatesChangedHistory(t *testing.T) {
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
	path := filepath.Join(project, "session.jsonl")
	first := `{"type":"assistant","timestamp":"2020-01-01T12:00:00Z","requestId":"r1","sessionId":"s1","message":{"id":"m1","model":"fixture-unknown","usage":{"input_tokens":100}}}` + "\n"
	second := `{"type":"assistant","timestamp":"2020-01-02T12:00:00Z","requestId":"r2","sessionId":"s1","message":{"id":"m2","model":"fixture-unknown","usage":{"input_tokens":200}}}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) (report.Report, string) {
		t.Helper()
		args := append([]string{"summary", "--all-time", "--agent", "claude", "--json", "--verbose"}, extra...)
		code, stdout, stderr := runCapturedCLI(t, args...)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%s", code, stderr)
		}
		var rep report.Report
		if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
			t.Fatal(err)
		}
		return rep, stderr
	}
	cold, coldDiagnostics := run()
	warm, warmDiagnostics := run()
	fresh, freshDiagnostics := run("--no-cache")
	if cold.Totals.Usage.Total() != 100 || !reflect.DeepEqual(cold, warm) || !reflect.DeepEqual(cold, fresh) {
		t.Fatalf("cache changed report: cold=%+v warm=%+v fresh=%+v", cold, warm, fresh)
	}
	if !strings.Contains(coldDiagnostics, "scan cache:") || !strings.Contains(warmDiagnostics, "1 hit(s)") || strings.Contains(freshDiagnostics, "scan cache:") {
		t.Fatalf("unexpected cache diagnostics: cold=%q warm=%q fresh=%q", coldDiagnostics, warmDiagnostics, freshDiagnostics)
	}
	if err := os.WriteFile(path, []byte(first+second), 0600); err != nil {
		t.Fatal(err)
	}
	changed, _ := run()
	if changed.Totals.Turns != 2 || changed.Totals.Usage.Total() != 300 {
		t.Fatalf("changed history remained stale: %+v", changed.Totals)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	deleted, _ := run()
	if deleted.Totals.Turns != 0 || deleted.Totals.Usage.Total() != 0 {
		t.Fatalf("deleted history remained cached: %+v", deleted.Totals)
	}
}
