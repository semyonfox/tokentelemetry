package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/ingest"
	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// statuslineRefresh is how stale an unchanged snapshot may get before it is
// rewritten anyway, so ObservedAt stays roughly current without a write on
// every hook run.
const statuslineRefresh = 5 * time.Minute

func cmdStatusline(args []string) int {
	fs := flag.NewFlagSet("statusline", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	rest, err := parseFlags(fs, args)
	if err == flag.ErrHelp {
		printHelp("statusline")
		return 0
	}
	if err != nil {
		return commandError("statusline", err)
	}
	if len(rest) > 0 {
		return commandError("statusline", fmt.Errorf("unexpected argument %q", rest[0]))
	}
	// a terminal on stdin means nobody piped the hook JSON in; reading would
	// just hang
	if isTerminal(os.Stdin) {
		return commandError("statusline", fmt.Errorf("expects Claude Code's status line JSON on stdin"))
	}
	dir, err := ingest.DefaultPlanDir()
	if err != nil {
		return commandError("statusline", err)
	}
	now := time.Now()
	windows, err := recordStatusline(os.Stdin, dir, now)
	if err != nil && windows == nil {
		return commandError("statusline", err)
	}
	if err != nil {
		// the snapshot could not be saved but the line is still worth showing
		fmt.Fprintf(os.Stderr, "tokentelemetry: warning: %v\n", err)
	}
	if line := formatStatusline(windows, now); line != "" {
		fmt.Println(line)
	}
	return 0
}

// statuslineInput is the part of Claude Code's status line JSON we read, see
// https://code.claude.com/docs/en/statusline. rate_limits only appears for
// Pro and Max subscribers after a session's first API response, and each
// window is dropped once it resets. spend_limit is for gateway users and is
// ignored.
type statuslineInput struct {
	RateLimits *struct {
		FiveHour *statuslineWindow `json:"five_hour"`
		SevenDay *statuslineWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

type statuslineWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	// documented as integer seconds; a float keeps a fractional value from
	// failing the whole hook
	ResetsAt float64 `json:"resets_at"`
}

// recordStatusline merges the rate-limit windows in r into the Claude plan
// snapshot under dir and returns the merged windows. It writes nothing when
// the input has no windows, and skips the write when nothing changed and the
// snapshot is fresh, since the hook can fire every few hundred milliseconds.
func recordStatusline(r io.Reader, dir string, now time.Time) ([]model.PlanWindow, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var in statuslineInput
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, fmt.Errorf("status line input: %w", err)
	}
	if in.RateLimits == nil {
		return nil, nil
	}
	var fresh []model.PlanWindow
	for _, w := range []struct {
		label string
		raw   *statuslineWindow
	}{{"5h", in.RateLimits.FiveHour}, {"7d", in.RateLimits.SevenDay}} {
		if w.raw == nil {
			continue
		}
		pw := model.PlanWindow{
			Agent:       model.AgentClaude,
			Window:      w.label,
			UsedPercent: w.raw.UsedPercentage,
			ObservedAt:  now,
		}
		// a missing or absurd resets_at stays unknown rather than becoming
		// 1970, or a year that cannot be written back out
		if r := w.raw.ResetsAt; r > 0 && r < math.MaxInt64 {
			pw.ResetsAt = ingest.PlanResetTime(int64(r))
		}
		fresh = append(fresh, pw)
	}
	if len(fresh) == 0 {
		return nil, nil
	}

	// an unreadable snapshot is overwritten rather than failing every run
	existing, err := ingest.ReadPlanSnapshot(dir, model.AgentClaude)
	if err != nil {
		existing = nil
	}
	merged := make([]model.PlanWindow, 0, len(fresh)+len(existing))
	for _, f := range fresh {
		for _, old := range existing {
			if old.Window == f.Window {
				f = reconcilePlanWindow(f, old)
			}
		}
		merged = append(merged, f)
	}
	for _, old := range existing {
		if hasPlanWindow(fresh, old.Window) || ingest.PlanWindowExpired(old, now) {
			continue
		}
		merged = append(merged, old)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return windowRank(merged[i].Window) < windowRank(merged[j].Window)
	})

	if samePlanWindows(merged, existing) && now.Sub(newestObserved(existing)) < statuslineRefresh {
		return merged, nil
	}
	// recording is best effort: the caller still gets the line to show
	if err := ingest.WritePlanSnapshot(dir, model.AgentClaude, merged); err != nil {
		return merged, err
	}
	return merged, nil
}

// reconcilePlanWindow picks between a fresh report and the recorded one for
// the same window. Every Claude Code session reports its own last response,
// so an idle session can send an older figure. Within one window usage only
// rises, so the higher figure stands; a later reset means a newer window.
// ObservedAt is the last report either way.
func reconcilePlanWindow(fresh, old model.PlanWindow) model.PlanWindow {
	switch {
	case old.ResetsAt.After(fresh.ResetsAt):
		old.ObservedAt = fresh.ObservedAt
		return old
	case old.ResetsAt.Equal(fresh.ResetsAt) && old.UsedPercent > fresh.UsedPercent:
		fresh.UsedPercent = old.UsedPercent
	}
	return fresh
}

func hasPlanWindow(windows []model.PlanWindow, label string) bool {
	for _, w := range windows {
		if w.Window == label {
			return true
		}
	}
	return false
}

func windowRank(label string) int {
	switch label {
	case "5h":
		return 0
	case "7d":
		return 1
	default:
		return 2
	}
}

// samePlanWindows compares everything but ObservedAt.
func samePlanWindows(a, b []model.PlanWindow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Agent != b[i].Agent || a[i].Window != b[i].Window || a[i].Plan != b[i].Plan ||
			a[i].UsedPercent != b[i].UsedPercent || !a[i].ResetsAt.Equal(b[i].ResetsAt) {
			return false
		}
	}
	return true
}

func newestObserved(windows []model.PlanWindow) time.Time {
	var newest time.Time
	for _, w := range windows {
		if w.ObservedAt.After(newest) {
			newest = w.ObservedAt
		}
	}
	return newest
}

// formatStatusline renders windows as one short line in now's location,
// dropping the weekday when the reset falls on the same day.
func formatStatusline(windows []model.PlanWindow, now time.Time) string {
	var parts []string
	ny, nm, nd := now.Date()
	for _, w := range windows {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", w.Window, w.UsedPercent))
		if w.ResetsAt.IsZero() {
			continue
		}
		at := w.ResetsAt.In(now.Location())
		layout := "Mon 15:04"
		if y, m, d := at.Date(); y == ny && m == nm && d == nd {
			layout = "15:04"
		}
		parts = append(parts, "resets "+at.Format(layout))
	}
	return strings.Join(parts, " · ")
}
