package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/klauspost/compress/zstd"
	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

const zedMaxThreadBytes = 64 << 20

type Zed struct{ dbPaths []string }

func NewZed() *Zed {
	p := zedDBPath()
	if !fileExists(p) {
		return &Zed{}
	}
	return &Zed{[]string{p}}
}
func (z *Zed) Agent() model.Agent { return model.Agent("zed") }
func (z *Zed) Roots() []string    { return append([]string(nil), z.dbPaths...) }
func (z *Zed) Scan(ctx context.Context, emit func(model.Turn)) error {
	return collectLocalDBs(ctx, z.Roots(), scanZedDB, emit)
}
func zedDBPath() string {
	var b string
	switch runtime.GOOS {
	case "darwin":
		b = filepath.Join(homeDir(), "Library", "Application Support", "Zed")
	case "windows":
		b = os.Getenv("LOCALAPPDATA")
		if b == "" {
			b = filepath.Join(homeDir(), "AppData", "Local")
		}
		b = filepath.Join(b, "Zed")
	default:
		b = os.Getenv("XDG_DATA_HOME")
		if b == "" {
			b = filepath.Join(homeDir(), ".local", "share")
		}
		b = filepath.Join(b, "zed")
	}
	return filepath.Join(b, "threads", "threads.db")
}

type zedUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
}

func (z zedUsage) usage() model.Usage {
	return model.Usage{Input: z.Input, Output: z.Output, CacheWrite: z.CacheWrite, CacheRead: z.CacheRead}
}

type zedThread struct {
	Cumulative zedUsage            `json:"cumulative_token_usage"`
	Requests   map[string]zedUsage `json:"request_token_usage"`
	Model      *struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	} `json:"model"`
}

func scanZedDB(ctx context.Context, path string) ([]model.Turn, error) {
	db, e := sql.Open("sqlite", localSQLiteDSN(path))
	if e != nil {
		return nil, e
	}
	defer db.Close()
	if e = requireLocalColumns(ctx, db, "threads", "id", "parent_id", "updated_at", "data_type", "data"); e != nil {
		return nil, e
	}
	rows, e := db.QueryContext(ctx, `SELECT id,parent_id,updated_at,data_type,data FROM threads`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var turns []model.Turn
	var scanErr error
	decoder, e := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(zedMaxThreadBytes), zstd.WithDecoderMaxWindow(zedMaxThreadBytes))
	if e != nil {
		return nil, e
	}
	defer decoder.Close()
	for rows.Next() {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		var id, stamp, typ string
		var parent sql.NullString
		var data []byte
		if e := rows.Scan(&id, &parent, &stamp, &typ, &data); e != nil {
			return nil, e
		}
		decoded, e := decodeZedThread(ctx, decoder, typ, data)
		if e != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("zed: unreadable thread data: %w", e))
			continue
		}
		var t zedThread
		if json.Unmarshal(decoded, &t) != nil {
			scanErr = errors.Join(scanErr, errors.New("zed: malformed thread JSON excluded"))
			continue
		}
		modelID, provider := "unknown", ""
		if t.Model != nil {
			if t.Model.Model != "" {
				modelID = t.Model.Model
			}
			provider = t.Model.Provider
		}
		keys := make([]string, 0, len(t.Requests))
		for k := range t.Requests {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sum := model.Usage{}
		valid := true
		for _, k := range keys {
			u, ok := t.Requests[k].usage().SanitizeAggregate()
			if !ok {
				valid = false
				break
			}
			sum.Add(u)
		}
		cum, ok := t.Cumulative.usage().SanitizeAggregate()
		if !ok || sum.Input > cum.Input || sum.Output > cum.Output || sum.CacheRead > cum.CacheRead || sum.CacheWrite > cum.CacheWrite {
			valid = false
		}
		if !valid {
			scanErr = errors.Join(scanErr, errors.New("zed: request usage does not reconcile with cumulative totals; thread excluded"))
			continue
		}
		for _, k := range keys {
			u, _ := t.Requests[k].usage().SanitizeAggregate()
			if u.IsZero() {
				continue
			}
			turns = append(turns, model.Turn{Key: identityKey("zed-request", id, k), SessionID: id, Agent: model.Agent("zed"), Timestamp: localTimestamp(stamp), Model: modelID, Provider: provider, Usage: u, Subagent: parent.Valid && parent.String != "", Aggregate: true, UnpricedReason: "Zed persists the thread's current model, not each request's historical model"})
		}
		rem := model.Usage{Input: cum.Input - sum.Input, Output: cum.Output - sum.Output, CacheRead: cum.CacheRead - sum.CacheRead, CacheWrite: cum.CacheWrite - sum.CacheWrite}
		if !rem.IsZero() {
			turns = append(turns, model.Turn{Key: identityKey("zed-cumulative", id), SessionID: id, Agent: model.Agent("zed"), Timestamp: localTimestamp(stamp), Model: modelID, Provider: provider, Usage: rem, Subagent: parent.Valid && parent.String != "", Aggregate: true, UnpricedReason: "Zed persists the thread's current model, not each request's historical model"})
		}
	}
	if e := rows.Err(); e != nil {
		return nil, fmt.Errorf("read threads: %w", e)
	}
	return turns, scanErr
}
func decodeZedThread(ctx context.Context, d *zstd.Decoder, typ string, data []byte) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if len(data) > zedMaxThreadBytes {
		return nil, errors.New("thread blob exceeds limit")
	}
	switch typ {
	case "json":
		return data, nil
	case "zstd":
		out, e := d.DecodeAll(data, nil)
		if e != nil {
			return nil, e
		}
		if len(out) > zedMaxThreadBytes {
			return nil, errors.New("decoded thread exceeds limit")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported Zed data type %q", typ)
	}
}
