package ingest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func fixtureDB(t *testing.T, schema string, inserts ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.db")
	db, e := sql.Open("sqlite", p)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if _, e = db.Exec(schema); e != nil {
		t.Fatal(e)
	}
	for _, q := range inserts {
		if _, e = db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	return p
}
func collectTurns(t *testing.T, scan func(context.Context, string) ([]model.Turn, error), p string) []model.Turn {
	t.Helper()
	v, e := scan(context.Background(), p)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func TestZCodeAccountingAndMalformedRows(t *testing.T) {
	p := fixtureDB(t, `CREATE TABLE session(id TEXT PRIMARY KEY,directory TEXT);CREATE TABLE model_usage(id TEXT PRIMARY KEY,session_id TEXT,model_id TEXT,input_tokens INTEGER,output_tokens INTEGER,reasoning_tokens INTEGER,cache_creation_input_tokens INTEGER,cache_read_input_tokens INTEGER,started_at INTEGER,completed_at INTEGER);`, `INSERT INTO session VALUES('s','/work')`, `INSERT INTO model_usage VALUES('ok','s','m',100,20,3,10,30,1000,2000),('bad','s','m',2,1,0,2,2,1000,NULL)`)
	got := collectTurns(t, scanZCodeDB, p)
	if len(got) != 1 {
		t.Fatalf("got %d turns", len(got))
	}
	want := model.Usage{Input: 60, Output: 20, CacheRead: 30, CacheWrite: 10, Reasoning: 3}
	if got[0].Usage != want || got[0].Timestamp.UnixMilli() != 2000 {
		t.Fatalf("got %#v", got[0])
	}
}

func TestZCodeReadsLiveWALReadOnly(t *testing.T) {
	p := fixtureDB(t, `PRAGMA journal_mode=WAL;CREATE TABLE session(id TEXT PRIMARY KEY,directory TEXT);CREATE TABLE model_usage(id TEXT PRIMARY KEY,session_id TEXT,model_id TEXT,input_tokens INTEGER,output_tokens INTEGER,reasoning_tokens INTEGER,cache_creation_input_tokens INTEGER,cache_read_input_tokens INTEGER,started_at INTEGER,completed_at INTEGER);`, `INSERT INTO session VALUES('s','')`, `INSERT INTO model_usage VALUES('x','s','m',4,2,0,0,0,1000,NULL)`)
	if got := collectTurns(t, scanZCodeDB, p); len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
}
