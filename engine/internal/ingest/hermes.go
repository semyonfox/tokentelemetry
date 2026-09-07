package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: keeps CGO_ENABLED=0 cross-compiles working

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
)

// Hermes reads per-route/task aggregates and replaces them with timestamped
// log calls only when their complete usage reconciles. Aggregates remain
// explicitly approximate when logs are missing, rotated or ambiguous.
type Hermes struct {
	root string
}

func NewHermes() *Hermes {
	return &Hermes{root: envDir("HERMES_HOME", ".hermes")}
}

func (h *Hermes) Agent() model.Agent { return model.AgentHermes }

// Roots lists every state.db: the default profile plus any named profiles.
func (h *Hermes) Roots() []string {
	if h.root == "" {
		return nil
	}
	var out []string
	if main := filepath.Join(h.root, "state.db"); fileExists(main) {
		out = append(out, main)
	}
	profiles, err := os.ReadDir(filepath.Join(h.root, "profiles"))
	if err == nil {
		for _, p := range profiles {
			if !p.IsDir() {
				continue
			}
			if db := filepath.Join(h.root, "profiles", p.Name(), "state.db"); fileExists(db) {
				out = append(out, db)
			}
		}
	}
	return out
}

func (h *Hermes) Scan(ctx context.Context, emit func(model.Turn)) error {
	var scanErr error
	for _, db := range h.Roots() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// One unreadable or mid-write database must not lose the others.
		turns, err := h.scanDB(ctx, db)
		if err != nil {
			scanErr = errors.Join(scanErr, err)
			continue
		}
		for _, t := range turns {
			emit(t)
		}
	}
	return scanErr
}

// hermesQuery reads per-model usage joined to its session for the working
// directory. Every numeric column is CAST because Hermes stores them as TEXT in
// places — SQLite's dynamic typing means a bare scan into int64 fails on rows
// written by an older build.
const hermesQuery = `
SELECT
  u.session_id,
  COALESCE(u.model, s.model, '')                        AS model,
  COALESCE(u.billing_provider, s.billing_provider, '')  AS provider,
  COALESCE(u.billing_base_url, s.billing_base_url, '')  AS endpoint,
  CAST(COALESCE(u.input_tokens, 0) AS INTEGER)          AS input_tokens,
  CAST(COALESCE(u.output_tokens, 0) AS INTEGER)         AS output_tokens,
  CAST(COALESCE(u.cache_read_tokens, 0) AS INTEGER)     AS cache_read,
  CAST(COALESCE(u.cache_write_tokens, 0) AS INTEGER)    AS cache_write,
  CAST(COALESCE(u.reasoning_tokens, 0) AS INTEGER)      AS reasoning,
  CAST(COALESCE(u.last_seen, u.first_seen, 0) AS REAL)  AS seen_at,
  COALESCE(s.cwd, '')                                   AS cwd,
  COALESCE(s.parent_session_id, '')                     AS parent_id,
  %s AS billing_mode,
  %s AS task,
  %s AS api_call_count
FROM session_model_usage u
LEFT JOIN sessions s ON s.id = u.session_id
`

func (h *Hermes) scanDB(ctx context.Context, path string) ([]model.Turn, error) {
	// Read-only, and immutable=false so a live WAL is still read correctly.
	// A busy timeout keeps a concurrently-writing Hermes from failing the scan.
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&_pragma=busy_timeout(3000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	cols, err := hermesColumns(ctx, db)
	if err != nil {
		return nil, err
	}
	optional := func(name, fallback string) string {
		if cols[name] {
			return "COALESCE(u." + name + ", " + fallback + ")"
		}
		return fallback
	}
	query := fmt.Sprintf(hermesQuery, optional("billing_mode", "''"), optional("task", "''"), optional("api_call_count", "0"))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scope := sha256.Sum256([]byte(filepath.Clean(path)))
	var aggregates []hermesAggregate
	for rows.Next() {
		var (
			sessionID, modelID, provider, endpoint, cwd, parentID, billingMode, task string
			in, out, cr, cw, reasoning, calls                                        int64
			seenAt                                                                   float64
		)
		if err := rows.Scan(&sessionID, &modelID, &provider, &endpoint,
			&in, &out, &cr, &cw, &reasoning, &seenAt, &cwd, &parentID, &billingMode, &task, &calls); err != nil {
			return nil, err
		}
		usage := model.Usage{
			Input: in, Output: out, CacheRead: cr, CacheWrite: cw, Reasoning: reasoning,
		}
		usage, ok := sanitizeHermesAggregate(usage)
		if !ok || usage.IsZero() {
			continue
		}
		aggregates = append(aggregates, hermesAggregate{session: sessionID, task: task, calls: calls, turn: model.Turn{
			Key:       identityKey("hermes-aggregate", path, sessionID, modelID, provider, endpoint, billingMode, task),
			SessionID: fmt.Sprintf("%s@%x", sessionID, scope),
			Agent:     model.AgentHermes,
			Timestamp: unixFloat(seenAt),
			Model:     modelID,
			Provider:  provider,
			Endpoint:  endpoint,
			Project:   cwd,
			Usage:     usage,
			Subagent:  parentID != "",
			Aggregate: true,
		}})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return reconcileHermesLogs(ctx, path, aggregates)
}

// unixFloat converts Hermes's fractional unix seconds to a time.
func unixFloat(v float64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	sec := int64(v)
	return time.Unix(sec, int64((v-float64(sec))*1e9))
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// Optional columns support older five-part keys and pre-call-count schemas.
func hermesColumns(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(session_model_usage)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			return nil, err
		}
		cols[strings.ToLower(name)] = true
	}
	return cols, rows.Err()
}

// Session aggregates can legitimately exceed any per-call token limit. Keep
// negative values clamped as for other readers, but reject arithmetic overflow.
func sanitizeHermesAggregate(u model.Usage) (model.Usage, bool) {
	for _, p := range []*int64{&u.Input, &u.Output, &u.CacheRead, &u.CacheWrite, &u.Reasoning} {
		if *p < 0 {
			*p = 0
		}
		if *p > math.MaxInt64/8 {
			return u, false
		}
	}
	return u, true
}
