package projectmeta

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// makeT3HistoryDB writes a state database shaped like T3's statev2: the v1
// thread table only holds live threads, deleted ones survive in the v2
// projection payloads, and project moves live in the event log.
func makeT3HistoryDB(t *testing.T, baseDir string) {
	t.Helper()
	stateDir := filepath.Join(baseDir, "userdata")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(stateDir, "statev2.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE projection_projects (project_id TEXT PRIMARY KEY, workspace_root TEXT NOT NULL, deleted_at TEXT)`,
		`CREATE TABLE projection_threads (thread_id TEXT PRIMARY KEY, project_id TEXT NOT NULL, worktree_path TEXT)`,
		`CREATE TABLE provider_session_runtime (thread_id TEXT PRIMARY KEY, adapter_key TEXT NOT NULL, resume_cursor_json TEXT)`,
		`CREATE TABLE orchestration_v2_projection_threads (thread_id TEXT PRIMARY KEY, project_id TEXT NOT NULL, payload_json TEXT NOT NULL)`,
		`CREATE TABLE orchestration_events (sequence INTEGER PRIMARY KEY, aggregate_kind TEXT NOT NULL, stream_id TEXT NOT NULL, event_type TEXT NOT NULL, payload_json TEXT NOT NULL)`,
		`INSERT INTO projection_projects VALUES
			('swim', '/code/owned/swim', NULL),
			('notes', '/code/notes', NULL),
			('home', '/home/u', NULL),
			('reuser', '/code/reused', NULL),
			('left', '/code/left', NULL),
			('right', '/code/right', NULL)`,
		`INSERT INTO projection_threads VALUES ('live', 'notes', '/wt/notes/live')`,
		`INSERT INTO orchestration_v2_projection_threads VALUES
			('live', 'notes', '{"worktreePath":"/wt/notes/live"}'),
			('deleted', 'swim', '{"worktreePath":"/wt/swim/old-branch","deletedAt":"2026-10-01"}'),
			('no-worktree', 'swim', '{"worktreePath":null}'),
			('broken', 'swim', 'not json'),
			('relative', 'swim', '{"worktreePath":"wt/relative"}')`,
		`INSERT INTO orchestration_events (aggregate_kind, stream_id, event_type, payload_json) VALUES
			('project', 'swim', 'project.created', '{"workspaceRoot":"C:\\Users\\u\\code\\swim"}'),
			('project', 'swim', 'project.meta-updated', '{"workspaceRoot":"/code/personal/swim"}'),
			('project', 'swim', 'project.meta-updated', '{"title":"Swim"}'),
			('project', 'swim', 'project.meta-updated', '{"workspaceRoot":"/code/owned/swim"}'),
			('project', 'home', 'project.created', '{"workspaceRoot":"C:\\Users\\u"}'),
			('project', 'home', 'project.meta-updated', '{"workspaceRoot":"/home/u"}'),
			('project', 'reuser', 'project.created', '{"workspaceRoot":"/code/notes"}'),
			('project', 'reuser', 'project.meta-updated', '{"workspaceRoot":"/code/reused"}'),
			('project', 'notes', 'project.created', '{"workspaceRoot":"/code/notes"}'),
			('project', 'left', 'project.created', '{"workspaceRoot":"/code/shared"}'),
			('project', 'left', 'project.meta-updated', '{"workspaceRoot":"/code/left"}'),
			('project', 'right', 'project.created', '{"workspaceRoot":"/code/shared"}'),
			('project', 'right', 'project.meta-updated', '{"workspaceRoot":"/code/right"}'),
			('project', 'bad', 'project.created', 'not json'),
			('thread', 'x', 'thread.created', '{"workspaceRoot":"/code/thread"}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadT3ProjectLineageReadsDeletedWorktreesAndMoves(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("fixture roots are POSIX paths")
	}
	baseDir := t.TempDir()
	makeT3HistoryDB(t, baseDir)
	t.Setenv("T3CODE_HOME", baseDir)

	lineage, err := LoadT3ProjectLineage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantWorktrees := map[string]string{
		"/wt/notes/live":      "/code/notes",
		"/wt/swim/old-branch": "/code/owned/swim",
	}
	if len(lineage.WorktreeRoots) != len(wantWorktrees) {
		t.Errorf("worktree roots = %v, want %v", lineage.WorktreeRoots, wantWorktrees)
	}
	for path, root := range wantWorktrees {
		if got := lineage.WorktreeRoots[path]; got != root {
			t.Errorf("worktree %q -> %q, want %q", path, got, root)
		}
	}

	// /code/notes was vacated by one project but is another's current root,
	// and /code/shared moved to two places, so neither is evidence
	wantMoves := map[string]string{
		`C:\Users\u\code\swim`: "/code/owned/swim",
		"/code/personal/swim":  "/code/owned/swim",
		`C:\Users\u`:           "/home/u",
	}
	if len(lineage.MovedRoots) != len(wantMoves) {
		t.Errorf("moved roots = %v, want %v", lineage.MovedRoots, wantMoves)
	}
	for from, to := range wantMoves {
		if got := lineage.MovedRoots[from]; got != to {
			t.Errorf("move %q -> %q, want %q", from, got, to)
		}
	}
}
