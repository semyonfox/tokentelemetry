package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Quickdesk reads the legacy desktop EMF metrics, never session text estimates.
// AWS does not publish a cache-token contract for these local metrics.
type Quickdesk struct{ root string }

func NewQuickdesk() *Quickdesk {
	root := os.Getenv("QUICKWORK_HOME")
	if root == "" {
		root = filepath.Join(homeDir(), ".quickwork")
	}
	return &Quickdesk{root: root}
}
func (*Quickdesk) Agent() model.Agent { return model.Agent("quickdesk") }
func (q *Quickdesk) Roots() []string {
	var roots []string
	seen := map[string]bool{}
	add := func(path string) {
		path = filepath.Clean(path)
		if !seen[path] && existingDir(filepath.Join(path, "metrics")) != "" {
			roots = append(roots, path)
			seen[path] = true
		}
	}
	add(q.root)
	var profiles struct {
		Entries []struct {
			Path string `json:"data_path"`
		} `json:"entries"`
	}
	if raw, err := os.ReadFile(filepath.Join(q.root, "profiles.json")); err == nil && json.Unmarshal(raw, &profiles) == nil {
		for _, p := range profiles.Entries {
			if p.Path == "" {
				continue
			}
			path := p.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(q.root, path)
			}
			add(path)
		}
	}
	return roots
}
func (q *Quickdesk) Scan(ctx context.Context, emit func(model.Turn)) error {
	var errs []error
	for _, root := range q.Roots() {
		paths, err := filepath.Glob(filepath.Join(root, "metrics", "metrics-????-??-??.jsonl"))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, path := range paths {
			if err := scanQuickdeskMetrics(ctx, path, emit); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
func scanQuickdeskMetrics(ctx context.Context, path string, emit func(model.Turn)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	occurrences := map[string]int{}
	for line := range jsonLines(f) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var row struct {
			Model   string `json:"Model"`
			Session string `json:"session_id"`
			Input   *int64 `json:"InputTokens"`
			Output  *int64 `json:"OutputTokens"`
			AWS     struct {
				Timestamp int64 `json:"Timestamp"`
			} `json:"_aws"`
		}
		if json.Unmarshal(line, &row) != nil || row.Model == "" || row.Input == nil || row.Output == nil || row.AWS.Timestamp <= 0 {
			continue
		}
		u, ok := (model.Usage{Unclassified: *row.Input, Output: *row.Output}).Sanitize()
		if !ok || u.IsZero() {
			continue
		}
		// No native request ID exists. Preserve repeated identical records within
		// a file while recognizing byte-identical copied daily metric files.
		digest := sha256.Sum256(line)
		fingerprint := hex.EncodeToString(digest[:])
		occurrences[fingerprint]++
		key := fmt.Sprintf("quickdesk|%s|%s|%d", filepath.Base(path), fingerprint, occurrences[fingerprint])
		emit(model.Turn{Key: key, SessionID: row.Session, Agent: model.Agent("quickdesk"), Timestamp: time.UnixMilli(row.AWS.Timestamp), Model: row.Model, Usage: u, UnpricedReason: "Quick Desktop: legacy metric input has no documented cache split; cost excluded"})
	}
	return nil
}
