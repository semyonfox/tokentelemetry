package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Zerostack persists session totals but not historical model/protocol changes.
type Zerostack struct{ root string }

func NewZerostack() *Zerostack {
	base := os.Getenv("ZS_DATA_DIR")
	if base == "" {
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			base = filepath.Join(homeDir(), ".local", "share")
		}
		if runtime.GOOS == "darwin" {
			base = filepath.Join(homeDir(), "Library", "Application Support")
		}
		if runtime.GOOS == "windows" {
			base = os.Getenv("APPDATA")
			if base == "" {
				base = filepath.Join(homeDir(), "AppData", "Roaming")
			}
		}
		base = filepath.Join(base, "zerostack")
	}
	return &Zerostack{root: filepath.Join(base, "sessions")}
}
func (*Zerostack) Agent() model.Agent { return model.Agent("zerostack") }
func (z *Zerostack) Roots() []string {
	if existingDir(z.root) == "" {
		return nil
	}
	return []string{z.root}
}
func (z *Zerostack) Scan(ctx context.Context, emit func(model.Turn)) error {
	if len(z.Roots()) == 0 {
		return nil
	}
	paths, err := filepath.Glob(filepath.Join(z.root, "*.json"))
	if err != nil {
		return err
	}
	var errs []error
	for _, path := range paths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var s struct {
			ID       string `json:"id"`
			Model    string `json:"model"`
			Provider string `json:"provider"`
			CWD      string `json:"working_dir"`
			Created  string `json:"created_at"`
			Updated  string `json:"updated_at"`
			Input    int64  `json:"total_input_tokens"`
			Output   int64  `json:"total_output_tokens"`
			Read     *int64 `json:"total_cached_input_tokens"`
			Write    *int64 `json:"total_cache_creation_input_tokens"`
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			errs = append(errs, e)
			continue
		}
		if json.Unmarshal(raw, &s) != nil || s.ID == "" || s.Input < 0 || s.Output < 0 {
			continue
		}
		u := model.Usage{Output: s.Output, Unclassified: s.Input}
		bounds := u
		if s.Read != nil {
			bounds.CacheRead = *s.Read
		}
		if s.Write != nil {
			bounds.CacheWrite = *s.Write
		}
		if _, ok := bounds.SanitizeAggregate(); !ok || bounds.CacheRead < 0 || bounds.CacheWrite < 0 {
			errs = append(errs, fmt.Errorf("zerostack: invalid session counters in %s", filepath.Base(path)))
			continue
		}
		reason := "Zerostack: raw session input lacks historical cache/protocol attribution; input total can omit native Anthropic cache tokens"
		if s.Read != nil && s.Write != nil && *s.Read >= 0 && *s.Write >= 0 {
			// The native Anthropic route reports fresh input; the other built-in
			// routes report gross input. Unknown custom routes remain unsplit.
			switch s.Provider {
			case "anthropic":
				u.Input, u.CacheRead, u.CacheWrite, u.Unclassified = s.Input, *s.Read, *s.Write, 0
			case "openai", "openrouter", "gemini", "ollama":
				if *s.Read+*s.Write <= s.Input {
					u.Input, u.CacheRead, u.CacheWrite, u.Unclassified = s.Input-*s.Read-*s.Write, *s.Read, *s.Write, 0
				}
			}
			if u.Unclassified == 0 {
				reason = "Zerostack: session-wide model/protocol tag may have changed; totals use its final protocol and cost is excluded"
			}
		}
		if u.IsZero() {
			continue
		}
		ts := parseTime(s.Updated)
		if ts.IsZero() {
			ts = parseTime(s.Created)
		}
		if s.Model == "" {
			s.Model = "unknown"
		}
		emit(model.Turn{Key: "zerostack|" + s.ID, SessionID: s.ID, Agent: z.Agent(), Timestamp: ts, Model: s.Model, Provider: s.Provider, Project: s.CWD, Usage: u, Aggregate: true, UnpricedReason: reason})
	}
	return errors.Join(errs...)
}
