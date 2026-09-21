package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

type CodeWhale struct{ roots []string }

func NewCodeWhale() *CodeWhale {
	if h := strings.TrimSpace(os.Getenv("CODEWHALE_HOME")); h != "" {
		return &CodeWhale{roots: []string{existingDir(filepath.Join(h, "sessions"))}}
	}
	h := homeDir()
	if h == "" {
		return &CodeWhale{}
	}
	return &CodeWhale{roots: []string{existingDir(filepath.Join(h, ".codewhale", "sessions")), existingDir(filepath.Join(h, ".deepseek", "sessions"))}}
}
func newCodeWhaleAt(roots ...string) *CodeWhale { return &CodeWhale{roots: roots} }
func (c *CodeWhale) Agent() model.Agent         { return model.Agent("codewhale") }
func (c *CodeWhale) Roots() []string {
	var out []string
	for _, r := range c.roots {
		if r != "" {
			out = append(out, r)
		}
	}
	return out
}
func (c *CodeWhale) Scan(ctx context.Context, emit func(model.Turn)) error {
	seen := map[string]bool{}
	var scanErr error
	for _, root := range c.Roots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			scanErr = errors.Join(scanErr, err)
			continue
		}
		for _, e := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			p := filepath.Join(root, e.Name())
			b, err := os.ReadFile(p)
			if err != nil {
				scanErr = errors.Join(scanErr, err)
				continue
			}
			var saved struct {
				Metadata struct {
					ID            string `json:"id"`
					CreatedAt     string `json:"created_at"`
					UpdatedAt     string `json:"updated_at"`
					Model         string `json:"model"`
					ModelProvider string `json:"model_provider"`
					Workspace     string `json:"workspace"`
					TotalTokens   int64  `json:"total_tokens"`
				} `json:"metadata"`
			}
			if json.Unmarshal(b, &saved) != nil || saved.Metadata.ID == "" || seen[saved.Metadata.ID] {
				continue
			}
			m := saved.Metadata
			if m.TotalTokens <= 0 {
				continue
			}
			usage, ok := (model.Usage{Unclassified: m.TotalTokens}).SanitizeAggregate()
			if !ok {
				scanErr = errors.Join(scanErr, fmt.Errorf("codewhale: implausible aggregate in %s", p))
				continue
			}
			seen[m.ID] = true
			ts := parseTime(m.UpdatedAt)
			if ts.IsZero() {
				ts = parseTime(m.CreatedAt)
			}
			if ts.IsZero() {
				if fi, e := os.Stat(p); e == nil {
					ts = fi.ModTime()
				}
			}
			modelID := m.Model
			if modelID == "" {
				modelID = m.ModelProvider
			}
			reason := "CodeWhale records only a total token count without input/output/cache buckets"
			if modelID == "" {
				modelID = "unknown"
				reason += "; model is absent"
			}
			emit(model.Turn{Key: identityKey("codewhale", m.ID), SessionID: m.ID, Agent: model.Agent("codewhale"), Timestamp: ts, Model: modelID, Provider: "codewhale", Project: m.Workspace, Usage: usage, Aggregate: true, UnpricedReason: reason})
		}
	}
	return scanErr
}
