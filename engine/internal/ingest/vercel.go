package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// VercelGateway reads one explicitly selected /v1/report snapshot. Gateway
// activity overlaps agent logs, so it is excluded unless --agent selects it.
type VercelGateway struct{ path string }

func NewVercelGateway() *VercelGateway    { return &VercelGateway{path: os.Getenv("TT_VERCEL_REPORT")} }
func (*VercelGateway) Agent() model.Agent { return model.Agent("vercel-gateway") }
func (*VercelGateway) ExplicitOnly() bool { return true }
func (v *VercelGateway) Roots() []string {
	if info, err := os.Stat(v.path); err == nil && info.Mode().IsRegular() {
		return []string{v.path}
	}
	return nil
}
func (v *VercelGateway) Scan(ctx context.Context, emit func(model.Turn)) error {
	if len(v.Roots()) == 0 {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	f, err := os.Open(v.path)
	if err != nil {
		return err
	}
	defer f.Close()
	var report struct {
		Results []struct {
			Day    string `json:"day"`
			Hour   string `json:"hour"`
			Model  string `json:"model"`
			Input  *int64 `json:"input_tokens"`
			Output *int64 `json:"output_tokens"`
		} `json:"results"`
	}
	if err := json.NewDecoder(f).Decode(&report); err != nil {
		return fmt.Errorf("vercel-gateway: invalid report snapshot: %w", err)
	}
	seen := map[string]bool{}
	dimension := ""
	var turns []model.Turn
	for _, row := range report.Results {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if row.Input == nil || row.Output == nil || *row.Input < 0 || *row.Output < 0 {
			return fmt.Errorf("vercel-gateway: report rows require non-negative integer input_tokens and output_tokens")
		}
		group, key := 0, ""
		for _, field := range []string{row.Day, row.Hour, row.Model} {
			if field != "" {
				group++
				key = field
			}
		}
		if group != 1 || seen[key] {
			return fmt.Errorf("vercel-gateway: expected one complete snapshot grouped by day or model; overlapping or unsupported rows rejected")
		}
		currentDimension := "model"
		if row.Day != "" {
			currentDimension = "day"
		}
		if row.Hour != "" {
			currentDimension = "hour"
		}
		if dimension != "" && dimension != currentDimension {
			return fmt.Errorf("vercel-gateway: mixed report dimensions rejected")
		}
		dimension = currentDimension
		seen[key] = true
		var ts time.Time
		if row.Day != "" {
			ts, err = time.Parse("2006-01-02", row.Day)
			if err != nil {
				return fmt.Errorf("vercel-gateway: invalid UTC day")
			}
		}
		if row.Hour != "" {
			ts, err = time.Parse("2006-01-02T15", row.Hour)
			if err != nil {
				return fmt.Errorf("vercel-gateway: invalid UTC hour")
			}
		}
		id := row.Model
		if id == "" {
			id = "unknown"
		} else if _, tail, ok := strings.Cut(id, "/"); ok {
			id = tail
		}
		u := model.Usage{Unclassified: *row.Input, Output: *row.Output}
		if _, ok := u.SanitizeAggregate(); !ok {
			return fmt.Errorf("vercel-gateway: report counts exceed aggregate bounds")
		}
		if u.IsZero() {
			continue
		}
		turns = append(turns, model.Turn{Key: "vercel-gateway|" + key, Agent: v.Agent(), Timestamp: ts, Model: id, Usage: u, Aggregate: true, UnpricedReason: "Vercel report: one grouping dimension and undocumented cache inclusion; imported totals remain unpriced"})
	}
	for _, t := range turns {
		emit(t)
	}
	return nil
}
