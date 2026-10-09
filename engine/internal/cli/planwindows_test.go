package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestRenderPlanWindowsDropsExpiredAndShowsAge(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local)
	windows := []model.PlanWindow{
		{Agent: model.AgentCodex, Window: "5h", UsedPercent: 42.4, ResetsAt: now.Add(2 * time.Hour), ObservedAt: now.Add(-12 * time.Minute), Plan: "pro"},
		{Agent: model.AgentCodex, Window: "7d", UsedPercent: 91, ResetsAt: now.Add(3 * 24 * time.Hour), ObservedAt: now.Add(-12 * time.Minute), Plan: "pro"},
		{Agent: model.AgentClaude, Window: "5h", UsedPercent: 10, ResetsAt: now.Add(-time.Minute), ObservedAt: now.Add(-3 * time.Hour)},
		{Agent: model.AgentClaude, Window: "7d", UsedPercent: 55, ObservedAt: now.Add(-3 * time.Hour)},
	}
	var buf bytes.Buffer
	renderPlanWindows(&buf, theme{}, windows, now)
	out := buf.String()
	for _, want := range []string{"Plan windows", "AGENT", "PLAN", "42%", "14:00", "12m ago", "pro", "91%", "55%", "unknown", "3h ago", now.Add(3 * 24 * time.Hour).Format("Mon 15:04")} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "10%") {
		t.Fatalf("expired window rendered:\n%s", out)
	}
}

func TestRenderPlanWindowsSilentWhenNothingCurrent(t *testing.T) {
	now := time.Now()
	var buf bytes.Buffer
	renderPlanWindows(&buf, theme{}, []model.PlanWindow{
		{Agent: model.AgentCodex, Window: "5h", UsedPercent: 1, ResetsAt: now.Add(-time.Hour), ObservedAt: now},
	}, now)
	if buf.Len() != 0 {
		t.Fatalf("expected no output, got:\n%s", buf.String())
	}
	renderPlanWindows(&buf, theme{}, nil, now)
	if buf.Len() != 0 {
		t.Fatalf("expected no output for nil windows, got:\n%s", buf.String())
	}
}

func TestResetLabelDistances(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.Local)
	cases := map[string]time.Time{
		"15:30":        time.Date(2026, 10, 9, 15, 30, 0, 0, time.Local),
		"Sat 09:00":    time.Date(2026, 10, 10, 9, 0, 0, 0, time.Local),
		"Oct 20 09:00": time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local),
		"unknown":      {},
	}
	for want, at := range cases {
		if got := resetLabel(at, now); got != want {
			t.Errorf("resetLabel(%v) = %q, want %q", at, got, want)
		}
	}
}
