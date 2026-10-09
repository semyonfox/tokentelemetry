package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func codexRateLimitsLine(ts, limits string) string {
	return `{"timestamp":"` + ts + `","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":` + limits + `}}`
}

// writeRollout writes a rollout under root/sessions and pins its mtime so the
// newest-first order does not depend on write timing.
func writeRollout(t *testing.T, root, name string, mtime time.Time, lines ...string) {
	t.Helper()
	path := filepath.Join(root, "sessions", "2026", "10", "09", name)
	writeFile(t, path, lines...)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func codexPlan(t *testing.T, root string) []model.PlanWindow {
	t.Helper()
	windows, err := newCodexAt(root).PlanWindows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return windows
}

func TestCodexPlanWindowsMapsPrimaryAndSecondary(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	writeRollout(t, root, "rollout-a.jsonl", base,
		codexMeta("s1", "", "cli", "gpt-5"),
		codexTokens("2026-10-09T11:00:00Z", 10, 0, 5),
		codexRateLimitsLine("2026-10-09T11:30:00Z",
			`{"primary":{"used_percent":42.5,"window_minutes":300,"resets_at":1791561600},`+
				`"secondary":{"used_percent":7,"window_minutes":10080},"plan_type":"pro"}`),
	)
	got := codexPlan(t, root)
	observed := time.Date(2026, 10, 9, 11, 30, 0, 0, time.UTC)
	want := []model.PlanWindow{
		{Agent: model.AgentCodex, Window: "5h", UsedPercent: 42.5, ResetsAt: time.Unix(1791561600, 0), ObservedAt: observed, Plan: "pro"},
		{Agent: model.AgentCodex, Window: "7d", UsedPercent: 7, ObservedAt: observed, Plan: "pro"},
	}
	if len(got) != len(want) {
		t.Fatalf("windows = %+v", got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Agent != w.Agent || g.Window != w.Window || g.UsedPercent != w.UsedPercent ||
			!g.ResetsAt.Equal(w.ResetsAt) || !g.ObservedAt.Equal(w.ObservedAt) || g.Plan != w.Plan {
			t.Fatalf("window %d = %+v, want %+v", i, g, w)
		}
	}
	if !got[1].ResetsAt.IsZero() {
		t.Fatalf("missing resets_at should be zero, got %v", got[1].ResetsAt)
	}
}

func TestCodexPlanWindowsNewestFileWins(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	writeRollout(t, root, "rollout-old.jsonl", base.Add(-time.Hour),
		codexRateLimitsLine("2026-10-09T10:00:00Z", `{"primary":{"used_percent":10,"window_minutes":300}}`))
	writeRollout(t, root, "rollout-new.jsonl", base,
		codexRateLimitsLine("2026-10-09T11:00:00Z", `{"primary":{"used_percent":60,"window_minutes":300}}`))
	got := codexPlan(t, root)
	if len(got) != 1 || got[0].UsedPercent != 60 {
		t.Fatalf("windows = %+v", got)
	}
}

func TestCodexPlanWindowsSkipsFileWithoutRateLimits(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	writeRollout(t, root, "rollout-old.jsonl", base.Add(-time.Hour),
		codexRateLimitsLine("2026-10-09T10:00:00Z", `{"primary":{"used_percent":33}}`))
	writeRollout(t, root, "rollout-new.jsonl", base,
		codexMeta("s2", "", "cli", "gpt-5"),
		codexTokens("2026-10-09T11:00:00Z", 10, 0, 5),
		codexRateLimitsLine("2026-10-09T11:05:00Z", `null`))
	got := codexPlan(t, root)
	if len(got) != 1 || got[0].UsedPercent != 33 || got[0].Window != "primary" {
		t.Fatalf("windows = %+v", got)
	}
}

func TestCodexPlanWindowsNoSessions(t *testing.T) {
	windows, err := newCodexAt(t.TempDir()).PlanWindows(context.Background())
	if windows != nil || err != nil {
		t.Fatalf("got %+v, %v", windows, err)
	}
}

var _ PlanReporter = (*Codex)(nil)

func TestCodexPlanWindowsLatestRecordWinsAcrossFiles(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	// the file touched most recently holds the older snapshot
	writeRollout(t, root, "rollout-touched.jsonl", base,
		codexRateLimitsLine("2026-10-09T09:00:00Z", `{"primary":{"used_percent":10,"window_minutes":300}}`))
	writeRollout(t, root, "rollout-quiet.jsonl", base.Add(-time.Hour),
		codexRateLimitsLine("2026-10-09T11:00:00Z", `{"primary":{"used_percent":60,"window_minutes":300}}`))
	got := codexPlan(t, root)
	if len(got) != 1 || got[0].UsedPercent != 60 {
		t.Fatalf("windows = %+v", got)
	}
}

func TestCodexPlanWindowsReadsOnlyNewestFiles(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	// the only snapshot sits in the sixth-newest file, past the cap
	writeRollout(t, root, "rollout-oldest.jsonl", base.Add(-6*time.Hour),
		codexRateLimitsLine("2026-10-09T06:00:00Z", `{"primary":{"used_percent":10,"window_minutes":300}}`))
	for i := 1; i <= codexPlanFiles; i++ {
		writeRollout(t, root, "rollout-"+string(rune('a'+i))+".jsonl", base.Add(-time.Duration(i)*time.Hour),
			codexTokens("2026-10-09T11:00:00Z", 10, 0, 5))
	}
	if got := codexPlan(t, root); got != nil {
		t.Fatalf("snapshot beyond the file cap was read: %+v", got)
	}
}

func TestCodexPlanWindowsOutOfRangeResetIsUnknown(t *testing.T) {
	root := t.TempDir()
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	writeRollout(t, root, "rollout-a.jsonl", base,
		codexRateLimitsLine("2026-10-09T11:30:00Z", `{"primary":{"used_percent":5,"window_minutes":300,"resets_at":1791561600000}}`))
	got := codexPlan(t, root)
	if len(got) != 1 || !got[0].ResetsAt.IsZero() {
		t.Fatalf("windows = %+v", got)
	}
}
