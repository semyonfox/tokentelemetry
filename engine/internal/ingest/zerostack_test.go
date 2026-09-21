package ingest

import (
	"path/filepath"
	"testing"
)

func TestZerostackKeepsAggregateModelAmbiguity(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "s.json"), `{"id":"s","model":"m","provider":"anthropic","total_input_tokens":100,"total_output_tokens":20,"total_cached_input_tokens":700,"total_cache_creation_input_tokens":200,"updated_at":"2026-09-01T10:00:00Z"}`)
	writeFile(t, filepath.Join(root, "legacy.json"), `{"id":"old","model":"m","total_input_tokens":1000,"total_output_tokens":20}`)
	turns := scan(t, &Zerostack{root: root})
	if len(turns) != 2 {
		t.Fatal(turns)
	}
	for _, turn := range turns {
		if !turn.Aggregate || turn.UnpricedReason == "" || turn.Model != "m" {
			t.Fatalf("ambiguous aggregate priced: %+v", turn)
		}
		if turn.SessionID == "s" && (turn.Usage.Total() != 1020 || turn.Usage.Input != 100 || turn.Usage.CacheRead != 700) {
			t.Fatalf("native input incorrectly normalized: %+v", turn)
		}
		if turn.SessionID == "old" && turn.Usage.Unclassified != 1000 {
			t.Fatal("legacy cache split guessed")
		}
	}
}
