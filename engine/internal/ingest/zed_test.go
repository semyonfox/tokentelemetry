package ingest

import (
	"context"
	"database/sql"
	"github.com/klauspost/compress/zstd"
	"testing"
)

func TestZedJSONZstdAndRemainder(t *testing.T) {
	schema := `CREATE TABLE threads(id TEXT,parent_id TEXT,updated_at TEXT,data_type TEXT,data BLOB);`
	p := fixtureDB(t, schema)
	db, e := sql.Open("sqlite", p)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	raw := []byte(`{"cumulative_token_usage":{"input_tokens":15,"output_tokens":7,"cache_creation_input_tokens":3,"cache_read_input_tokens":4},"request_token_usage":{"r1":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":1,"cache_read_input_tokens":2}},"model":{"provider":"anthropic","model":"claude-x"}}`)
	enc, e := zstd.NewWriter(nil)
	if e != nil {
		t.Fatal(e)
	}
	compressed := enc.EncodeAll(raw, nil)
	enc.Close()
	if _, e = db.Exec(`INSERT INTO threads VALUES('j',NULL,'2026-01-01T00:00:00Z','json',?),('z','parent','2026-01-01T00:00:00Z','zstd',?)`, raw, compressed); e != nil {
		t.Fatal(e)
	}
	got := collectTurns(t, scanZedDB, p)
	if len(got) != 4 {
		t.Fatalf("got %d: %#v", len(got), got)
	}
	for _, v := range got {
		if !v.Aggregate || v.Model != "claude-x" {
			t.Fatalf("got %#v", v)
		}
	}
}

func TestZedRejectsInconsistentCumulativeAndUnknownEncoding(t *testing.T) {
	schema := `CREATE TABLE threads(id TEXT,parent_id TEXT,updated_at TEXT,data_type TEXT,data BLOB);`
	bad := `{"cumulative_token_usage":{"input_tokens":1},"request_token_usage":{"r":{"input_tokens":2}}}`
	p := fixtureDB(t, schema, `INSERT INTO threads VALUES('bad',NULL,'','json','`+bad+`')`, `INSERT INTO threads VALUES('future',NULL,'','brotli','x')`)
	if got, err := scanZedDB(context.Background(), p); len(got) != 0 || err == nil {
		t.Fatalf("invalid data must be diagnosed: %#v, %v", got, err)
	}
}
