package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func scanCursorDB(ctx context.Context, path string, emit func(model.Turn)) error {
	db, err := sql.Open("sqlite", localSQLiteDSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := requireLocalColumns(ctx, db, "cursorDiskKV", "key", "value"); err != nil {
		return err
	}
	// Project only accounting fields. ItemTable can contain credentials; message
	// text and context-window gauges are neither needed nor valid usage sources.
	rows, err := db.QueryContext(ctx, `SELECT key,
		CASE WHEN json_valid(value) THEN json_object(
			'type', json_extract(value, '$.type'),
			'input', json_extract(value, '$.tokenCount.inputTokens'),
			'output', json_extract(value, '$.tokenCount.outputTokens'),
			'model', json_extract(value, '$.modelInfo.modelName'),
			'time', json_extract(value, '$.createdAt')) ELSE '{}' END
		FROM cursorDiskKV WHERE key LIKE 'bubbleId:%' ORDER BY rowid`)
	if err != nil {
		return err
	}
	defer rows.Close()
	missing, invalid := 0, 0
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return err
		}
		var bubble struct {
			Type   int    `json:"type"`
			Input  *int64 `json:"input"`
			Output *int64 `json:"output"`
			Model  string `json:"model"`
			Time   string `json:"time"`
		}
		if err := json.Unmarshal([]byte(raw), &bubble); err != nil || bubble.Type == 0 {
			invalid++
			continue
		}
		if bubble.Type != 2 {
			continue
		}
		if bubble.Input == nil || bubble.Output == nil || (*bubble.Input == 0 && *bubble.Output == 0) {
			missing++
			continue
		}
		if *bubble.Input < 0 || *bubble.Output < 0 {
			invalid++
			continue
		}
		// The local contract has no cache split. Preserve measured input as
		// unclassified instead of charging it all at the fresh-input rate.
		u, valid := (model.Usage{Unclassified: *bubble.Input, Output: *bubble.Output}).SanitizeAggregate()
		if !valid {
			invalid++
			continue
		}
		parts := strings.SplitN(key, ":", 3)
		if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
			invalid++
			continue
		}
		id := bubble.Model
		if id == "" {
			id = "unknown"
		}
		ts := parseTime(bubble.Time)
		reason := "Cursor local token counters are best-effort and omit the input/cache breakdown"
		if ts.IsZero() {
			reason += "; record has no usable timestamp"
		}
		emit(model.Turn{Key: identityKey("cursor-bubble", key), Agent: model.Agent("cursor"),
			SessionID: parts[1], Timestamp: ts, Model: id, Provider: "cursor", Usage: u,
			Aggregate: true, UnpricedReason: reason})
	}
	var warnings error
	if missing > 0 {
		warnings = fmt.Errorf("Cursor local history has %d assistant records without token counters; usage is incomplete, not zero", missing)
	}
	if invalid > 0 {
		warnings = errors.Join(warnings, fmt.Errorf("Cursor local history has %d malformed accounting records; excluded", invalid))
	}
	return errors.Join(rows.Err(), warnings)
}
