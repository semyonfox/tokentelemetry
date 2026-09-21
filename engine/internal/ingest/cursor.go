package ingest

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Cursor discovers the IDE database automatically. A configured CSV replaces
// local history because the two sources lack shared request identities.
type Cursor struct{ path, dbPath string }

func NewCursor() *Cursor {
	if p := os.Getenv("TT_CURSOR_CSV"); p != "" {
		return &Cursor{path: p}
	}
	p := os.Getenv("TT_CURSOR_DB")
	if p == "" {
		if config, err := os.UserConfigDir(); err == nil {
			p = filepath.Join(config, "Cursor", "User", "globalStorage", "state.vscdb")
		}
	}
	if fileExists(p) {
		return &Cursor{dbPath: p}
	}
	return &Cursor{}
}
func newCursorAt(path string) *Cursor { return &Cursor{path: path} }
func (c *Cursor) Agent() model.Agent  { return model.Agent("cursor") }
func (c *Cursor) Roots() []string {
	if c.dbPath != "" {
		return []string{c.dbPath}
	}
	if c.path == "" {
		return nil
	}
	return []string{c.path}
}

func (c *Cursor) Scan(ctx context.Context, emit func(model.Turn)) error {
	if c.dbPath != "" {
		return scanCursorDB(ctx, c.dbPath, emit)
	}
	if c.path == "" {
		return nil
	}
	f, err := os.Open(c.path)
	if err != nil {
		return err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("cursor csv: %w", err)
	}
	cols := map[string]int{}
	for i, h := range header {
		cols[strings.ToLower(strings.TrimSpace(h))] = i
	}
	required := []string{"date", "model", "input (w/ cache write)", "input (w/o cache write)", "cache read", "output tokens", "total tokens"}
	for _, h := range required {
		if _, ok := cols[h]; !ok {
			return fmt.Errorf("cursor csv: missing column %q", h)
		}
	}
	rowNum := 1
	var scanErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		rowNum++
		if err != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("cursor csv row %d: %w", rowNum, err))
			continue
		}
		get := func(name string) string {
			i, ok := cols[name]
			if !ok || i >= len(row) {
				return ""
			}
			return strings.TrimSpace(row[i])
		}
		cw, e1 := cursorInt(get("input (w/ cache write)"))
		in, e2 := cursorInt(get("input (w/o cache write)"))
		cr, e3 := cursorInt(get("cache read"))
		out, e4 := cursorInt(get("output tokens"))
		total, e5 := cursorInt(get("total tokens"))
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("cursor csv row %d: invalid token count", rowNum))
			continue
		}
		bucketed, bucketsOK := (model.Usage{Input: in, Output: out, CacheRead: cr, CacheWrite: cw}).SanitizeAggregate()
		unclassified, totalOK := (model.Usage{Unclassified: total}).SanitizeAggregate()
		if !bucketsOK || !totalOK {
			scanErr = errors.Join(scanErr, fmt.Errorf("cursor csv row %d: implausible token count", rowNum))
			continue
		}
		u := model.Usage{}
		reason := ""
		if in+cw+cr+out == total {
			u = bucketed
			u.ContextTokens = in + cw + cr
		} else {
			u = unclassified
			reason = "Cursor CSV token columns do not reconcile with Total Tokens"
		}
		if u.IsZero() {
			continue
		}
		ts := parseTime(get("date"))
		if ts.IsZero() {
			scanErr = errors.Join(scanErr, fmt.Errorf("cursor csv row %d: invalid date", rowNum))
			continue
		}
		modelID := get("model")
		if modelID == "" {
			modelID = "unknown"
			if reason != "" {
				reason += "; "
			}
			reason += "model is absent"
		}
		session := get("cloud agent id")
		if session == "" {
			session = get("automation id")
		}
		if session == "" {
			session = "cursor-csv"
		}
		emit(model.Turn{Key: identityKey("cursor-csv", filepath.Clean(c.path), strconv.Itoa(rowNum)), SessionID: session, Agent: model.Agent("cursor"), Timestamp: ts, Model: modelID, Provider: "cursor", Usage: u, Aggregate: false, UnpricedReason: reason})
	}
	return scanErr
}

func cursorInt(raw string) (int64, error) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), ",", "")
	if raw == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, errors.New("invalid")
	}
	return v, nil
}
