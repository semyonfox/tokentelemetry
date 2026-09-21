package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Antigravity's public headless result is cumulative across a conversation.
// It is an explicit import source because the same calls may also be present in
// the automatically discovered native conversation databases.
type antigravityStreamUsage struct {
	Input    int64 `json:"input_tokens"`
	Output   int64 `json:"output_tokens"`
	Read     int64 `json:"cache_read_tokens"`
	Thinking int64 `json:"thinking_tokens"`
}

type antigravityStreamResult struct {
	Conversation string                  `json:"conversation_id"`
	Turns        int64                   `json:"num_turns"`
	Usage        *antigravityStreamUsage `json:"usage"`
}

type antigravityStreamEvent struct {
	Event string `json:"event"`
	Init  struct {
		CWD string `json:"cwd"`
	} `json:"init"`
	Step struct {
		Usage *antigravityStreamUsage `json:"usage"`
	} `json:"step_update"`
	Result antigravityStreamResult `json:"result"`
}

func antigravityStreamTokens(u *antigravityStreamUsage) (model.Usage, bool) {
	if u == nil || u.Input < 0 || u.Output < 0 || u.Read < 0 || u.Thinking < 0 || u.Thinking > u.Output {
		return model.Usage{}, false
	}
	// The documented cached continuation reports input=278 and cache=30214.
	// Cache reads are separate from input in this public schema.
	v, ok := (model.Usage{Input: u.Input, Output: u.Output, CacheRead: u.Read, Reasoning: u.Thinking}).SanitizeAggregate()
	return v, ok && !v.IsZero()
}

func scanAntigravityStreams(ctx context.Context, root string, emit func(model.Turn)) error {
	var errs []error
	var observations []model.Turn
	type snapshot struct {
		usage model.Usage
		turns int64
	}
	latest := map[string]snapshot{}
	conflicts := map[string]bool{}
	dominates := func(a, b model.Usage) bool {
		return a.Input >= b.Input && a.Output >= b.Output && a.CacheRead >= b.CacheRead && a.Reasoning >= b.Reasoning
	}
	collect := func(t model.Turn, sequence int64) {
		old, seen := latest[t.Key]
		if !seen {
			latest[t.Key] = snapshot{t.Usage, sequence}
		} else if (sequence > old.turns && !dominates(t.Usage, old.usage)) || (sequence < old.turns && !dominates(old.usage, t.Usage)) {
			conflicts[t.Key] = true
		} else if dominates(t.Usage, old.usage) {
			latest[t.Key] = snapshot{t.Usage, sequence}
		} else if !dominates(old.usage, t.Usage) {
			conflicts[t.Key] = true
		}
		observations = append(observations, t)
	}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if !entry.Type().IsRegular() || (filepath.Ext(path) != ".jsonl" && filepath.Ext(path) != ".ndjson") {
			return nil
		}
		if scanErr := scanAntigravityStream(ctx, path, collect); scanErr != nil {
			errs = append(errs, scanErr)
		}
		return nil
	})
	for _, observation := range observations {
		if !conflicts[observation.Key] {
			emit(observation)
		}
	}
	if len(conflicts) > 0 {
		errs = append(errs, fmt.Errorf("antigravity: %d conversations have conflicting cumulative counters; usage excluded", len(conflicts)))
	}
	return errors.Join(append(errs, err)...)
}

func scanAntigravityStream(ctx context.Context, path string, emit func(model.Turn, int64)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	project := ""
	unfinished := false
	for line := range jsonLines(f) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var event antigravityStreamEvent
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		switch event.Event {
		case "init":
			project = event.Init.CWD
		case "step_update":
			if event.Step.Usage != nil {
				unfinished = true
			}
		case "result":
			unfinished = false
			result := event.Result
			u, ok := antigravityStreamTokens(result.Usage)
			if !ok || result.Conversation == "" || result.Turns <= 0 {
				continue
			}
			emit(model.Turn{
				Key: identityKey("antigravity", result.Conversation), SessionID: result.Conversation,
				Agent: model.AgentAntigravity, Model: "unknown", Project: project, Usage: u,
				Aggregate: true, UnpricedReason: "Antigravity result contains cumulative conversation usage without historical model/time; usage is undated and unpriced",
			}, result.Turns)
		}
	}
	if unfinished {
		return fmt.Errorf("antigravity: unfinished headless turn in %s has no result; partial steps excluded", path)
	}
	return nil
}
