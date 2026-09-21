package ingest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestVibeKeepsCacheAwareSessionTotalsAggregate(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "session-1", "meta.json"), `{
      "session_id":"s1","start_time":"2026-09-01T09:00:00Z","end_time":"2026-09-01T10:00:00Z",
      "environment":{"working_directory":"/work/vibe"},
	  "stats":{"session_prompt_tokens":1000,"session_completion_tokens":80,"session_cached_tokens":700,"session_cost":0.5},
      "config":{"active_model":"medium","models":[{"alias":"medium","name":"mistral-medium-3.5"}]}
    }`)
	turns := scan(t, newVibeAt(root))
	want := model.Usage{Input: 300, Output: 80, CacheRead: 700}
	if len(turns) != 1 || turns[0].Usage != want || !turns[0].Aggregate || turns[0].Model != "mistral-medium-3.5" {
		t.Fatalf("turns = %+v, want usage %+v", turns, want)
	}
	if !strings.Contains(turns[0].UnpricedReason, "model-switch") {
		t.Fatalf("missing aggregate model limitation: %+v", turns[0])
	}
}

func TestVibeRejectsOversizedAggregateBeforeTotaling(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "huge", "meta.json"), `{"session_id":"huge","end_time":"2026-09-01T10:00:00Z","stats":{"session_prompt_tokens":9223372036854775807,"session_completion_tokens":1,"session_cached_tokens":0}}`)
	var turns []model.Turn
	err := newVibeAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || len(turns) != 0 {
		t.Fatalf("turns=%+v error=%v", turns, err)
	}
}

func TestVibeOlderMetadataKeepsPromptUnclassified(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "old", "meta.json"), `{"session_id":"old","end_time":"2026-09-01T10:00:00Z","stats":{"session_prompt_tokens":1000,"session_completion_tokens":80},"config":{"active_model":"devstral"}}`)
	turns := scan(t, newVibeAt(root))
	if len(turns) != 1 || turns[0].Usage.Unclassified != 1000 || !strings.Contains(turns[0].UnpricedReason, "predates") {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestVibeAcceptsAggregateBeyondPerCallLimit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "large", "meta.json"), `{"session_id":"large","end_time":"2026-09-01T10:00:00Z","stats":{"session_prompt_tokens":60000000,"session_completion_tokens":5000000,"session_cached_tokens":20000000},"config":{"active_model":"devstral"}}`)
	turns := scan(t, newVibeAt(root))
	want := model.Usage{Input: 40_000_000, Output: 5_000_000, CacheRead: 20_000_000}
	if len(turns) != 1 || turns[0].Usage != want {
		t.Fatalf("turns=%+v", turns)
	}
}

func TestVibeSubagentAggregateHasDistinctIdentity(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "session-1", "meta.json"), `{"session_id":"s1","end_time":"2026-09-01T10:00:00Z","stats":{"session_prompt_tokens":10,"session_completion_tokens":2},"config":{"active_model":"devstral"}}`)
	writeFile(t, filepath.Join(root, "session-1", "agents", "explore", "meta.json"), `{"session_id":"child-1","end_time":"2026-09-01T10:00:00Z","stats":{"session_prompt_tokens":10,"session_completion_tokens":2},"config":{"active_model":"devstral"}}`)
	turns := scan(t, newVibeAt(root))
	if len(turns) != 2 || turns[0].Key == turns[1].Key {
		t.Fatalf("turns = %+v", turns)
	}
	var subagents int
	for _, turn := range turns {
		if turn.Subagent {
			subagents++
		}
	}
	if subagents != 1 {
		t.Fatalf("subagent aggregates = %d", subagents)
	}
}
