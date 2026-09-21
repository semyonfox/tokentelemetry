package ingest

import "testing"

func TestForgeReadsActualUsageOnly(t *testing.T) {
	schema := `CREATE TABLE conversations(conversation_id TEXT,title TEXT,workspace_id INTEGER,context TEXT,created_at TEXT,updated_at TEXT);`
	raw := `{"messages":[{"text":{"role":"Assistant","model":"claude-x"},"usage":{"prompt_tokens":{"actual":100},"completion_tokens":{"actual":20},"total_tokens":{"actual":120},"cached_tokens":{"actual":30}}},{"text":{"role":"Assistant","model":"ignored"},"usage":{"prompt_tokens":{"approx":5},"completion_tokens":{"actual":2},"cached_tokens":{"actual":0}}}]}`
	p := fixtureDB(t, schema, `INSERT INTO conversations VALUES('c','project',1,'`+raw+`','2026-01-01 00:00:00','2026-01-02 00:00:00')`)
	got := collectTurns(t, scanForgeDB, p)
	if len(got) != 1 || got[0].Usage.Input != 70 || got[0].Usage.CacheRead != 30 || got[0].Model != "claude-x" {
		t.Fatalf("got %#v", got)
	}
}
