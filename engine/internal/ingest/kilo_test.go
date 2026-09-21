package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func openKiloFixture(t *testing.T, path string, current, legacy bool) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	statements := []string{
		`CREATE TABLE session(id TEXT PRIMARY KEY, directory TEXT NOT NULL, parent_id TEXT)`,
		`INSERT INTO session(id, directory, parent_id) VALUES('ses_main', '/work/main', NULL), ('ses_child', '/work/child', 'ses_main')`,
	}
	if current {
		statements = append(statements, `CREATE TABLE session_message(id TEXT PRIMARY KEY, session_id TEXT NOT NULL, type TEXT NOT NULL, seq INTEGER, time_created INTEGER NOT NULL, data TEXT NOT NULL)`)
	}
	if legacy {
		statements = append(statements,
			`CREATE TABLE message(id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, data TEXT NOT NULL)`,
			`CREATE TABLE part(id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, data TEXT NOT NULL)`,
		)
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func insertKiloCurrent(t *testing.T, db *sql.DB, id, sessionID, kind string, created int64, data string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO session_message(id, session_id, type, seq, time_created, data) VALUES(?, ?, ?, 1, ?, ?)`, id, sessionID, kind, created, data); err != nil {
		t.Fatal(err)
	}
}

func insertKiloLegacy(t *testing.T, db *sql.DB, id, sessionID string, created int64, data string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO message(id, session_id, time_created, data) VALUES(?, ?, ?, ?)`, id, sessionID, created, data); err != nil {
		t.Fatal(err)
	}
}

func scanKiloFixture(t *testing.T, paths ...string) ([]model.Turn, error) {
	t.Helper()
	var turns []model.Turn
	err := (&Kilo{dbPaths: paths}).Scan(context.Background(), func(turn model.Turn) {
		turns = append(turns, turn)
	})
	return turns, err
}

func sumKiloUsage(turns []model.Turn) model.Usage {
	var usage model.Usage
	for _, turn := range turns {
		usage.Add(turn.Usage)
	}
	return usage
}

