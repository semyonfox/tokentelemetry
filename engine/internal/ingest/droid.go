package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

type Droid struct{ root string }

func NewDroid() *Droid              { return &Droid{root: envDir("FACTORY_DIR", ".factory")} }
func newDroidAt(root string) *Droid { return &Droid{root: root} }
func (d *Droid) Agent() model.Agent { return model.Agent("droid") }
func (d *Droid) Roots() []string {
	if d.root == "" {
		return nil
	}
	s := existingDir(filepath.Join(d.root, "sessions"))
	if s == "" {
		return nil
	}
	return []string{s}
}

type droidSettings struct {
	Model         string `json:"model"`
	RawTokenUsage *struct {
		Input         int64 `json:"inputTokens"`
		Output        int64 `json:"outputTokens"`
		CacheCreation int64 `json:"cacheCreationTokens"`
		CacheRead     int64 `json:"cacheReadTokens"`
		Thinking      int64 `json:"thinkingTokens"`
	} `json:"tokenUsage"`
}
type droidRecord struct{ Type, ID, Timestamp, CWD string }

func (d *Droid) Scan(ctx context.Context, emit func(model.Turn)) error {
	roots := d.Roots()
	if len(roots) == 0 {
		return nil
	}
	var paths []string
	var scanErr error
	err := filepath.WalkDir(roots[0], func(path string, e os.DirEntry, err error) error {
		if err != nil {
			scanErr = errors.Join(scanErr, err)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, turn := range mapFiles(ctx, paths, d.scanFile) {
		emit(turn)
	}
	return scanErr
}

func (d *Droid) scanFile(path string) []model.Turn {
	raw, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".settings.json")
	if err != nil {
		return nil
	}
	var settings droidSettings
	if json.Unmarshal(raw, &settings) != nil || settings.RawTokenUsage == nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	session, project := strings.TrimSuffix(filepath.Base(path), ".jsonl"), ""
	var stamp time.Time
	for line := range jsonLines(f) {
		var r droidRecord
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if r.Type == "session_start" {
			session = firstNonEmpty(r.ID, session)
			project = r.CWD
		}
		if t := parseTime(r.Timestamp); t.After(stamp) {
			stamp = t
		}
	}
	if filepath.Clean(project) == filepath.Clean(d.root) {
		return nil
	}
	u0 := settings.RawTokenUsage
	components, ok := (model.Usage{Input: u0.Input, Output: u0.Output, CacheRead: u0.CacheRead, CacheWrite: u0.CacheCreation, Reasoning: u0.Thinking}).SanitizeAggregate()
	if !ok {
		return nil
	}
	u := components
	u.Output += u.Reasoning
	u.ContextTokens = u.Input + u.CacheRead + u.CacheWrite
	u, ok = u.SanitizeAggregate()
	if !ok {
		return nil
	}
	if u.IsZero() {
		return nil
	}
	modelID := strings.TrimSpace(settings.Model)
	if modelID == "" {
		modelID = "unknown"
	}
	return []model.Turn{{Key: identityKey("droid-session", session), SessionID: session, Agent: model.Agent("droid"), Timestamp: stamp, Model: modelID, Project: project, Usage: u, Aggregate: true,
		UnpricedReason: "Droid stores session totals without per-call model attribution"}}
}
