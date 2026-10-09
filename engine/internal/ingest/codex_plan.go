package ingest

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// codexPlanFiles bounds how many recent rollouts are read for a snapshot: the
// newest session almost always has one, and older files are only a fallback.
const codexPlanFiles = 5

// codexRateWindow and codexRateLimits mirror RateLimitWindow and
// RateLimitSnapshot in openai/codex codex-rs/protocol/src/protocol.rs, carried
// on event_msg/token_count as payload.rate_limits. resets_at is unix seconds.
type codexRateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int64   `json:"window_minutes"`
	ResetsAt      *int64  `json:"resets_at"`
}

type codexRateLimits struct {
	Primary   *codexRateWindow `json:"primary"`
	Secondary *codexRateWindow `json:"secondary"`
	PlanType  string           `json:"plan_type"`
}

type codexRateRecord struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string           `json:"type"`
		RateLimits *codexRateLimits `json:"rate_limits"`
	} `json:"payload"`
}

// PlanWindows returns the rate-limit windows from the newest token_count
// snapshot in the most recently modified rollouts.
func (c *Codex) PlanWindows(ctx context.Context) ([]model.PlanWindow, error) {
	roots := c.Roots()
	if len(roots) == 0 {
		return nil, nil
	}
	type rollout struct {
		path  string
		mtime time.Time
	}
	var files []rollout
	err := filepath.WalkDir(roots[0], func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := d.Name()
		if d.IsDir() || !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		if info, err := d.Info(); err == nil {
			files = append(files, rollout{path, info.ModTime()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(files, func(a, b rollout) int { return b.mtime.Compare(a.mtime) })
	files = files[:min(len(files), codexPlanFiles)]

	var best *codexRateLimits
	var observed time.Time
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		limits, at := latestCodexRateLimits(ctx, f.path, f.mtime)
		if limits != nil && (best == nil || at.After(observed)) {
			best, observed = limits, at
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if best == nil {
		return nil, nil
	}
	var out []model.PlanWindow
	for _, w := range []struct {
		name string
		win  *codexRateWindow
	}{{"primary", best.Primary}, {"secondary", best.Secondary}} {
		if w.win == nil {
			continue
		}
		pw := model.PlanWindow{
			Agent:       model.AgentCodex,
			Window:      cmp.Or(planWindowLabel(w.win.WindowMinutes), w.name),
			UsedPercent: w.win.UsedPercent,
			ObservedAt:  observed,
			Plan:        best.PlanType,
		}
		if w.win.ResetsAt != nil {
			pw.ResetsAt = PlanResetTime(*w.win.ResetsAt)
		}
		out = append(out, pw)
	}
	return out, nil
}

// latestCodexRateLimits scans one rollout for its newest rate-limit snapshot.
// Only lines mentioning rate_limits are decoded so big transcripts stay cheap,
// and a cancelled ctx stops the read partway; the caller reports ctx.Err().
func latestCodexRateLimits(ctx context.Context, path string, mtime time.Time) (*codexRateLimits, time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}
	}
	defer f.Close()
	var best *codexRateLimits
	var bestAt time.Time
	n := 0
	for line := range jsonLines(f) {
		if n++; n%1024 == 0 && ctx.Err() != nil {
			return nil, time.Time{}
		}
		if !bytes.Contains(line, []byte(`"rate_limits"`)) {
			continue
		}
		var r codexRateRecord
		if json.Unmarshal(line, &r) != nil || r.Type != "event_msg" || r.Payload.Type != "token_count" || r.Payload.RateLimits == nil {
			continue
		}
		at, err := time.Parse(time.RFC3339, r.Timestamp)
		if err != nil {
			at = mtime
		}
		if best == nil || !at.Before(bestAt) {
			best, bestAt = r.Payload.RateLimits, at
		}
	}
	return best, bestAt
}
