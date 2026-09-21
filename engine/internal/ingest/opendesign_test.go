package ingest

import (
	"path/filepath"
	"testing"
)

func TestOpenDesignTracksModelChangesAndCacheFamilies(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "namespaces", "team", "data", "runs", "run-1", "events.jsonl")
	writeFile(t, path, `{"id":"start","event":"start","timestamp":1788220800000,"data":{"model":"openai-model"}}`, `{"id":"u1","event":"agent","timestamp":1788220860000,"data":{"type":"usage","usage":{"input_tokens":100,"output_tokens":20,"cached_read_tokens":40,"cached_write_tokens":5,"thought_tokens":3}}}`, `{"id":"status","event":"agent","timestamp":1788220900000,"data":{"type":"status","model":"anthropic-model"}}`, `{"id":"u2","event":"agent","timestamp":1788220920000,"data":{"type":"usage","usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":30,"cache_creation_input_tokens":4}}}`)
	turns := scan(t, newOpenDesignAt(root))
	if len(turns) != 2 {
		t.Fatalf("turns: %+v", turns)
	}
	if turns[0].Model != "openai-model" || turns[0].Usage.Input != 55 || turns[0].Usage.Output != 23 || turns[0].Usage.Reasoning != 3 || turns[0].Usage.Total() != 123 {
		t.Fatalf("openai: %+v", turns[0])
	}
	if turns[1].Model != "anthropic-model" || turns[1].Usage.Input != 10 || turns[1].Usage.CacheRead != 30 || turns[1].Usage.CacheWrite != 4 || turns[1].Usage.Total() != 46 {
		t.Fatalf("anthropic: %+v", turns[1])
	}
}

func TestOpenDesignUsageWithoutModelIsRetainedUnpriced(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "runs", "run", "events.jsonl"), `{"id":"u","event":"agent","timestamp":"2026-09-01T00:00:00Z","data":{"type":"usage","usage":{"input_tokens":1}}}`)
	turns := scan(t, newOpenDesignAt(root))
	if len(turns) != 1 || turns[0].Model != "unknown" || turns[0].UnpricedReason == "" {
		t.Fatalf("turns: %+v", turns)
	}
}

func TestOpenDesignDropsOversizedEventBeforeAddingThinking(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runs", "run", "events.jsonl")
	writeFile(t, path, `{"event":"start","data":{"model":"m"}}`, `{"id":"bad","event":"agent","data":{"type":"usage","usage":{"output_tokens":9223372036854775807,"thought_tokens":1}}}`, `{"id":"good","event":"agent","data":{"type":"usage","usage":{"input_tokens":2}}}`)
	turns := scan(t, newOpenDesignAt(root))
	if len(turns) != 1 || turns[0].Usage.Input != 2 {
		t.Fatalf("turns: %+v", turns)
	}
}
