package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestPlanWindowExpired(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		w    model.PlanWindow
		want bool
	}{
		{"reset ahead", model.PlanWindow{Window: "5h", ResetsAt: now.Add(time.Minute)}, false},
		{"reset now", model.PlanWindow{Window: "5h", ResetsAt: now}, true},
		{"reset passed", model.PlanWindow{Window: "7d", ResetsAt: now.Add(-time.Second)}, true},
		{"no reset, 5h seen 4h ago", model.PlanWindow{Window: "5h", ObservedAt: now.Add(-4 * time.Hour)}, false},
		{"no reset, 5h seen 6h ago", model.PlanWindow{Window: "5h", ObservedAt: now.Add(-6 * time.Hour)}, true},
		{"no reset, 7d seen 6 days ago", model.PlanWindow{Window: "7d", ObservedAt: now.Add(-6 * 24 * time.Hour)}, false},
		{"no reset, unknown label seen 8 days ago", model.PlanWindow{Window: "primary", ObservedAt: now.Add(-8 * 24 * time.Hour)}, true},
		{"no reset, 90m seen 2h ago", model.PlanWindow{Window: "90m", ObservedAt: now.Add(-2 * time.Hour)}, true},
	}
	for _, c := range cases {
		if got := PlanWindowExpired(c.w, now); got != c.want {
			t.Errorf("%s: expired = %v, want %v", c.name, got, c.want)
		}
	}
	live := LivePlanWindows([]model.PlanWindow{cases[0].w, cases[1].w, cases[3].w}, now)
	if len(live) != 2 {
		t.Fatalf("live windows = %+v", live)
	}
}

func TestPlanResetTimeRange(t *testing.T) {
	if got := PlanResetTime(1791561600); !got.Equal(time.Unix(1791561600, 0)) {
		t.Fatalf("in-range reset = %v", got)
	}
	for _, bad := range []int64{0, -1, 1738425600000, 946684799, 4102444801} {
		if got := PlanResetTime(bad); !got.IsZero() {
			t.Errorf("PlanResetTime(%d) = %v, want zero", bad, got)
		}
	}
}

func TestPlanSnapshotRoundTripAndStaleTemp(t *testing.T) {
	dir := t.TempDir()
	if got, err := ReadPlanSnapshot(dir, model.AgentClaude); got != nil || err != nil {
		t.Fatalf("missing snapshot = %v, %v", got, err)
	}
	stale := filepath.Join(dir, "claude.stale.tmp")
	live := filepath.Join(dir, "claude.live.tmp")
	for _, p := range []string{stale, live} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	want := []model.PlanWindow{{Agent: model.AgentClaude, Window: "5h", UsedPercent: 12.5, ObservedAt: seen}}
	if err := WritePlanSnapshot(dir, model.AgentClaude, want); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp file kept: %v", err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("fresh temp file removed: %v", err)
	}
	got, err := ReadPlanSnapshot(dir, model.AgentClaude)
	if err != nil || len(got) != 1 {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	if g := got[0]; g.Agent != want[0].Agent || g.Window != want[0].Window || g.UsedPercent != want[0].UsedPercent ||
		!g.ObservedAt.Equal(want[0].ObservedAt) || !g.ResetsAt.IsZero() {
		t.Fatalf("round trip = %+v, want %+v", g, want[0])
	}
	data, err := os.ReadFile(PlanSnapshotPath(dir, model.AgentClaude))
	if err != nil || strings.Contains(string(data), "resets_at") {
		t.Fatalf("unknown reset should be omitted from JSON (%v):\n%s", err, data)
	}
}
