package ingest

import (
	"context"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestClaudePlanWindowsReadsSnapshot(t *testing.T) {
	dir := t.TempDir()
	observed := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	want := []model.PlanWindow{
		{Agent: model.AgentClaude, Window: "5h", UsedPercent: 23.5, ResetsAt: time.Unix(1738425600, 0).UTC(), ObservedAt: observed},
		{Agent: model.AgentClaude, Window: "7d", UsedPercent: 41.2, ResetsAt: time.Unix(1738857600, 0).UTC(), ObservedAt: observed},
	}
	if err := WritePlanSnapshot(dir, model.AgentClaude, want); err != nil {
		t.Fatal(err)
	}
	got, err := (&Claude{planDir: dir}).PlanWindows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d windows, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Window != want[i].Window || got[i].UsedPercent != want[i].UsedPercent ||
			!got[i].ResetsAt.Equal(want[i].ResetsAt) || !got[i].ObservedAt.Equal(want[i].ObservedAt) {
			t.Errorf("window %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestClaudePlanWindowsWithoutDir(t *testing.T) {
	got, err := (&Claude{}).PlanWindows(context.Background())
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want nil, nil", got, err)
	}
}
