package ingest

import (
	"path/filepath"
	"testing"
)

func TestDroidEmitsOneUnpricedAggregateWithoutSplittingMessages(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "bucket", "session.jsonl")
	writeFile(t, path, `{"type":"session_start","id":"native-session","timestamp":"2026-09-01T00:00:00Z","cwd":"/work/repo"}`, `{"type":"message","id":"a","timestamp":"2026-09-01T00:01:00Z"}`, `{"type":"message","id":"b","timestamp":"2026-09-01T00:02:00Z"}`)
	writeFile(t, filepath.Join(root, "sessions", "bucket", "session.settings.json"), `{"model":"custom:Model-[Proxy]-0","tokenUsage":{"inputTokens":100,"outputTokens":20,"cacheCreationTokens":30,"cacheReadTokens":40,"thinkingTokens":5}}`)
	turns := scan(t, newDroidAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns: %+v", turns)
	}
	turn := turns[0]
	if !turn.Aggregate || turn.UnpricedReason == "" || turn.Model != "custom:Model-[Proxy]-0" || turn.Project != "/work/repo" {
		t.Fatalf("metadata: %+v", turn)
	}
	if turn.Usage.Input != 100 || turn.Usage.Output != 25 || turn.Usage.Reasoning != 5 || turn.Usage.Total() != 195 {
		t.Fatalf("usage: %+v", turn.Usage)
	}
}

func TestDroidAllowsLegitimateSessionAggregateAbovePerCallLimit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "bucket", "session.jsonl")
	writeFile(t, path, `{"type":"session_start","id":"large-session","cwd":"/work/repo"}`)
	writeFile(t, filepath.Join(root, "sessions", "bucket", "session.settings.json"), `{"model":"m","tokenUsage":{"inputTokens":60000000,"outputTokens":1}}`)
	turns := scan(t, newDroidAt(root))
	if len(turns) != 1 || turns[0].Usage.Input != 60_000_000 || !turns[0].Aggregate {
		t.Fatalf("turns: %+v", turns)
	}
}
