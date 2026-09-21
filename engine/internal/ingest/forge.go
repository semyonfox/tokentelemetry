package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

type Forge struct{ dbPaths []string }

func NewForge() *Forge {
	base := os.Getenv("FORGE_CONFIG")
	if base == "" {
		h := homeDir()
		legacy := filepath.Join(h, "forge")
		if fi, e := os.Stat(legacy); e == nil && fi.IsDir() {
			base = legacy
		} else {
			base = filepath.Join(h, ".forge")
		}
	}
	p := filepath.Join(base, ".forge.db")
	if !fileExists(p) {
		return &Forge{}
	}
	return &Forge{[]string{p}}
}
func (f *Forge) Agent() model.Agent { return model.Agent("forge") }
func (f *Forge) Roots() []string    { return append([]string(nil), f.dbPaths...) }
func (f *Forge) Scan(ctx context.Context, emit func(model.Turn)) error {
	return collectLocalDBs(ctx, f.Roots(), scanForgeDB, emit)
}

type forgeCount struct {
	Actual *int64 `json:"actual"`
	Approx *int64 `json:"approx"`
}
type forgeText struct {
	Role  string `json:"role"`
	Model string `json:"model"`
}
type forgeContext struct {
	Messages []struct {
		Text    *forgeText `json:"text"`
		Message struct {
			Text *forgeText `json:"text"`
		} `json:"message"`
		Usage *struct {
			Prompt     forgeCount `json:"prompt_tokens"`
			Completion forgeCount `json:"completion_tokens"`
			Total      forgeCount `json:"total_tokens"`
			Cached     forgeCount `json:"cached_tokens"`
		} `json:"usage"`
	} `json:"messages"`
}

func scanForgeDB(ctx context.Context, path string) ([]model.Turn, error) {
	db, e := sql.Open("sqlite", localSQLiteDSN(path))
	if e != nil {
		return nil, e
	}
	defer db.Close()
	if e = requireLocalColumns(ctx, db, "conversations", "conversation_id", "title", "workspace_id", "context", "created_at", "updated_at"); e != nil {
		return nil, e
	}
	rows, e := db.QueryContext(ctx, `SELECT conversation_id,COALESCE(title,''),workspace_id,context,COALESCE(updated_at,created_at,'') FROM conversations`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var turns []model.Turn
	for rows.Next() {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		var id, title, raw, stamp string
		var workspace sql.NullInt64
		if e := rows.Scan(&id, &title, &workspace, &raw, &stamp); e != nil {
			return nil, e
		}
		var c forgeContext
		if json.Unmarshal([]byte(raw), &c) != nil {
			continue
		}
		project := ""
		if workspace.Valid {
			project = "forge-workspace:" + strconv.FormatInt(workspace.Int64, 10)
		}
		for i, m := range c.Messages {
			text := m.Text
			if text == nil {
				text = m.Message.Text
			}
			if text == nil || !strings.EqualFold(text.Role, "assistant") || m.Usage == nil {
				continue
			}
			u := m.Usage
			if u.Prompt.Actual == nil || u.Completion.Actual == nil || u.Cached.Actual == nil || u.Prompt.Approx != nil || u.Completion.Approx != nil || u.Cached.Approx != nil {
				continue
			}
			if *u.Cached.Actual < 0 || *u.Prompt.Actual < *u.Cached.Actual {
				continue
			}
			usage, ok := (model.Usage{Input: *u.Prompt.Actual - *u.Cached.Actual, Output: *u.Completion.Actual, CacheRead: *u.Cached.Actual}).Sanitize()
			if !ok || usage.IsZero() {
				continue
			}
			turns = append(turns, model.Turn{Key: identityKey("forge-message", id, strconv.Itoa(i)), SessionID: id, Agent: model.Agent("forge"), Timestamp: localTimestamp(stamp), Model: text.Model, Project: project, Usage: usage, Aggregate: true})
		}
	}
	if e := rows.Err(); e != nil {
		return nil, fmt.Errorf("read conversations: %w", e)
	}
	return turns, nil
}
