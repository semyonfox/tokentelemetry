package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

type Goose struct{ dbPaths []string }

func NewGoose() *Goose {
	p := gooseDBPath()
	if !fileExists(p) {
		return &Goose{}
	}
	return &Goose{[]string{p}}
}
func (g *Goose) Agent() model.Agent { return model.Agent("goose") }
func (g *Goose) Roots() []string    { return append([]string(nil), g.dbPaths...) }
func (g *Goose) Scan(ctx context.Context, emit func(model.Turn)) error {
	return collectLocalDBs(ctx, g.Roots(), scanGooseDB, emit)
}

func gooseDBPath() string {
	if root := os.Getenv("GOOSE_PATH_ROOT"); filepath.IsAbs(root) {
		return filepath.Join(root, "data", "sessions", "sessions.db")
	}
	var base string
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(homeDir(), "Library", "Application Support", "Block", "goose")
	case "windows":
		base = os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(homeDir(), "AppData", "Roaming")
		}
		base = filepath.Join(base, "Block", "goose")
	default:
		base = os.Getenv("XDG_DATA_HOME")
		if base == "" {
			base = filepath.Join(homeDir(), ".local", "share")
		}
		base = filepath.Join(base, "goose")
	}
	return filepath.Join(base, "sessions", "sessions.db")
}
func scanGooseDB(ctx context.Context, path string) ([]model.Turn, error) {
	db, e := sql.Open("sqlite", localSQLiteDSN(path))
	if e != nil {
		return nil, e
	}
	defer db.Close()
	if e = requireLocalColumns(ctx, db, "sessions", "id", "working_dir", "created_at", "updated_at", "accumulated_input_tokens", "accumulated_output_tokens", "accumulated_cache_read_tokens", "accumulated_cache_write_tokens", "provider_name", "model_config_json", "parent_session_id"); e != nil {
		return nil, e
	}
	rows, e := db.QueryContext(ctx, `SELECT id,COALESCE(working_dir,''),COALESCE(updated_at,created_at,''),COALESCE(accumulated_input_tokens,0),COALESCE(accumulated_output_tokens,0),accumulated_cache_read_tokens,accumulated_cache_write_tokens,COALESCE(provider_name,''),COALESCE(model_config_json,''),COALESCE(parent_session_id,'') FROM sessions`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var turns []model.Turn
	for rows.Next() {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		var id, project, stamp, provider, raw, parent string
		var in, out int64
		var cr, cw sql.NullInt64
		if e := rows.Scan(&id, &project, &stamp, &in, &out, &cr, &cw, &provider, &raw, &parent); e != nil {
			return nil, e
		}
		modelID := "unknown"
		var cfg struct {
			ModelName string `json:"model_name"`
		}
		if json.Unmarshal([]byte(raw), &cfg) == nil && cfg.ModelName != "" {
			modelID = cfg.ModelName
		}
		u := model.Usage{Output: out}
		reason := "Goose session totals lack historical per-call model attribution"
		if !cr.Valid || !cw.Valid {
			u.Unclassified = in
			reason = "Goose did not record the input/cache split"
		} else if cr.Int64 < 0 || cw.Int64 < 0 || in < cr.Int64+cw.Int64 {
			continue
		} else {
			u.Input = in - cr.Int64 - cw.Int64
			u.CacheRead = cr.Int64
			u.CacheWrite = cw.Int64
		}
		u, ok := u.SanitizeAggregate()
		if !ok || u.IsZero() {
			continue
		}
		turns = append(turns, model.Turn{Key: identityKey("goose-session", id), SessionID: id, Agent: model.Agent("goose"), Timestamp: localTimestamp(stamp), Model: modelID, Provider: provider, Project: project, Usage: u, Subagent: parent != "", Aggregate: true, UnpricedReason: reason})
	}
	if e := rows.Err(); e != nil {
		return nil, fmt.Errorf("read sessions: %w", e)
	}
	return turns, nil
}
