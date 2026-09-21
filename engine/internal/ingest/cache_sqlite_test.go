package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestCursorCacheInvalidatesCommittedWALWithoutMainDatabaseChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;` + cursorLocalSchema +
		`INSERT INTO cursorDiskKV VALUES('bubbleId:fixture:measured','{"type":2,"tokenCount":{"inputTokens":100,"outputTokens":10}}')`); err != nil {
		t.Fatal(err)
	}
	scanner := &Cursor{dbPath: path}
	cacheDir := t.TempDir()
	run := func() *Result {
		t.Helper()
		res, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
		if err != nil || len(res.Errors) != 0 {
			t.Fatalf("scan: %+v %v", res, err)
		}
		return res
	}
	if cold := run(); cold.CacheMisses != 1 || sumUsage(cold.Turns).Total() != 110 {
		t.Fatalf("initial scan: %+v", cold)
	}
	if warm := run(); warm.CacheHits != 1 || sumUsage(warm.Turns).Total() != 110 {
		t.Fatalf("unchanged database missed cache: %+v", warm)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE cursorDiskKV SET value=json_set(value,'$.tokenCount.outputTokens',20)`); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("fixture changed the main database instead of only its WAL")
	}
	if updated := run(); updated.CacheMisses != 1 || sumUsage(updated.Turns).Total() != 120 {
		t.Fatalf("committed WAL update remained stale: %+v", updated)
	}
	if warm := run(); warm.CacheHits != 1 || sumUsage(warm.Turns).Total() != 120 {
		t.Fatalf("updated database did not populate cache: %+v", warm)
	}
}
