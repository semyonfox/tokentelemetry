package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

const cursorSDKTestSchema = `
CREATE TABLE agents (
	agent_id TEXT PRIMARY KEY,
	workspace_ref TEXT NOT NULL
);
CREATE TABLE runs (
	run_id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL,
	status TEXT NOT NULL,
	model TEXT,
	usage_json TEXT,
	updated_at TEXT NOT NULL,
	finished_at TEXT,
	cancelled_at TEXT,
	expired_at TEXT
);`

func newCursorSDKTestDB(t *testing.T, projects, project, hash string) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(projects, project, "sdk-agent-store", hash, "index.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;` + cursorSDKTestSchema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func insertCursorSDKTestRun(t *testing.T, db *sql.DB, runID, status, modelID, usage, updated, finished string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO agents(agent_id,workspace_ref) VALUES('agent-1','/work/repo')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO runs(
		run_id,agent_id,status,model,usage_json,updated_at,finished_at
	) VALUES(?,'agent-1',?,?,?,?,?)`, runID, status,
		sql.NullString{String: modelID, Valid: modelID != ""},
		sql.NullString{String: usage, Valid: usage != ""}, updated,
		sql.NullString{String: finished, Valid: finished != ""}); err != nil {
		t.Fatal(err)
	}
}

func TestCursorSDKDiscoversTerminalUsageAndDeduplicatesCopiedStores(t *testing.T) {
	projects := t.TempDir()
	first, firstPath := newCursorSDKTestDB(t, projects, "[workspace]", "0123456789abcdef0123456789abcdef")
	copyDB, copyPath := newCursorSDKTestDB(t, projects, "a-workspace", "fedcba9876543210fedcba9876543210")
	usage := `{"inputTokens":100,"outputTokens":50,"cacheReadTokens":700,"cacheWriteTokens":200,"totalTokens":1050,"reasoningTokens":20}`
	for _, db := range []*sql.DB{first, copyDB} {
		insertCursorSDKTestRun(t, db, "run-1", "FINISHED", "composer-2.5", usage, "2026-09-20T11:59:00Z", "2026-09-20T12:00:00Z")
	}
	insertCursorSDKTestRun(t, first, "run-live", "RUNNING", "composer-2.5", usage, "2026-09-20T12:01:00Z", "")

	scanner := newCursorAgentSDKAt(projects)
	wantRoots := []string{copyPath, firstPath}
	slices.Sort(wantRoots)
	if roots := scanner.Roots(); !slices.Equal(roots, wantRoots) {
		t.Fatalf("roots=%q want=%q", roots, wantRoots)
	}
	result, err := Run(context.Background(), []Scanner{scanner})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan: result=%+v err=%v", result, err)
	}
	if len(result.Turns) != 1 || result.Duplicates != 1 {
		t.Fatalf("copied terminal runs were not deduplicated: %+v", result)
	}
	turn := result.Turns[0]
	if turn.Key != identityKey("cursor-agent", "run-1") || turn.SessionID != "agent-1" || turn.Project != "/work/repo" {
		t.Fatalf("identity: %+v", turn)
	}
	if turn.Usage != (model.Usage{Input: 100, Output: 50, CacheRead: 700, CacheWrite: 200, Reasoning: 20}) {
		t.Fatalf("disjoint cache buckets were changed: %+v", turn.Usage)
	}
	if turn.Model != "composer-2.5" || !turn.Aggregate || !strings.Contains(turn.UnpricedReason, "subagent") {
		t.Fatalf("model attribution limitation missing: %+v", turn)
	}
	wantTime := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if !turn.Timestamp.Equal(wantTime) {
		t.Fatalf("timestamp=%s want=%s", turn.Timestamp, wantTime)
	}
}

func TestCursorSDKRejectsInvalidUsageAndKeepsUndatedTerminalRuns(t *testing.T) {
	projects := t.TempDir()
	db, _ := newCursorSDKTestDB(t, projects, "workspace", "0123456789abcdef0123456789abcdef")
	valid := `{"inputTokens":3,"outputTokens":2,"cacheReadTokens":5,"cacheWriteTokens":7,"totalTokens":17,"reasoningTokens":1}`
	insertCursorSDKTestRun(t, db, "bad-time", "ERROR", "m", valid, "not-a-time", "")
	insertCursorSDKTestRun(t, db, "missing-time-model", "FINISHED", "", valid, "", "")
	insertCursorSDKTestRun(t, db, "missing-bucket", "FINISHED", "m", `{"inputTokens":1,"outputTokens":2,"cacheReadTokens":3,"totalTokens":6}`, "2026-09-20T12:00:00Z", "")
	insertCursorSDKTestRun(t, db, "missing-usage", "FINISHED", "m", "", "2026-09-20T12:00:00Z", "")
	insertCursorSDKTestRun(t, db, "bad-reasoning", "FINISHED", "m", `{"inputTokens":1,"outputTokens":2,"cacheReadTokens":3,"cacheWriteTokens":4,"totalTokens":10,"reasoningTokens":3}`, "2026-09-20T12:00:00Z", "")
	insertCursorSDKTestRun(t, db, "oversized", "FINISHED", "m", `{"inputTokens":600000000000000000,"outputTokens":0,"cacheReadTokens":0,"cacheWriteTokens":0,"totalTokens":600000000000000000}`, "2026-09-20T12:00:00Z", "")

	var turns []model.Turn
	err := newCursorAgentSDKAt(projects).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil {
		t.Fatal("invalid rows did not produce diagnostics")
	}
	for _, runID := range []string{"bad-time", "missing-bucket", "missing-usage", "bad-reasoning", "oversized"} {
		if !strings.Contains(err.Error(), runID) {
			t.Fatalf("diagnostic %q missing run %q", err, runID)
		}
	}
	if len(turns) != 2 {
		t.Fatalf("valid terminal rows=%d want=2: %+v", len(turns), turns)
	}
	for _, turn := range turns {
		if !turn.Timestamp.IsZero() || !strings.Contains(turn.UnpricedReason, "terminal timestamp") {
			t.Fatalf("undated run was given pricing attribution: %+v", turn)
		}
	}
	if turns[1].Model != "unknown" || !strings.Contains(turns[1].UnpricedReason, "no model") {
		t.Fatalf("missing model handling: %+v", turns[1])
	}
}

func TestCursorSDKDefaultDiscoveryAndSavedResultOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("TT_CURSOR_AGENT_DIR", "")
	projects := filepath.Join(home, ".cursor", "projects")
	if err := os.MkdirAll(filepath.Join(projects, "ordinary-cursor-project"), 0o755); err != nil {
		t.Fatal(err)
	}
	native := NewCursorAgent()
	if roots := native.Roots(); len(roots) != 0 {
		t.Fatalf("ordinary Cursor project falsely detected as an SDK store: %q", roots)
	}
	db, nativePath := newCursorSDKTestDB(t, projects, "workspace", "0123456789abcdef0123456789abcdef")
	usage := `{"inputTokens":1,"outputTokens":2,"cacheReadTokens":3,"cacheWriteTokens":4,"totalTokens":10}`
	insertCursorSDKTestRun(t, db, "native-run", "FINISHED", "m", usage, "2026-09-20T12:00:00Z", "2026-09-20T12:00:00Z")

	if roots := native.Roots(); !slices.Equal(roots, []string{nativePath}) {
		t.Fatalf("default roots=%q want=%q", roots, []string{nativePath})
	}
	if turns := scan(t, native); len(turns) != 1 || turns[0].Key != identityKey("cursor-agent", "native-run") {
		t.Fatalf("default native discovery: %+v", turns)
	}

	explicit := t.TempDir()
	writeFile(t, filepath.Join(explicit, "saved.json"), `{"id":"saved-run","agentId":"saved-agent","status":"finished","createdAt":1,"model":{"id":"m"},"usage":{"inputTokens":2,"outputTokens":3,"cacheReadTokens":4,"cacheWriteTokens":5,"totalTokens":14}}`)
	t.Setenv("TT_CURSOR_AGENT_DIR", explicit)
	saved := NewCursorAgent()
	if roots := saved.Roots(); !slices.Equal(roots, []string{explicit}) {
		t.Fatalf("explicit roots=%q", roots)
	}
	turns := scan(t, saved)
	if len(turns) != 1 || turns[0].Key != identityKey("cursor-agent", "saved-run") {
		t.Fatalf("explicit saved import did not override native discovery: %+v", turns)
	}
}
