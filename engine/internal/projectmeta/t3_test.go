package projectmeta

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
)

func makeT3StateDB(t *testing.T, baseDir string) string {
	t.Helper()
	stateDir := filepath.Join(baseDir, "userdata")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(stateDir, "state.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE projection_projects (
			project_id TEXT PRIMARY KEY,
			workspace_root TEXT NOT NULL,
			deleted_at TEXT
		)`,
		`CREATE TABLE projection_threads (
			thread_id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			worktree_path TEXT
		)`,
		`CREATE TABLE provider_session_runtime (
			thread_id TEXT PRIMARY KEY,
			adapter_key TEXT NOT NULL,
			resume_cursor_json TEXT
		)`,
		`INSERT INTO projection_projects VALUES
			('alpha', '/repos/alpha', NULL),
			('alpha-copy', '/repos/alpha/.', NULL),
			('beta', '/repos/beta', NULL),
			('invalid', 'relative/root', NULL),
			('deleted', '/repos/deleted', '2026-09-01')`,
		`INSERT INTO projection_threads VALUES
			('alpha-1', 'alpha', '/old/worktrees/alpha'),
			('alpha-2', 'alpha-copy', '/old/worktrees/alpha/.'),
			('shared-alpha', 'alpha', '/old/worktrees/shared'),
			('shared-beta', 'beta', '/old/worktrees/shared'),
			('relative-worktree', 'alpha', 'relative/worktree'),
			('invalid-root', 'invalid', '/old/worktrees/invalid'),
			('local-thread', 'alpha', NULL),
			('deleted-thread', 'deleted', '/old/worktrees/deleted'),
			('malformed-cursor', 'alpha', '/old/worktrees/malformed'),
			('claude-cursor', 'alpha', '/old/worktrees/claude')`,
		`INSERT INTO provider_session_runtime VALUES
			('alpha-1', 'codex', '{"threadId":"codex-alpha"}'),
			('alpha-2', 'codex', '{"threadId":"codex-alpha"}'),
			('shared-alpha', 'codex', '{"threadId":"codex-shared"}'),
			('shared-beta', 'codex', '{"threadId":"codex-shared"}'),
			('relative-worktree', 'codex', '{"threadId":"codex-relative-worktree"}'),
			('invalid-root', 'codex', '{"threadId":"codex-invalid-root"}'),
			('local-thread', 'codex', '{"threadId":"codex-local"}'),
			('deleted-thread', 'codex', '{"threadId":"codex-deleted"}'),
			('malformed-cursor', 'codex', 'not-json'),
			('claude-cursor', 'claudeAgent', '{"threadId":"not-a-codex-session"}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestLoadT3ProjectLineageUsesExplicitHomeAndRejectsAmbiguity(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := makeT3StateDB(t, baseDir)
	t.Setenv("T3CODE_HOME", baseDir)
	if err := os.Chmod(dbPath, 0400); err != nil {
		t.Fatal(err)
	}

	lineage, err := LoadT3ProjectLineage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Clean("/old/worktrees/alpha")
	if got, want := lineage.WorktreeRoots[wantPath], filepath.Clean("/repos/alpha"); got != want {
		t.Errorf("alpha root = %q, want %q", got, want)
	}
	for _, rejected := range []string{
		"/old/worktrees/shared",
		"relative/worktree",
		"/old/worktrees/invalid",
		"/old/worktrees/deleted",
	} {
		if root, ok := lineage.WorktreeRoots[filepath.Clean(rejected)]; ok {
			t.Errorf("invalid, deleted, or ambiguous mapping %q -> %q was retained", rejected, root)
		}
	}
	if len(lineage.WorktreeRoots) != 3 {
		t.Errorf("worktree roots = %v, want three verified mappings", lineage.WorktreeRoots)
	}

	alpha := model.ProjectSession{Agent: model.AgentCodex, SessionID: "codex-alpha"}
	if got, want := lineage.SessionRoots[alpha], filepath.Clean("/repos/alpha"); got != want {
		t.Errorf("Codex session root = %q, want %q", got, want)
	}
	for _, rejected := range []string{
		"codex-shared",
		"codex-relative-worktree",
		"codex-invalid-root",
		"codex-local",
		"codex-deleted",
		"not-a-codex-session",
	} {
		session := model.ProjectSession{Agent: model.AgentCodex, SessionID: rejected}
		if root, ok := lineage.SessionRoots[session]; ok {
			t.Errorf("invalid, out-of-scope, or ambiguous session %q -> %q was retained", rejected, root)
		}
	}
	if len(lineage.SessionRoots) != 1 {
		t.Errorf("session roots = %v, want one verified mapping", lineage.SessionRoots)
	}
}

func TestLoadT3ProjectLineageUsesDefaultHome(t *testing.T) {
	home := t.TempDir()
	makeT3StateDB(t, filepath.Join(home, ".t3"))
	t.Setenv("T3CODE_HOME", "")
	t.Setenv("HOME", home)

	lineage, err := LoadT3ProjectLineage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := lineage.WorktreeRoots[filepath.Clean("/old/worktrees/alpha")]; got != filepath.Clean("/repos/alpha") {
		t.Errorf("default-home root = %q", got)
	}
}

func TestLoadT3ProjectLineageDoesNotCreateMissingDatabase(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv("T3CODE_HOME", baseDir)
	dbPath := filepath.Join(baseDir, "userdata", "state.sqlite")

	lineage, err := LoadT3ProjectLineage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(lineage.SessionRoots) != 0 || len(lineage.WorktreeRoots) != 0 {
		t.Fatalf("lineage = %+v, want empty", lineage)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("missing database was created: %v", err)
	}
}
