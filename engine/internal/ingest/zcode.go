package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

type ZCode struct{ dbPaths []string }

func NewZCode() *ZCode {
	h := homeDir()
	if h == "" {
		return &ZCode{}
	}
	p := filepath.Join(h, ".zcode", "cli", "db", "db.sqlite")
	if !fileExists(p) {
		return &ZCode{}
	}
	return &ZCode{dbPaths: []string{p}}
}
func (z *ZCode) Agent() model.Agent { return model.Agent("zcode") }
func (z *ZCode) Roots() []string    { return append([]string(nil), z.dbPaths...) }
func (z *ZCode) Scan(ctx context.Context, emit func(model.Turn)) error {
	return collectLocalDBs(ctx, z.Roots(), scanZCodeDB, emit)
}

func scanZCodeDB(ctx context.Context, path string) ([]model.Turn, error) {
	db, err := sql.Open("sqlite", localSQLiteDSN(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := requireLocalColumns(ctx, db, "model_usage", "id", "session_id", "model_id", "input_tokens", "output_tokens", "reasoning_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "started_at", "completed_at"); err != nil {
		return nil, err
	}
	if err := requireLocalColumns(ctx, db, "session", "id", "directory"); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT m.id,m.session_id,m.model_id,m.input_tokens,m.output_tokens,m.reasoning_tokens,m.cache_creation_input_tokens,m.cache_read_input_tokens,m.started_at,m.completed_at,COALESCE(s.directory,'') FROM model_usage m LEFT JOIN session s ON s.id=m.session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var turns []model.Turn
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var id, sid, modelID, project string
		var in, out, reason, cw, cr, started int64
		var completed sql.NullInt64
		if err := rows.Scan(&id, &sid, &modelID, &in, &out, &reason, &cw, &cr, &started, &completed, &project); err != nil {
			return nil, err
		}
		if in < cr+cw || cr < 0 || cw < 0 {
			continue
		}
		u, ok := (model.Usage{Input: in - cr - cw, Output: out, Reasoning: reason, CacheWrite: cw, CacheRead: cr}).Sanitize()
		if !ok || u.IsZero() {
			continue
		}
		ms := started
		if completed.Valid {
			ms = completed.Int64
		}
		turns = append(turns, model.Turn{Key: identityKey("zcode", id), SessionID: sid, Agent: model.Agent("zcode"), Timestamp: time.UnixMilli(ms), Model: modelID, Project: project, Usage: u})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read model_usage: %w", err)
	}
	return turns, nil
}
