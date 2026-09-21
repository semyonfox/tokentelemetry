package ingest

import "testing"

func TestGooseAggregateCacheAndUnknownSplit(t *testing.T) {
	schema := `CREATE TABLE sessions(id TEXT,working_dir TEXT,created_at TEXT,updated_at TEXT,accumulated_input_tokens INTEGER,accumulated_output_tokens INTEGER,accumulated_cache_read_tokens INTEGER,accumulated_cache_write_tokens INTEGER,provider_name TEXT,model_config_json TEXT,parent_session_id TEXT);`
	p := fixtureDB(t, schema, `INSERT INTO sessions VALUES('split','/p','2026-01-01T00:00:00Z','2026-01-02T00:00:00Z',100,20,30,10,'anthropic','{"model_name":"claude-x"}','')`, `INSERT INTO sessions VALUES('unsplit','','2026-01-01T00:00:00Z','2026-01-02T00:00:00Z',40,5,NULL,NULL,'openai','{"model_name":"gpt-x"}','parent')`)
	got := collectTurns(t, scanGooseDB, p)
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].Usage.Input != 60 || got[0].Usage.CacheRead != 30 || !got[0].Aggregate {
		t.Fatalf("split %#v", got[0])
	}
	if got[1].Usage.Unclassified != 40 || got[1].Usage.Input != 0 || got[1].UnpricedReason == "" || !got[1].Subagent {
		t.Fatalf("unsplit %#v", got[1])
	}
}
