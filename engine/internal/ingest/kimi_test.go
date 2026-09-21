package ingest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestKimiStatusUpdateUsesDisjointMeasuredBuckets(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sessions", "workhash", "session-1", "wire.jsonl"),
		`{"timestamp":1788256800000,"message":{"type":"StatusUpdate","payload":{"message_id":"m1","model":"kimi-k2","token_usage":{"input_other":100,"output":20,"input_cache_read":700,"input_cache_creation":50}}}}`)
	turns := scan(t, newKimiAt(root))
	want := model.Usage{Input: 100, Output: 20, CacheRead: 700, CacheWrite: 50}
	if len(turns) != 1 || turns[0].Usage != want || turns[0].Model != "kimi-k2" || turns[0].Aggregate {
		t.Fatalf("turns = %+v, want usage %+v", turns, want)
	}
}

func TestKimiDoesNotInferHistoricalModelAndKeepsSubagentCalls(t *testing.T) {
	root := t.TempDir()
	line := `{"timestamp":"2026-09-01T10:00:00Z","type":"StatusUpdate","payload":{"message_id":"m1","token_usage":{"input_other":10,"output":2,"input_cache_read":3,"input_cache_creation":1}}}`
	writeFile(t, filepath.Join(root, "sessions", "wd", "s", "wire.jsonl"), line)
	writeFile(t, filepath.Join(root, "sessions", "wd", "s", "subagents", "agent-1", "wire.jsonl"), line)
	result, err := Run(context.Background(), []Scanner{newKimiAt(root)})
	if err != nil || len(result.Errors) != 0 || len(result.Turns) != 2 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	var subagents int
	for _, turn := range result.Turns {
		if turn.Model != "unknown" || turn.UnpricedReason == "" {
			t.Fatalf("invented model attribution: %+v", turn)
		}
		if turn.Subagent {
			subagents++
		}
	}
	if subagents != 1 {
		t.Fatalf("subagent turns = %d", subagents)
	}
}

func TestKimiReadsNativeFractionalSecondTimestamp(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sessions", "workhash", "session-1", "wire.jsonl"),
		`{"timestamp":1788256800.125,"message":{"type":"StatusUpdate","payload":{"message_id":"fractional","token_usage":{"input_other":10,"output":2,"input_cache_read":3,"input_cache_creation":1}}}}`)
	turns := scan(t, newKimiAt(root))
	if len(turns) != 1 || turns[0].Timestamp.UnixMilli() != 1788256800125 {
		t.Fatalf("turns = %+v", turns)
	}
}
