package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// PlanReporter is implemented by scanners whose agent records its own
// subscription rate-limit windows. The windows are the agent's latest snapshot,
// not an estimate from token counts, and the caller shows when they were seen.
type PlanReporter interface {
	PlanWindows(ctx context.Context) ([]model.PlanWindow, error)
}

// PlanWindows collects every reporter's latest windows in scanner order. A
// failing reporter comes back as a warning so one agent never hides the rest.
func PlanWindows(ctx context.Context, scanners []Scanner) ([]model.PlanWindow, []error) {
	var out []model.PlanWindow
	var errs []error
	for _, s := range scanners {
		r, ok := s.(PlanReporter)
		if !ok {
			continue
		}
		windows, err := r.PlanWindows(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: plan windows: %w", s.Agent(), err))
			continue
		}
		out = append(out, windows...)
	}
	return out, errs
}

// planWindowLabel names a rolling window by its length: the two the agents
// actually sell are "5h" and "7d"; anything else is spelled out.
func planWindowLabel(minutes int64) string {
	switch {
	case minutes <= 0:
		return ""
	case minutes%(24*60) == 0:
		return fmt.Sprintf("%dd", minutes/(24*60))
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

// PlanResetTime converts an agent's unix-second reset to a time, treating
// anything outside 2000 to 2100 as unknown: a millisecond value or garbage
// would otherwise become a year encoding/json refuses to marshal.
func PlanResetTime(seconds int64) time.Time {
	if seconds < 946684800 || seconds > 4102444800 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

// PlanWindowExpired reports whether a window's figure is stale: its reset has
// passed, or, when the agent gave no reset, the window's own length has gone
// by since it was seen.
func PlanWindowExpired(w model.PlanWindow, now time.Time) bool {
	if !w.ResetsAt.IsZero() {
		return !w.ResetsAt.After(now)
	}
	return !w.ObservedAt.Add(planWindowLength(w.Window)).After(now)
}

// LivePlanWindows drops expired windows.
func LivePlanWindows(windows []model.PlanWindow, now time.Time) []model.PlanWindow {
	var out []model.PlanWindow
	for _, w := range windows {
		if !PlanWindowExpired(w, now) {
			out = append(out, w)
		}
	}
	return out
}

// planWindowLength reads the length back out of a planWindowLabel; any other
// label gets the longest window sold, a week.
func planWindowLength(label string) time.Duration {
	n, err := strconv.Atoi(strings.TrimRight(label, "mhd"))
	if err != nil || n <= 0 || len(label) < 2 {
		return 7 * 24 * time.Hour
	}
	switch label[len(label)-1] {
	case 'm':
		return time.Duration(n) * time.Minute
	case 'h':
		return time.Duration(n) * time.Hour
	case 'd':
		return time.Duration(n) * 24 * time.Hour
	}
	return 7 * 24 * time.Hour
}

// Plan snapshots hold what a hook last received for agents that do not write
// rate limits to disk themselves: one JSON array of windows per agent under
// the user cache directory.

// DefaultPlanDir is where snapshots live for this user.
func DefaultPlanDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tokentelemetry", "plan"), nil
}

// PlanSnapshotPath is the snapshot file for one agent.
func PlanSnapshotPath(dir string, agent model.Agent) string {
	return filepath.Join(dir, string(agent)+".json")
}

// WritePlanSnapshot replaces an agent's snapshot atomically.
func WritePlanSnapshot(dir string, agent model.Agent, windows []model.PlanWindow) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	removeStaleTemp(dir, agent)
	data, err := json.MarshalIndent(windows, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, string(agent)+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), PlanSnapshotPath(dir, agent)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// removeStaleTemp clears temp files a killed hook left behind. A minute is far
// longer than a write takes, so a live writer's file is never touched.
func removeStaleTemp(dir string, agent model.Agent) {
	matches, _ := filepath.Glob(filepath.Join(dir, string(agent)+".*.tmp"))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && time.Since(info.ModTime()) > time.Minute {
			os.Remove(m)
		}
	}
}

// ReadPlanSnapshot returns an agent's snapshot, or nil when none was recorded.
func ReadPlanSnapshot(dir string, agent model.Agent) ([]model.PlanWindow, error) {
	data, err := os.ReadFile(PlanSnapshotPath(dir, agent))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var windows []model.PlanWindow
	if err := json.Unmarshal(data, &windows); err != nil {
		return nil, fmt.Errorf("%s: %w", PlanSnapshotPath(dir, agent), err)
	}
	return windows, nil
}
