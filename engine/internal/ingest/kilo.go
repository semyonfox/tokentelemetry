package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Kilo reads Kilo Code's SQLite databases. Current Kilo builds store complete
// assistant calls in session_message. The older message table can remain in
// the same database after migration, so current rows take precedence by ID and
// legacy rows contribute only calls that have no current counterpart.
type Kilo struct {
	dbPaths []string
}

func NewKilo() *Kilo {
	dataDir := kiloDataDir()
	if override := os.Getenv("KILO_DB"); override != "" {
		if override == ":memory:" {
			return &Kilo{}
		}
		if !filepath.IsAbs(override) {
			override = filepath.Join(dataDir, override)
		}
		if fileExists(override) {
			return &Kilo{dbPaths: []string{override}}
		}
		return &Kilo{}
	}

	if dataDir == "" {
		return &Kilo{}
	}
	paths := []string{filepath.Join(dataDir, "kilo.db")}
	for _, pattern := range []string{"kilo-*.db", "opencode-*.db"} {
		matches, _ := filepath.Glob(filepath.Join(dataDir, pattern))
		paths = append(paths, matches...)
	}
	return &Kilo{dbPaths: kiloExistingUniqueFiles(paths)}
}

func (k *Kilo) Agent() model.Agent { return model.Agent("kilo-code") }

func (k *Kilo) Roots() []string {
	return append([]string(nil), k.dbPaths...)
}

func kiloDataDir() string {
	if data := os.Getenv("XDG_DATA_HOME"); data != "" {
		return filepath.Join(data, "kilo")
	}
	home := homeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "kilo")
}

func kiloExistingUniqueFiles(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	var out []string
	for _, path := range paths {
		path = filepath.Clean(path)
		if seen[path] || !fileExists(path) {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

type kiloTokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

type kiloCurrentMessage struct {
	Model struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
	} `json:"model"`
	Tokens *kiloTokens `json:"tokens"`
	Time   struct {
		Created int64 `json:"created"`
	} `json:"time"`
}

type kiloLegacyMessage struct {
	Role       string      `json:"role"`
	ModelID    string      `json:"modelID"`
	ProviderID string      `json:"providerID"`
	Tokens     *kiloTokens `json:"tokens"`
	Time       struct {
		Created int64 `json:"created"`
	} `json:"time"`
}

type kiloCandidate struct {
	// key remains set when a current row has no usable usage. That row must
	// still mask a stale legacy copy with the same message ID.
	key     string
	turn    model.Turn
	current bool
}

func (k *Kilo) Scan(ctx context.Context, emit func(model.Turn)) error {
	byID := make(map[string]kiloCandidate)
	var scanErr error
	for _, path := range k.Roots() {
		if err := ctx.Err(); err != nil {
			return err
		}
		candidates, err := scanKiloDB(ctx, path)
		if err != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("kilo database %q: %w", path, err))
		}
		for _, candidate := range candidates {
			previous, exists := byID[candidate.key]
			if !exists || preferKiloCandidate(candidate, previous) {
				byID[candidate.key] = candidate
			}
		}
	}

	turns := make([]model.Turn, 0, len(byID))
	for _, candidate := range byID {
		if candidate.turn.Key != "" {
			turns = append(turns, candidate.turn)
		}
	}
	sort.Slice(turns, func(i, j int) bool {
		if turns[i].Timestamp.Equal(turns[j].Timestamp) {
			return turns[i].Key < turns[j].Key
		}
		return turns[i].Timestamp.Before(turns[j].Timestamp)
	})
	for _, turn := range turns {
		emit(turn)
	}
	return scanErr
}

func preferKiloCandidate(next, previous kiloCandidate) bool {
	if next.current != previous.current {
		return next.current
	}
	if (next.turn.Key != "") != (previous.turn.Key != "") {
		return next.turn.Key != ""
	}
	return next.turn.Key != "" && moreCompleteUsage(next.turn.Usage, previous.turn.Usage)
}

func scanKiloDB(ctx context.Context, path string) ([]kiloCandidate, error) {
	db, err := sql.Open("sqlite", kiloSQLiteDSN(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()

	tables, err := kiloTables(ctx, db)
	if err != nil {
		return nil, err
	}
	if !tables["session_message"] && !tables["message"] {
		return nil, errors.New("unsupported schema: neither session_message nor message table exists")
	}
	if !tables["session"] {
		return nil, errors.New("unsupported schema: session table is missing")
	}
	if err := requireKiloColumns(ctx, db, "session", "id", "directory", "parent_id"); err != nil {
		return nil, err
	}

	var candidates []kiloCandidate
	var formatErr error
	if tables["session_message"] {
		if err := requireKiloColumns(ctx, db, "session_message", "id", "session_id", "type", "seq", "time_created", "data"); err != nil {
			formatErr = errors.Join(formatErr, err)
		} else {
			rows, err := scanKiloCurrent(ctx, db)
			if err != nil {
				formatErr = errors.Join(formatErr, err)
			} else {
				candidates = append(candidates, rows...)
			}
		}
	}
	if tables["message"] {
		if err := requireKiloColumns(ctx, db, "message", "id", "session_id", "time_created", "data"); err != nil {
			formatErr = errors.Join(formatErr, err)
		} else {
			rows, err := scanKiloLegacy(ctx, db)
			if err != nil {
				formatErr = errors.Join(formatErr, err)
			} else {
				candidates = append(candidates, rows...)
			}
		}
	}
	return candidates, formatErr
}

func kiloSQLiteDSN(path string) string {
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{
		Scheme:   "file",
		Path:     uriPath,
		RawQuery: "mode=ro&_pragma=busy_timeout(3000)",
	}).String()
}

func kiloTables(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'table'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables[strings.ToLower(name)] = true
	}
	return tables, rows.Err()
}

