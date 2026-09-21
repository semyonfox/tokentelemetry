package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// OpenClaw reads both the current per-agent SQLite store and legacy/materialized
// JSONL transcripts. Native event IDs make the two representations deduplicate.
type OpenClaw struct {
	roots   []string
	stateDB string
}

func NewOpenClaw() *OpenClaw {
	if state := os.Getenv("OPENCLAW_STATE_DIR"); state != "" {
		return &OpenClaw{
			roots:   existingOpenClawRoots(filepath.Join(state, "agents")),
			stateDB: existingOpenClawStateDB(filepath.Join(state, "state", "openclaw.sqlite")),
		}
	}
	h := homeDir()
	if h == "" {
		return &OpenClaw{}
	}
	states := []string{filepath.Join(h, ".openclaw"), filepath.Join(h, ".clawdbot"), filepath.Join(h, ".moltbot"), filepath.Join(h, ".moldbot")}
	return &OpenClaw{
		roots: existingOpenClawRoots(
			filepath.Join(states[0], "agents"), filepath.Join(states[1], "agents"),
			filepath.Join(states[2], "agents"), filepath.Join(states[3], "agents"),
		),
		stateDB: firstExistingOpenClawStateDB(states),
	}
}
func newOpenClawAt(roots ...string) *OpenClaw { return &OpenClaw{roots: roots} }
func newOpenClawAtState(stateDB string, roots ...string) *OpenClaw {
	return &OpenClaw{roots: roots, stateDB: stateDB}
}
func (o *OpenClaw) Agent() model.Agent { return model.Agent("openclaw") }
func (o *OpenClaw) Roots() []string    { return append([]string(nil), o.roots...) }

func firstExistingOpenClawStateDB(states []string) string {
	for _, state := range states {
		if path := existingOpenClawStateDB(filepath.Join(state, "state", "openclaw.sqlite")); path != "" {
			return path
		}
	}
	return ""
}

func existingOpenClawStateDB(path string) string {
	if fileExists(path) {
		return path
	}
	return ""
}

