package cli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/ingest"
	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// the example values from the Claude Code status line docs
const statuslineDocExample = `{
  "model": {"id": "claude-opus-4-1", "display_name": "Opus"},
  "rate_limits": {
    "five_hour": {"used_percentage": 23.5, "resets_at": 1738425600},
    "seven_day": {"used_percentage": 41.2, "resets_at": 1738857600}
  }
}`

// an hour before the documented five-hour reset
var statuslineNow = time.Unix(1738425600, 0).Add(-time.Hour)

func TestStatuslineRecordsDocExample(t *testing.T) {
	dir := t.TempDir()
	got, err := recordStatusline(strings.NewReader(statuslineDocExample), dir, statuslineNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.PlanWindow{
		{Agent: model.AgentClaude, Window: "5h", UsedPercent: 23.5, ResetsAt: time.Unix(1738425600, 0), ObservedAt: statuslineNow},
		{Agent: model.AgentClaude, Window: "7d", UsedPercent: 41.2, ResetsAt: time.Unix(1738857600, 0), ObservedAt: statuslineNow},
	}
	assertPlanWindows(t, got, want)
	saved, err := ingest.ReadPlanSnapshot(dir, model.AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanWindows(t, saved, want)
}

func TestStatuslineKeepsUnexpiredRecordedWindows(t *testing.T) {
	dir := t.TempDir()
	earlier := statuslineNow.Add(-time.Hour)
	// input carries only 7d: the live 5h stays, the expired window goes
	recorded := []model.PlanWindow{
		{Agent: model.AgentClaude, Window: "5h", UsedPercent: 30, ResetsAt: statuslineNow.Add(2 * time.Hour), ObservedAt: earlier},
		{Agent: model.AgentClaude, Window: "custom", UsedPercent: 5, ResetsAt: statuslineNow.Add(-time.Second), ObservedAt: earlier},
	}
	if err := ingest.WritePlanSnapshot(dir, model.AgentClaude, recorded); err != nil {
		t.Fatal(err)
	}
	input := `{"rate_limits": {"seven_day": {"used_percentage": 41, "resets_at": 1738857600}}}`
	got, err := recordStatusline(strings.NewReader(input), dir, statuslineNow)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.PlanWindow{
		recorded[0],
		{Agent: model.AgentClaude, Window: "7d", UsedPercent: 41, ResetsAt: time.Unix(1738857600, 0), ObservedAt: statuslineNow},
	}
	assertPlanWindows(t, got, want)
	saved, err := ingest.ReadPlanSnapshot(dir, model.AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanWindows(t, saved, want)
}

func TestStatuslineSkipsUnchangedRewrite(t *testing.T) {
	dir := t.TempDir()
	if _, err := recordStatusline(strings.NewReader(statuslineDocExample), dir, statuslineNow); err != nil {
		t.Fatal(err)
	}
	path := ingest.PlanSnapshotPath(dir, model.AgentClaude)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := recordStatusline(strings.NewReader(statuslineDocExample), dir, statuslineNow.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("identical input within five minutes rewrote the snapshot")
	}

	// past the refresh interval the same input refreshes ObservedAt
	later := statuslineNow.Add(6 * time.Minute)
	if _, err := recordStatusline(strings.NewReader(statuslineDocExample), dir, later); err != nil {
		t.Fatal(err)
	}
	saved, err := ingest.ReadPlanSnapshot(dir, model.AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) == 0 || !saved[0].ObservedAt.Equal(later) {
		t.Fatalf("snapshot not refreshed after five minutes: %+v", saved)
	}
}

func TestStatuslineWithoutRateLimitsWritesNothing(t *testing.T) {
	for _, input := range []string{
		`{"model": {"id": "claude-opus-4-1"}}`,
		`{"rate_limits": {}}`,
		`{"rate_limits": {"spend_limit": {"used_percentage": 50, "resets_at": 1738857600}}}`,
	} {
		dir := t.TempDir()
		got, err := recordStatusline(strings.NewReader(input), dir, statuslineNow)
		if err != nil || got != nil {
			t.Fatalf("%s: got %v, %v; want nil, nil", input, got, err)
		}
		if _, err := os.Stat(ingest.PlanSnapshotPath(dir, model.AgentClaude)); !os.IsNotExist(err) {
			t.Fatalf("%s: snapshot written: %v", input, err)
		}
	}
}

func TestStatuslineInvalidJSON(t *testing.T) {
	if _, err := recordStatusline(strings.NewReader(`{"rate_limits":`), t.TempDir(), statuslineNow); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestStatuslineFormat(t *testing.T) {
	loc := time.FixedZone("test", 2*60*60)
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, loc) // a Tuesday
	windows := []model.PlanWindow{
		{Window: "5h", UsedPercent: 23.5, ResetsAt: time.Date(2026, 10, 6, 14, 5, 0, 0, loc)},
		// given in UTC to check it is shown in now's zone
		{Window: "7d", UsedPercent: 41.2, ResetsAt: time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)},
	}
	want := "5h 24% · resets 14:05 · 7d 41% · resets Thu 09:00"
	if got := formatStatusline(windows, now); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := formatStatusline(nil, now); got != "" {
		t.Fatalf("got %q for no windows", got)
	}
}

func assertPlanWindows(t *testing.T, got, want []model.PlanWindow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d windows %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Agent != w.Agent || g.Window != w.Window || g.UsedPercent != w.UsedPercent ||
			!g.ResetsAt.Equal(w.ResetsAt) || !g.ObservedAt.Equal(w.ObservedAt) {
			t.Errorf("window %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestStatuslineStaleSessionKeepsHigherFigure(t *testing.T) {
	dir := t.TempDir()
	reset := time.Unix(1738425600, 0)
	recorded := []model.PlanWindow{
		{Agent: model.AgentClaude, Window: "5h", UsedPercent: 62, ResetsAt: reset, ObservedAt: statuslineNow.Add(-10 * time.Minute)},
	}
	if err := ingest.WritePlanSnapshot(dir, model.AgentClaude, recorded); err != nil {
		t.Fatal(err)
	}
	// an idle session reports an older, lower figure for the same window
	got, err := recordStatusline(strings.NewReader(`{"rate_limits": {"five_hour": {"used_percentage": 40, "resets_at": 1738425600}}}`), dir, statuslineNow)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanWindows(t, got, []model.PlanWindow{{Agent: model.AgentClaude, Window: "5h", UsedPercent: 62, ResetsAt: reset, ObservedAt: statuslineNow}})

	// a report from an older window loses to the recorded newer one
	got, err = recordStatusline(strings.NewReader(`{"rate_limits": {"five_hour": {"used_percentage": 99, "resets_at": 1738407600}}}`), dir, statuslineNow)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanWindows(t, got, []model.PlanWindow{{Agent: model.AgentClaude, Window: "5h", UsedPercent: 62, ResetsAt: reset, ObservedAt: statuslineNow}})

	// a later reset is a new window and replaces the figure outright
	got, err = recordStatusline(strings.NewReader(`{"rate_limits": {"five_hour": {"used_percentage": 3, "resets_at": 1738443600}}}`), dir, statuslineNow)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanWindows(t, got, []model.PlanWindow{{Agent: model.AgentClaude, Window: "5h", UsedPercent: 3, ResetsAt: time.Unix(1738443600, 0), ObservedAt: statuslineNow}})
}

func TestStatuslineOutOfRangeResetIsUnknown(t *testing.T) {
	dir := t.TempDir()
	// milliseconds instead of seconds, and a value beyond int64
	input := `{"rate_limits": {"five_hour": {"used_percentage": 10, "resets_at": 1738425600000}, "seven_day": {"used_percentage": 20, "resets_at": 1e30}}}`
	got, err := recordStatusline(strings.NewReader(input), dir, statuslineNow)
	if err != nil {
		t.Fatal(err)
	}
	assertPlanWindows(t, got, []model.PlanWindow{
		{Agent: model.AgentClaude, Window: "5h", UsedPercent: 10, ObservedAt: statuslineNow},
		{Agent: model.AgentClaude, Window: "7d", UsedPercent: 20, ObservedAt: statuslineNow},
	})
	if _, err := ingest.ReadPlanSnapshot(dir, model.AgentClaude); err != nil {
		t.Fatalf("snapshot unreadable after unknown resets: %v", err)
	}
}

func TestStatuslineCorruptSnapshotOverwritten(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ingest.PlanSnapshotPath(dir, model.AgentClaude), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := recordStatusline(strings.NewReader(statuslineDocExample), dir, statuslineNow)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	saved, err := ingest.ReadPlanSnapshot(dir, model.AgentClaude)
	if err != nil || len(saved) != 2 {
		t.Fatalf("snapshot not replaced: %+v, %v", saved, err)
	}
}
