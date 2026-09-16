// Package projectmeta reads optional project lineage recorded by agent hosts.
package projectmeta

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
	_ "modernc.org/sqlite" // pure-Go driver: keeps CGO_ENABLED=0 cross-compiles working
)

const t3WorktreeRootsQuery = `
SELECT threads.worktree_path, projects.workspace_root
FROM projection_threads AS threads
INNER JOIN projection_projects AS projects
  ON projects.project_id = threads.project_id
WHERE threads.worktree_path IS NOT NULL
  AND projects.deleted_at IS NULL
`

const t3CodexSessionRootsQuery = `
SELECT runtime.resume_cursor_json, threads.worktree_path, projects.workspace_root
FROM provider_session_runtime AS runtime
INNER JOIN projection_threads AS threads
  ON threads.thread_id = runtime.thread_id
INNER JOIN projection_projects AS projects
  ON projects.project_id = threads.project_id
WHERE runtime.adapter_key = 'codex'
  AND runtime.resume_cursor_json IS NOT NULL
  AND threads.worktree_path IS NOT NULL
  AND projects.deleted_at IS NULL
`

type codexResumeCursor struct {
	ThreadID string `json:"threadId"`
}

// LoadT3ProjectLineage returns verified project relationships from T3's state.
// A missing T3 database is normal and returns empty lineage. The database is
// opened read-only; callers may still use partial lineage when an optional
// relation from an older or damaged database cannot be read.
func LoadT3ProjectLineage(ctx context.Context) (model.ProjectLineage, error) {
	lineage := emptyLineage()
	dbPath, err := t3StateDBPath()
	if err != nil || dbPath == "" {
		return lineage, err
	}
	return loadT3ProjectLineage(ctx, dbPath)
}

func emptyLineage() model.ProjectLineage {
	return model.ProjectLineage{
		SessionRoots:  make(map[model.ProjectSession]string),
		WorktreeRoots: make(map[string]string),
	}
}

func t3StateDBPath() (string, error) {
	baseDir := strings.TrimSpace(os.Getenv("T3CODE_HOME"))
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil
		}
		baseDir = filepath.Join(home, ".t3")
	}
	dbPath := filepath.Join(baseDir, "userdata", "state.sqlite")
	info, err := os.Stat(dbPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect T3 project metadata: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("inspect T3 project metadata: %s is not a regular file", dbPath)
	}
	return dbPath, nil
}

func loadT3ProjectLineage(ctx context.Context, dbPath string) (model.ProjectLineage, error) {
	lineage := emptyLineage()
	uriPath := filepath.ToSlash(dbPath)
	if filepath.VolumeName(dbPath) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     uriPath,
		RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(3000)",
	}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return lineage, fmt.Errorf("open T3 project metadata: %w", err)
	}
	defer db.Close()

	worktreeRoots, err := readT3WorktreeRoots(ctx, db)
	if err != nil {
		return lineage, err
	}
	lineage.WorktreeRoots = worktreeRoots

	sessionRoots, err := readT3CodexSessionRoots(ctx, db)
	if err != nil {
		return lineage, err
	}
	lineage.SessionRoots = sessionRoots
	return lineage, nil
}

func readT3WorktreeRoots(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, t3WorktreeRootsQuery)
	if err != nil {
		return nil, fmt.Errorf("read T3 worktree metadata: %w", err)
	}
	defer rows.Close()

	candidates := make(map[string]map[string]struct{})
	for rows.Next() {
		var worktreePath, workspaceRoot string
		if err := rows.Scan(&worktreePath, &workspaceRoot); err != nil {
			return nil, fmt.Errorf("read T3 worktree metadata row: %w", err)
		}
		worktreePath, worktreeOK := cleanAbsolutePath(worktreePath)
		workspaceRoot, rootOK := cleanAbsolutePath(workspaceRoot)
		if !worktreeOK || !rootOK {
			continue
		}
		if candidates[worktreePath] == nil {
			candidates[worktreePath] = make(map[string]struct{})
		}
		candidates[worktreePath][workspaceRoot] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read T3 worktree metadata: %w", err)
	}

	roots := make(map[string]string, len(candidates))
	for worktreePath, workspaceRoots := range candidates {
		// A reused path pointing at different projects is not evidence. Keep it
		// separate rather than assigning its historical usage by guesswork.
		if len(workspaceRoots) != 1 {
			continue
		}
		for workspaceRoot := range workspaceRoots {
			roots[worktreePath] = workspaceRoot
		}
	}
	return roots, nil
}

func readT3CodexSessionRoots(ctx context.Context, db *sql.DB) (map[model.ProjectSession]string, error) {
	rows, err := db.QueryContext(ctx, t3CodexSessionRootsQuery)
	if err != nil {
		return nil, fmt.Errorf("read T3 Codex session metadata: %w", err)
	}
	defer rows.Close()

	candidates := make(map[model.ProjectSession]map[string]struct{})
	for rows.Next() {
		var rawCursor, worktreePath, workspaceRoot string
		if err := rows.Scan(&rawCursor, &worktreePath, &workspaceRoot); err != nil {
			return nil, fmt.Errorf("read T3 Codex session metadata row: %w", err)
		}
		var cursor codexResumeCursor
		if json.Unmarshal([]byte(rawCursor), &cursor) != nil {
			continue
		}
		cursor.ThreadID = strings.TrimSpace(cursor.ThreadID)
		_, worktreeOK := cleanAbsolutePath(worktreePath)
		workspaceRoot, rootOK := cleanAbsolutePath(workspaceRoot)
		if cursor.ThreadID == "" || !worktreeOK || !rootOK {
			continue
		}
		session := model.ProjectSession{Agent: model.AgentCodex, SessionID: cursor.ThreadID}
		if candidates[session] == nil {
			candidates[session] = make(map[string]struct{})
		}
		candidates[session][workspaceRoot] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read T3 Codex session metadata: %w", err)
	}

	roots := make(map[model.ProjectSession]string, len(candidates))
	for session, workspaceRoots := range candidates {
		if len(workspaceRoots) != 1 {
			continue
		}
		for workspaceRoot := range workspaceRoots {
			roots[session] = workspaceRoot
		}
	}
	return roots, nil
}

func cleanAbsolutePath(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) {
		return "", false
	}
	return filepath.Clean(value), true
}
