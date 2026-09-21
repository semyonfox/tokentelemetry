package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKiroCLIUsesOnlyRecordedCredits(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	raw := `{"session_id":"s","cwd":"/work","session_state":{"rts_model_state":{"model_info":{"model_id":"auto"}},"conversation_metadata":{"user_turn_metadatas":[{"end_timestamp":"2026-01-01T00:00:00Z","metering_usage":[{"value":1.25,"unit":"credit"}]},{"metering_usage":[]},{"metering_usage":[{"value":4,"unit":"token"}]}]}}}`
	if e := os.WriteFile(p, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	got := scanKiroCLIMetadata(p)
	if len(got) != 1 || got[0].Credits == nil || *got[0].Credits != 1.25 || got[0].Usage.Total() != 0 || got[0].Model != "kiro-auto" || got[0].UnpricedReason == "" {
		t.Fatalf("got %#v", got)
	}
}
