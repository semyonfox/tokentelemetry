package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// OpenCode scans the OpenCode CLI's SQLite store.
//
// Layout is ~/.local/share/opencode/opencode.db, with a `message` row per turn
// whose `data` column holds the JSON payload — including per-message token
// counts and the model that served it. That makes this the most precise of the
// SQLite-backed agents: unlike Hermes, the per-call detail survives, so turns
// carry their own clock and bucket into the correct local day.
//
// Token counts here are already NET of cache, and reasoning is reported beside
// output rather than inside it: the payload's total equals
// input + output + reasoning + cache.read + cache.write. Input must not be
// reduced again, and reasoning is folded into output (see kiloTokens.usage).
type OpenCode struct {
	dbPath  string
	dbPaths []string
}

func NewOpenCode() *OpenCode {
	data := os.Getenv("OPENCODE_DATA_DIR")
	if data == "" {
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			base = filepath.Join(homeDir(), ".local", "share")
		}
		data = filepath.Join(base, "opencode")
	}
	if override := os.Getenv("OPENCODE_DB"); override != "" {
		if override == ":memory:" {
			return &OpenCode{}
		}
		if !filepath.IsAbs(override) {
			override = filepath.Join(data, override)
		}
		return &OpenCode{dbPaths: kiloExistingUniqueFiles([]string{override})}
	}
	paths, _ := filepath.Glob(filepath.Join(data, "opencode*.db"))
	if len(paths) > 0 {
		return &OpenCode{dbPaths: kiloExistingUniqueFiles(paths)}
	}
	if os.Getenv("OPENCODE_DATA_DIR") != "" {
		return &OpenCode{}
	}
	// XDG first, then the legacy location the older builds used.
	var candidates []string
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		candidates = append(candidates, filepath.Join(x, "opencode", "opencode.db"))
	}
	if home := homeDir(); home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".local", "share", "opencode", "opencode.db"),
			filepath.Join(home, ".opencode", "opencode.db"))
	}
	for _, c := range candidates {
		if fileExists(c) {
			return &OpenCode{dbPath: c}
		}
	}
	return &OpenCode{}
}

func (o *OpenCode) Agent() model.Agent { return model.AgentOpenCode }

func (o *OpenCode) Roots() []string {
	if len(o.dbPaths) > 0 {
		return append([]string(nil), o.dbPaths...)
	}
	if o.dbPath == "" {
		return nil
	}
	return []string{o.dbPath}
}

// openCodeMessage is the subset of the JSON payload we need.
type openCodeMessage struct {
	Role       string     `json:"role"`
	ModelID    string     `json:"modelID"`
	ProviderID string     `json:"providerID"`
	Tokens     kiloTokens `json:"tokens"`
	Time       struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

const openCodeQuery = `
SELECT m.id, m.session_id, CAST(m.time_created AS INTEGER), m.data,
       COALESCE(s.directory, '')
FROM message m
LEFT JOIN session s ON s.id = m.session_id
`

func (o *OpenCode) Scan(ctx context.Context, emit func(model.Turn)) error {
	var errs []error
	for _, path := range o.Roots() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := scanOpenCodeDB(ctx, path, emit); err != nil {
			errs = append(errs, fmt.Errorf("opencode %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}

func scanOpenCodeDB(ctx context.Context, path string, emit func(model.Turn)) error {
	db, err := sql.Open("sqlite", kiloSQLiteDSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	tables, err := kiloTables(ctx, db)
	if err != nil {
		return err
	}
	modern := map[string]*model.Turn{}
	if tables["session_message"] {
		modern, err = scanOpenCodeModern(ctx, db, tables)
		if err != nil {
			return err
		}
		for _, turn := range modern {
			if turn != nil {
				emit(*turn)
			}
		}
	}
	if !tables["message"] {
		if !tables["session_message"] {
			return errors.New("unsupported schema: no message or session_message table")
		}
		return nil
	}

	rows, err := db.QueryContext(ctx, openCodeQuery)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, sessionID, dir string
			data               []byte
			created            int64
		)
		if err := rows.Scan(&id, &sessionID, &created, &data, &dir); err != nil {
			continue
		}
		if _, exists := modern[id]; exists {
			continue
		}
		// a row without a token block has no usage, and user rows are the
		// large ones, so they are skipped before decoding
		if !bytes.Contains(data, openCodeTokensKey) {
			continue
		}
		var m openCodeMessage
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if m.Role != "assistant" {
			continue
		}
		if m.ModelID == "" {
			m.ModelID = "unknown"
		}
		usage, ok := m.Tokens.usage().Sanitize()
		if !ok || usage.IsZero() {
			continue
		}
		// Prefer the payload's own clock; fall back to the row's.
		ts := m.Time.Created
		if ts == 0 {
			ts = created
		}
		emit(model.Turn{
			Key:       "opencode|" + id,
			SessionID: sessionID,
			Agent:     model.AgentOpenCode,
			Timestamp: unixMillis(ts),
			Model:     m.ModelID,
			Provider:  m.ProviderID,
			Project:   dir,
			Usage:     usage,
		})
	}
	return rows.Err()
}

// OpenCode and Kilo share this payload family. OpenCode briefly used
// session_v2, then merged session metadata back into session. Probe that join;
// never join new messages to frozen metadata from the wrong generation.
func scanOpenCodeModern(ctx context.Context, db *sql.DB, tables map[string]bool) (map[string]*model.Turn, error) {
	sessionTable := "session"
	if tables["session_v2"] {
		sessionTable = "session_v2"
	}
	query := `SELECT m.id,m.session_id,CAST(m.time_created AS INTEGER),m.data,COALESCE(s.directory,''),COALESCE(s.parent_id,'') FROM session_message m LEFT JOIN ` + sessionTable + ` s ON s.id=m.session_id WHERE m.type='assistant' ORDER BY m.time_created,m.id`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	turns := map[string]*model.Turn{}
	for rows.Next() {
		var id, session, data, project, parent string
		var created int64
		if err := rows.Scan(&id, &session, &created, &data, &project, &parent); err != nil {
			return nil, err
		}
		turns[id] = nil
		var message kiloCurrentMessage
		if json.Unmarshal([]byte(data), &message) != nil || message.Tokens == nil {
			continue
		}
		modelID := message.Model.ID
		if modelID == "" {
			modelID = "unknown"
		}
		turn, ok := kiloTurn(id, session, project, created, modelID, message.Model.ProviderID, parent != "", *message.Tokens)
		if !ok {
			continue
		}
		turn.Key = "opencode|" + id
		turn.Agent = model.AgentOpenCode
		turns[id] = &turn
	}
	return turns, rows.Err()
}

var openCodeTokensKey = []byte(`"tokens"`)

// unixMillis converts JavaScript-style epoch milliseconds to a time.
func unixMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