func requireKiloColumns(ctx context.Context, db *sql.DB, table string, required ...string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, primaryKey int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range required {
		if !columns[name] {
			return fmt.Errorf("unsupported schema: %s.%s is missing", table, name)
		}
	}
	return nil
}

const kiloCurrentQuery = `
SELECT sm.id, sm.session_id, CAST(sm.time_created AS INTEGER), sm.data,
       COALESCE(s.directory, ''),
       CASE WHEN s.parent_id IS NOT NULL AND s.parent_id <> '' THEN 1 ELSE 0 END
FROM session_message sm
LEFT JOIN session s ON s.id = sm.session_id
WHERE sm.type = 'assistant'
ORDER BY sm.time_created, sm.id
`

func scanKiloCurrent(ctx context.Context, db *sql.DB) ([]kiloCandidate, error) {
	rows, err := db.QueryContext(ctx, kiloCurrentQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []kiloCandidate
	for rows.Next() {
		var id, sessionID, data, directory string
		var created int64
		var subagent bool
		if err := rows.Scan(&id, &sessionID, &created, &data, &directory, &subagent); err != nil {
			return nil, err
		}
		key := identityKey("kilo-message", id)
		var message kiloCurrentMessage
		if json.Unmarshal([]byte(data), &message) != nil || message.Tokens == nil {
			out = append(out, kiloCandidate{key: key, current: true})
			continue
		}
		if created <= 0 {
			created = message.Time.Created
		}
		turn, ok := kiloTurn(id, sessionID, directory, created, message.Model.ID, message.Model.ProviderID, subagent, *message.Tokens)
		if ok {
			out = append(out, kiloCandidate{key: key, turn: turn, current: true})
		} else {
			out = append(out, kiloCandidate{key: key, current: true})
		}
	}
	return out, rows.Err()
}

const kiloLegacyQuery = `
SELECT m.id, m.session_id, CAST(m.time_created AS INTEGER), m.data,
       COALESCE(s.directory, ''),
       CASE WHEN s.parent_id IS NOT NULL AND s.parent_id <> '' THEN 1 ELSE 0 END
FROM message m
LEFT JOIN session s ON s.id = m.session_id
ORDER BY m.time_created, m.id
`

func scanKiloLegacy(ctx context.Context, db *sql.DB) ([]kiloCandidate, error) {
	rows, err := db.QueryContext(ctx, kiloLegacyQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []kiloCandidate
	for rows.Next() {
		var id, sessionID, data, directory string
		var created int64
		var subagent bool
		if err := rows.Scan(&id, &sessionID, &created, &data, &directory, &subagent); err != nil {
			return nil, err
		}
		var message kiloLegacyMessage
		if json.Unmarshal([]byte(data), &message) != nil || message.Role != "assistant" || message.Tokens == nil {
			continue
		}
		if message.Time.Created > 0 {
			created = message.Time.Created
		}
		turn, ok := kiloTurn(id, sessionID, directory, created, message.ModelID, message.ProviderID, subagent, *message.Tokens)
		if ok {
			out = append(out, kiloCandidate{key: turn.Key, turn: turn})
		}
	}
	return out, rows.Err()
}

func kiloTurn(id, sessionID, directory string, created int64, modelID, providerID string, subagent bool, tokens kiloTokens) (model.Turn, bool) {
	if modelID == "" {
		modelID = "unknown"
	}
	usage, ok := (model.Usage{
		Input:      tokens.Input,
		Output:     tokens.Output,
		Reasoning:  tokens.Reasoning,
		CacheRead:  tokens.Cache.Read,
		CacheWrite: tokens.Cache.Write,
	}).Sanitize()
	if !ok || usage.IsZero() {
		return model.Turn{}, false
	}
	return model.Turn{
		Key:       identityKey("kilo-message", id),
		SessionID: sessionID,
		Agent:     model.Agent("kilo-code"),
		Timestamp: unixMillis(created),
		Model:     modelID,
		Provider:  providerID,
		Project:   directory,
		Usage:     usage,
		Subagent:  subagent,
	}, true
}