func TestNewKiloDiscoversXDGAndDatabaseOverride(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("KILO_DB", "")
	kiloDir := filepath.Join(data, "kilo")
	if err := os.MkdirAll(kiloDir, 0700); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, name := range []string{"kilo.db", "kilo-dev.db", "opencode-local.db"} {
		path := filepath.Join(kiloDir, name)
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		want = append(want, path)
	}
	if err := os.WriteFile(filepath.Join(kiloDir, "unrelated.db"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got := NewKilo().Roots(); !reflect.DeepEqual(got, want) {
		t.Fatalf("discovered roots = %q, want %q", got, want)
	}

	override := filepath.Join(kiloDir, "private-channel.sqlite")
	if err := os.WriteFile(override, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILO_DB", "private-channel.sqlite")
	if got := NewKilo().Roots(); !reflect.DeepEqual(got, []string{override}) {
		t.Fatalf("relative override roots = %q", got)
	}

	t.Setenv("KILO_DB", ":memory:")
	if got := NewKilo().Roots(); len(got) != 0 {
		t.Fatalf("separate process cannot read memory database: %q", got)
	}
}

func TestKiloCurrentAccountingAndMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kilo.db")
	db := openKiloFixture(t, path, true, false)
	created := time.Date(2026, 9, 18, 23, 57, 0, 0, time.UTC).UnixMilli()
	insertKiloCurrent(t, db, "msg_billable", "ses_child", "assistant", created,
		`{"agent":"build","model":{"id":"unknown-model-v9","providerID":"some-router"},"tokens":{"input":100,"output":20,"reasoning":7,"cache":{"read":60,"write":10}},"time":{"created":1},"content":[]}`)
	insertKiloCurrent(t, db, "msg_user", "ses_main", "user", created+1,
		`{"model":{"id":"ignored","providerID":"ignored"},"tokens":{"input":500,"output":500,"reasoning":0,"cache":{"read":0,"write":0}}}`)
	insertKiloCurrent(t, db, "msg_zero", "ses_main", "assistant", created+2,
		`{"model":{"id":"zero","providerID":"p"},"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}`)
	insertKiloCurrent(t, db, "msg_corrupt", "ses_main", "assistant", created+3,
		`{"model":{"id":"bad","providerID":"p"},"tokens":{"input":50000001,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}`)

	turns, err := scanKiloFixture(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %+v", turns)
	}
	turn := turns[0]
	wantUsage := model.Usage{Input: 100, Output: 20, Reasoning: 7, CacheRead: 60, CacheWrite: 10}
	if turn.Usage != wantUsage {
		t.Fatalf("usage = %+v, want %+v; input must remain net", turn.Usage, wantUsage)
	}
	if turn.Model != "unknown-model-v9" || turn.Provider != "some-router" {
		t.Fatalf("model metadata lost: %+v", turn)
	}
	if turn.Agent != model.Agent("kilo-code") || turn.SessionID != "ses_child" || turn.Project != "/work/child" || !turn.Subagent {
		t.Fatalf("turn attribution = %+v", turn)
	}
	if got := turn.Timestamp; !got.Equal(time.UnixMilli(created)) {
		t.Fatalf("timestamp = %s, want row timestamp %s", got, time.UnixMilli(created))
	}
}

func TestKiloRetainsMeasuredUsageWithoutModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kilo.db")
	db := openKiloFixture(t, path, true, true)
	insertKiloCurrent(t, db, "current", "ses_main", "assistant", 1_000,
		`{"tokens":{"input":12,"output":3,"cache":{"read":2,"write":1}}}`)
	insertKiloLegacy(t, db, "legacy", "ses_main", 2_000,
		`{"role":"assistant","tokens":{"input":8,"output":2,"cache":{"read":1,"write":0}}}`)

	turns, err := scanKiloFixture(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || turns[0].Model != "unknown" || turns[1].Model != "unknown" {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestKiloCurrentWinsDuplicateAndLegacyOnlyUsageSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kilo-dev.db")
	db := openKiloFixture(t, path, true, true)
	insertKiloCurrent(t, db, "msg_same", "ses_main", "assistant", 2_000,
		`{"model":{"id":"current-model","providerID":"current-provider"},"tokens":{"input":11,"output":2,"reasoning":3,"cache":{"read":4,"write":5}}}`)
	insertKiloLegacy(t, db, "msg_same", "ses_main", 1_000,
		`{"role":"assistant","modelID":"legacy-duplicate","providerID":"legacy-provider","tokens":{"input":900,"output":90,"reasoning":9,"cache":{"read":80,"write":70}},"time":{"created":1000}}`)
	insertKiloLegacy(t, db, "msg_legacy_only", "ses_main", 3_000,
		`{"role":"assistant","modelID":"legacy-only-model","providerID":"legacy-provider","tokens":{"input":31,"output":7,"reasoning":2,"cache":{"read":13,"write":1}},"time":{"created":3000}}`)
	insertKiloCurrent(t, db, "msg_current_zero", "ses_main", "assistant", 3_500,
		`{"model":{"id":"current-zero","providerID":"current-provider"},"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}}`)
	insertKiloLegacy(t, db, "msg_current_zero", "ses_main", 3_500,
		`{"role":"assistant","modelID":"stale-legacy","providerID":"legacy-provider","tokens":{"input":700,"output":70,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":3500}}`)
	insertKiloLegacy(t, db, "msg_legacy_user", "ses_main", 4_000,
		`{"role":"user","modelID":"ignored","providerID":"ignored","tokens":{"input":100,"output":100,"reasoning":0,"cache":{"read":0,"write":0}}}`)

	turns, err := scanKiloFixture(t, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns = %+v", turns)
	}
	if turns[0].Model != "current-model" || turns[0].Usage.Input != 11 {
		t.Fatalf("legacy duplicate won: %+v", turns[0])
	}
	if turns[1].Model != "legacy-only-model" || turns[1].Usage != (model.Usage{Input: 31, Output: 7, Reasoning: 2, CacheRead: 13, CacheWrite: 1}) {
		t.Fatalf("legacy-only usage lost: %+v", turns[1])
	}
	if got := sumKiloUsage(turns); got != (model.Usage{Input: 42, Output: 9, Reasoning: 5, CacheRead: 17, CacheWrite: 6}) {
		t.Fatalf("independently summed usage = %+v", got)
	}
}

func TestKiloReportsUnsupportedSchemaAndKeepsOtherDatabases(t *testing.T) {
	root := t.TempDir()
	badPath := filepath.Join(root, "kilo-bad.db")
	bad, err := sql.Open("sqlite", badPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Exec(`CREATE TABLE session_message(id TEXT, session_id TEXT, type TEXT, data TEXT)`); err != nil {
		t.Fatal(err)
	}
	bad.Close()

	goodPath := filepath.Join(root, "kilo.db")
	good := openKiloFixture(t, goodPath, true, false)
	insertKiloCurrent(t, good, "msg_good", "ses_main", "assistant", 5_000,
		`{"model":{"id":"kept","providerID":"p"},"tokens":{"input":8,"output":1,"reasoning":0,"cache":{"read":2,"write":0}}}`)

	turns, scanErr := scanKiloFixture(t, badPath, goodPath)
	if scanErr == nil || !strings.Contains(scanErr.Error(), "unsupported schema") || !strings.Contains(scanErr.Error(), badPath) {
		t.Fatalf("unsupported schema error = %v", scanErr)
	}
	if len(turns) != 1 || turns[0].Model != "kept" {
		t.Fatalf("valid database was lost: %+v", turns)
	}
}
