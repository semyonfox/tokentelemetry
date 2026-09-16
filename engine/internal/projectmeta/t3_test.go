package projectmeta

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
)

type t3TestPaths struct {
	alphaRoot         string
	alphaWorktree     string
	sharedWorktree    string
	invalidWorktree   string
	deletedWorktree   string
	malformedWorktree string
	claudeWorktree    string
}

func makeT3StateDB(t *testing.T, baseDir string) (string, t3TestPaths) {
	t.Helper()
	paths := t3TestPaths{
		alphaRoot:         filepath.Join(baseDir, "repos", "alpha"),
		alphaWorktree:     filepath.Join(baseDir, "old", "worktrees", "alpha"),
		sharedWorktree:    filepath.Join(baseDir, "old", "worktrees", "shared"),
		invalidWorktree:   filepath.Join(baseDir, "old", "worktrees", "invalid"),
		deletedWorktree:   filepath.Join(baseDir, "old", "worktrees", "deleted"),
		malformedWorktree: filepath.Join(baseDir, "old", "worktrees", "malformed"),
		claudeWorktree:    filepath.Join(baseDir, "old", "worktrees", "claude"),
	}
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
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	statements := []struct {
		query string
		args  []any
	}{
		{
			`INSERT INTO projection_projects VALUES
				('alpha', ?, NULL),
				('alpha-copy', ?, NULL),
				('beta', ?, NULL),
				('invalid', 'relative/root', NULL),
				('deleted', ?, '2026-09-01')`,
			[]any{
				paths.alphaRoot,
				paths.alphaRoot + string(filepath.Separator) + ".",
				filepath.Join(baseDir, "repos", "beta"),
				filepath.Join(baseDir, "repos", "deleted"),
			},
		},
		{
			`INSERT INTO projection_threads VALUES
				('alpha-1', 'alpha', ?),
				('alpha-2', 'alpha-copy', ?),
				('shared-alpha', 'alpha', ?),
				('shared-beta', 'beta', ?),
				('relative-worktree', 'alpha', 'relative/worktree'),
				('invalid-root', 'invalid', ?),
				('local-thread', 'alpha', NULL),
				('deleted-thread', 'deleted', ?),
				('malformed-cursor', 'alpha', ?),
				('claude-cursor', 'alpha', ?)`,
			[]any{
				paths.alphaWorktree,
				paths.alphaWorktree + string(filepath.Separator) + ".",
				paths.sharedWorktree,
				paths.sharedWorktree,
				paths.invalidWorktree,
				paths.deletedWorktree,
				paths.malformedWorktree,
				paths.claudeWorktree,
			},
		},
		{
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
			nil,
		},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath, paths
}

func TestLoadT3ProjectLineageUsesExplicitHomeAndRejectsAmbiguity(t *testing.T) {
	baseDir := t.TempDir()
	dbPath, paths := makeT3StateDB(t, baseDir)
	t.Setenv("T3CODE_HOME", baseDir)
	if err := os.Chmod(dbPath, 0400); err != nil {
		t.Fatal(err)
	}

	lineage, err := LoadT3ProjectLineage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Clean(paths.alphaWorktree)
	if got, want := lineage.WorktreeRoots[wantPath], filepath.Clean(paths.alphaRoot); got != want {
		t.Errorf("alpha root = %q, want %q", got, want)
	}
	for _, rejected := range []string{
		paths.sharedWorktree,
		"relative/worktree",
		paths.invalidWorktree,
		paths.deletedWorktree,
	} {
		if root, ok := lineage.WorktreeRoots[filepath.Clean(rejected)]; ok {
			t.Errorf("invalid, deleted, or ambiguous mapping %q -> %q was retained", rejected, root)
		}
	}
	if len(lineage.WorktreeRoots) != 3 {
		t.Errorf("worktree roots = %v, want three verified mappings", lineage.WorktreeRoots)
	}

	alpha := model.ProjectSession{Agent: model.AgentCodex, SessionID: "codex-alpha"}
	if got, want := lineage.SessionRoots[alpha], filepath.Clean(paths.alphaRoot); got != want {
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
	_, paths := makeT3StateDB(t, filepath.Join(home, ".t3"))
	t.Setenv("T3CODE_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	lineage, err := LoadT3ProjectLineage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := lineage.WorktreeRoots[filepath.Clean(paths.alphaWorktree)]; got != filepath.Clean(paths.alphaRoot) {
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