func existingOpenClawRoots(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if p = existingDir(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type openClawUsage struct {
	Input            int64 `json:"input"`
	Output           int64 `json:"output"`
	CacheRead        int64 `json:"cacheRead"`
	CacheWrite       int64 `json:"cacheWrite"`
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadInputTokens"`
	CacheWriteTokens int64 `json:"cacheWriteInputTokens"`
}
type openClawRecord struct {
	Type, CustomType, ID, Timestamp, CWD, Provider, ModelID string
	Data                                                    struct {
		Provider string `json:"provider"`
		ModelID  string `json:"modelId"`
	} `json:"data"`
	Message struct {
		Role, Model, Provider, ResponseID string
		Usage                             *openClawUsage `json:"usage"`
	} `json:"message"`
}
type openClawState struct {
	session, project, modelID, provider, runtimeSession string
	subagent                                            bool
	index                                               int
}

func (o *OpenClaw) Scan(ctx context.Context, emit func(model.Turn)) error {
	var scanErr error
	bindings, err := loadOpenClawCodexBindings(ctx, o.stateDB)
	if err != nil {
		scanErr = errors.Join(scanErr, err)
	}
	for _, root := range o.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			scanErr = errors.Join(scanErr, err)
			continue
		}
		for _, agent := range entries {
			if !agent.IsDir() {
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			base := filepath.Join(root, agent.Name())
			if db := filepath.Join(base, "agent", "openclaw-agent.sqlite"); fileExists(db) {
				if err := scanOpenClawDB(ctx, db, bindings, emit); err != nil {
					scanErr = errors.Join(scanErr, err)
				}
			}
			walkErr := filepath.WalkDir(filepath.Join(base, "sessions"), func(path string, e os.DirEntry, err error) error {
				if err != nil {
					if !errors.Is(err, os.ErrNotExist) {
						scanErr = errors.Join(scanErr, err)
					}
					return nil
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if e.IsDir() || !strings.Contains(e.Name(), ".jsonl") || strings.HasSuffix(e.Name(), ".lock") {
					return nil
				}
				for _, turn := range scanOpenClawFile(path, agent.Name()) {
					emit(turn)
				}
				return nil
			})
			if walkErr != nil {
				if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
					return walkErr
				}
				scanErr = errors.Join(scanErr, walkErr)
			}
		}
	}
	return scanErr
}

func scanOpenClawFile(path, fallbackProject string) []model.Turn {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	state := openClawState{session: openClawFileSession(path), project: fallbackProject}
	var turns []model.Turn
	for line := range jsonLines(f) {
		if turn, ok := parseOpenClawEvent(line, &state); ok {
			turns = append(turns, turn)
		}
	}
	return turns
}

func openClawFileSession(path string) string {
	base := filepath.Base(path)
	if i := strings.Index(base, ".jsonl"); i >= 0 {
		return base[:i]
	}
	return base
}

func parseOpenClawEvent(raw []byte, state *openClawState) (model.Turn, bool) {
	var r openClawRecord
	if json.Unmarshal(raw, &r) != nil {
		return model.Turn{}, false
	}
	switch r.Type {
	case "session":
		state.session = firstNonEmpty(r.ID, state.session)
		state.project = firstNonEmpty(r.CWD, state.project)
		return model.Turn{}, false
	case "model_change":
		state.modelID = firstNonEmpty(r.ModelID, state.modelID)
		state.provider = firstNonEmpty(r.Provider, state.provider)
		return model.Turn{}, false
	case "custom":
		if r.CustomType == "model-snapshot" {
			state.modelID = firstNonEmpty(r.Data.ModelID, state.modelID)
			state.provider = firstNonEmpty(r.Data.Provider, state.provider)
		}
		return model.Turn{}, false
	}
	if r.Type != "message" || r.Message.Role != "assistant" || r.Message.Usage == nil {
		return model.Turn{}, false
	}
	u0 := r.Message.Usage
	u := model.Usage{Input: firstPositive(u0.Input, u0.InputTokens), Output: firstPositive(u0.Output, u0.OutputTokens), CacheRead: firstPositive(u0.CacheRead, u0.CacheReadTokens), CacheWrite: firstPositive(u0.CacheWrite, u0.CacheWriteTokens)}
	u.ContextTokens = u.Input + u.CacheRead + u.CacheWrite
	u, ok := u.Sanitize()
	if !ok || u.IsZero() {
		return model.Turn{}, false
	}
	state.index++
	modelID := firstNonEmpty(r.Message.Model, state.modelID)
	provider := firstNonEmpty(r.Message.Provider, state.provider)
	unpriced := ""
	if modelID == "" {
		modelID = "unknown"
		unpriced = "OpenClaw transcript does not record the request model"
	}
	key := identityKey("openclaw-session", state.session, strconv.Itoa(state.index))
	if r.ID != "" {
		key = identityKey("openclaw-entry", r.ID)
	} else if r.Message.ResponseID != "" {
		key = identityKey("openclaw-response", r.Message.ResponseID)
	}
	turn := model.Turn{Key: key, SessionID: state.session, Agent: model.Agent("openclaw"), Timestamp: parseTime(r.Timestamp), Model: modelID, Provider: provider, Project: state.project, Usage: u, Subagent: state.subagent, UnpricedReason: unpriced}
	if state.runtimeSession != "" {
		turn.RuntimeAgent = model.AgentCodex
		turn.RuntimeSessionID = state.runtimeSession
	}
	return turn, true
}

func firstPositive(primary, legacy int64) int64 {
	if primary != 0 {
		return primary
	}
	return legacy
}

func scanOpenClawDB(ctx context.Context, path string, codexBindings map[string]string, emit func(model.Turn)) error {
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&_pragma=busy_timeout(3000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	columns, err := openClawSessionWindowColumns(ctx, db)
	if err != nil {
		return err
	}
	harnessColumn := "''"
	if columns["agent_harness_id"] {
		harnessColumn = "COALESCE(w.agent_harness_id,'')"
	}
	rows, err := db.QueryContext(ctx, `SELECT e.session_id,e.event_json,COALESCE(w.model_provider,''),COALESCE(w.model,''),COALESCE(w.parent_session_key,''),COALESCE(w.spawned_by,''),`+harnessColumn+` FROM transcript_events e LEFT JOIN session_windows w ON w.session_id=e.session_id ORDER BY e.session_id,e.seq`)
	if err != nil {
		return err
	}
	defer rows.Close()
	states := map[string]*openClawState{}
	for rows.Next() {
		var session, raw, provider, modelID, parent, spawned, harness string
		if err := rows.Scan(&session, &raw, &provider, &modelID, &parent, &spawned, &harness); err != nil {
			return err
		}
		state := states[session]
		if state == nil {
			state = &openClawState{session: session, provider: provider, modelID: modelID, subagent: parent != "" || spawned != ""}
			if harness == "codex" {
				state.runtimeSession = codexBindings[session]
			}
			states[session] = state
		}
		if turn, ok := parseOpenClawEvent([]byte(raw), state); ok {
			emit(turn)
		}
	}
	return rows.Err()
}

func openClawSessionWindowColumns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info('session_windows')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

// loadOpenClawCodexBindings reads only the persisted session-to-thread facts
// from the Codex plugin namespace. The state and per-agent databases are kept
// separate by OpenClaw, so both the active binding and the session window's
// codex harness marker are required before a turn is linked above.
func loadOpenClawCodexBindings(ctx context.Context, path string) (map[string]string, error) {
	bindings := map[string]string{}
	if path == "" || !fileExists(path) {
		return bindings, nil
	}
	db, err := sql.Open("sqlite", kiloSQLiteDSN(path))
	if err != nil {
		return bindings, err
	}
	defer db.Close()
	tables, err := kiloTables(ctx, db)
	if err != nil || !tables["plugin_state_entries"] {
		return bindings, err
	}
	rows, err := db.QueryContext(ctx, `
SELECT
  CASE WHEN json_valid(value_json) THEN json_extract(value_json,'$.sessionId') END,
  CASE WHEN json_valid(value_json) THEN json_extract(value_json,'$.state') END,
  CASE WHEN json_valid(value_json) THEN json_extract(value_json,'$.binding.threadId') END
FROM plugin_state_entries
WHERE plugin_id='codex' AND namespace='app-server-thread-bindings'
  AND (expires_at IS NULL OR expires_at > ?)`, time.Now().UnixMilli())
	if err != nil {
		return bindings, err
	}
	defer rows.Close()
	ambiguous := map[string]bool{}
	for rows.Next() {
		var session, state, thread sql.NullString
		if err := rows.Scan(&session, &state, &thread); err != nil {
			return bindings, err
		}
		if !session.Valid || !state.Valid || !thread.Valid || state.String != "active" {
			continue
		}
		sessionID, threadID := strings.TrimSpace(session.String), strings.TrimSpace(thread.String)
		if sessionID == "" || threadID == "" || ambiguous[sessionID] {
			continue
		}
		if previous := bindings[sessionID]; previous != "" && previous != threadID {
			delete(bindings, sessionID)
			ambiguous[sessionID] = true
			continue
		}
		bindings[sessionID] = threadID
	}
	return bindings, rows.Err()
}
