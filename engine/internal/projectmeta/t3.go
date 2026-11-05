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

// t3V2WorktreeRootsQuery reads the v2 thread projection, which keeps deleted
// threads. The v1 table only holds live ones, so most historical worktrees are
// only here. A malformed payload yields NULL instead of failing the query.
const t3V2WorktreeRootsQuery = `
SELECT threads.worktree_path, projects.workspace_root
FROM (
  SELECT project_id,
    CASE WHEN json_valid(payload_json) THEN json_extract(payload_json, '$.worktreePath') END AS worktree_path
  FROM orchestration_v2_projection_threads
) AS threads
INNER JOIN projection_projects AS projects
  ON projects.project_id = threads.project_id
WHERE threads.worktree_path IS NOT NULL
  AND projects.deleted_at IS NULL
`

// t3ProjectRootEventsQuery lists every workspace root a project has had, in
// order, so an earlier root can be mapped to where the project lives now.
const t3ProjectRootEventsQuery = `
SELECT stream_id,
  CASE WHEN json_valid(payload_json) THEN json_extract(payload_json, '$.workspaceRoot') END
FROM orchestration_events
WHERE aggregate_kind = 'project'
  AND event_type IN ('project.created', 'project.meta-updated')
ORDER BY sequence
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
		MovedRoots:    make(map[string]string),
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
	// T3 moved its live state to statev2.sqlite with the same projection
	// tables. The old state.sqlite stays behind frozen, so reading it gives
	// stale lineage, and it is often offloaded somewhere slow.
	for _, name := range []string{"statev2.sqlite", "state.sqlite"} {
		dbPath := filepath.Join(baseDir, "userdata", name)
		info, err := os.Stat(dbPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect T3 project metadata: %w", err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("inspect T3 project metadata: %s is not a regular file", dbPath)
		}
		return dbPath, nil
	}
	return "", nil
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

	movedRoots, err := readT3MovedRoots(ctx, db)
	if err != nil {
		return lineage, err
	}
	lineage.MovedRoots = movedRoots
	return lineage, nil
}

func readT3WorktreeRoots(ctx context.Context, db *sql.DB) (map[string]string, error) {
	candidates := make(map[string]map[string]struct{})
	if err := collectT3WorktreeRoots(ctx, db, t3WorktreeRootsQuery, candidates); err != nil {
		return nil, err
	}
	if ok, err := t3TableExists(ctx, db, "orchestration_v2_projection_threads"); err != nil {
		return nil, err
	} else if ok {
		if err := collectT3WorktreeRoots(ctx, db, t3V2WorktreeRootsQuery, candidates); err != nil {
			return nil, err
		}
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

func collectT3WorktreeRoots(ctx context.Context, db *sql.DB, query string, candidates map[string]map[string]struct{}) error {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("read T3 worktree metadata: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var worktreePath, workspaceRoot string
		if err := rows.Scan(&worktreePath, &workspaceRoot); err != nil {
			return fmt.Errorf("read T3 worktree metadata row: %w", err)
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
		return fmt.Errorf("read T3 worktree metadata: %w", err)
	}
	return nil
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

func t3TableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect T3 metadata tables: %w", err)
	}
	return true, nil
}

// readT3MovedRoots maps each earlier workspace root of a project to the root
// it has now. Roots stay as T3 recorded them, so a project moved from Windows
// to Linux keeps its Windows spelling for the report to normalise.
func readT3MovedRoots(ctx context.Context, db *sql.DB) (map[string]string, error) {
	moved := make(map[string]string)
	if ok, err := t3TableExists(ctx, db, "orchestration_events"); err != nil || !ok {
		return moved, err
	}
	rows, err := db.QueryContext(ctx, t3ProjectRootEventsQuery)
	if err != nil {
		return nil, fmt.Errorf("read T3 project history: %w", err)
	}
	defer rows.Close()

	var order []string
	history := make(map[string][]string)
	for rows.Next() {
		var project string
		var root sql.NullString
		if err := rows.Scan(&project, &root); err != nil {
			return nil, fmt.Errorf("read T3 project history row: %w", err)
		}
		workspaceRoot, ok := hostAbsolutePath(root.String)
		if !root.Valid || !ok {
			continue
		}
		roots := history[project]
		if len(roots) == 0 {
			order = append(order, project)
		}
		if len(roots) == 0 || roots[len(roots)-1] != workspaceRoot {
			history[project] = append(roots, workspaceRoot)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read T3 project history: %w", err)
	}

	current := make(map[string]bool, len(history))
	for _, roots := range history {
		current[roots[len(roots)-1]] = true
	}
	candidates := make(map[string]map[string]struct{})
	for _, project := range order {
		roots := history[project]
		latest := roots[len(roots)-1]
		for _, earlier := range roots[:len(roots)-1] {
			// a root some project still lives at was reused, not vacated
			if current[earlier] {
				continue
			}
			if candidates[earlier] == nil {
				candidates[earlier] = make(map[string]struct{})
			}
			candidates[earlier][latest] = struct{}{}
		}
	}
	for earlier, latest := range candidates {
		if len(latest) != 1 {
			continue
		}
		for root := range latest {
			moved[earlier] = root
		}
	}
	return moved, nil
}

// hostAbsolutePath accepts an absolute path from any host T3 ran on. Project
// history can predate a move between operating systems, so a Windows root is
// valid evidence on Linux and the other way round.
func hostAbsolutePath(value string) (string, bool) {
	value = strings.TrimSpace(value)
	switch {
	case strings.HasPrefix(value, "/"):
		return value, true
	case len(value) >= 3 && value[1] == ':' && (value[2] == '\\' || value[2] == '/') &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')):
		return value, true
	case strings.HasPrefix(value, `\\`):
		return value, true
	}
	return "", false
}
