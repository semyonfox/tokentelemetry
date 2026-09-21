package ingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestDevinReadsExactATIFStepUsageAndSessionMetadata(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "transcripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "transcripts", "fallback.json"), `{"schema_version":"ATIF-v1.7","session_id":"session","agent":{"model_name":"agent-model"},"steps":[{"step_id":1,"source":"agent","timestamp":"2026-09-01T00:01:00Z","model_name":"step-model","metrics":{"prompt_tokens":100,"completion_tokens":20,"cached_tokens":40,"extra":{"cache_creation_input_tokens":5}}}]}`)
	db, err := sql.Open("sqlite", filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions(id TEXT,working_directory TEXT,model TEXT,created_at INTEGER,last_activity_at INTEGER,hidden INTEGER); INSERT INTO sessions VALUES('session','/work/repo','db-model',0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	turns := scan(t, newDevinAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns: %+v", turns)
	}
	turn := turns[0]
	if turn.Model != "step-model" || turn.Project != "/work/repo" || turn.Timestamp.IsZero() {
		t.Fatalf("metadata: %+v", turn)
	}
	if turn.Usage.Input != 60 || turn.Usage.CacheRead != 40 || turn.Usage.CacheWrite != 5 || turn.Usage.Output != 20 || turn.Usage.Total() != 125 {
		t.Fatalf("usage: %+v", turn.Usage)
	}
}

func TestDevinLegacyMetadataAndUnknownModelStayUnpriced(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "transcripts", "s.json"), `{"schema_version":"legacy","session_id":"s","agent":{},"steps":[{"step_id":7,"source":"agent","metadata":{"created_at":"2026-09-01T00:00:00Z","metrics":{"input_tokens":50,"output_tokens":3,"cache_read_tokens":10,"cache_creation_tokens":2}}}]}`)
	turns := scan(t, newDevinAt(root))
	if len(turns) != 1 || turns[0].Model != "unknown" || turns[0].UnpricedReason == "" || turns[0].Usage.Input != 40 {
		t.Fatalf("turns: %+v", turns)
	}
}

func TestDevinDoesNotInventStepTimeOrPriceSessionModel(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "transcripts", "s.json"), `{"session_id":"s","steps":[{"step_id":1,"metrics":{"prompt_tokens":1}}]}`)
	db, err := sql.Open("sqlite", filepath.Join(root, "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sessions(id TEXT,working_directory TEXT,model TEXT,created_at INTEGER,last_activity_at INTEGER,hidden INTEGER); INSERT INTO sessions VALUES('s','/work','session-model',1788220800,1788220860,0)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	turns := scan(t, newDevinAt(root))
	if len(turns) != 1 || turns[0].Model != "session-model" || turns[0].UnpricedReason == "" || !turns[0].Timestamp.IsZero() {
		t.Fatalf("turns: %+v", turns)
	}
}

func TestDevinDropsOversizedStepAndKeepsNextStep(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "transcripts", "s.json"), `{"session_id":"s","agent":{"model_name":"m"},"steps":[{"step_id":1,"metrics":{"prompt_tokens":1,"completion_tokens":9223372036854775807}},{"step_id":2,"metrics":{"prompt_tokens":3}}]}`)
	turns := scan(t, newDevinAt(root))
	if len(turns) != 1 || turns[0].Usage.Input != 3 {
		t.Fatalf("turns: %+v", turns)
	}
}
