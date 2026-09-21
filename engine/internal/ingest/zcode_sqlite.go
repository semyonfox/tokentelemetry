package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func localSQLiteDSN(path string) string {
	p := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: "mode=ro&_pragma=busy_timeout(3000)"}).String()
}

func localColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
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

func requireLocalColumns(ctx context.Context, db *sql.DB, table string, required ...string) error {
	cols, err := localColumns(ctx, db, table)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return fmt.Errorf("unsupported schema: %s table is missing", table)
	}
	var missing []string
	for _, col := range required {
		if !cols[col] {
			missing = append(missing, col)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("unsupported schema: %s is missing columns %s", table, strings.Join(missing, ", "))
	}
	return nil
}

func localTimestamp(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t
		}
	}
	return time.Time{}
}

func collectLocalDBs(ctx context.Context, paths []string, scan func(context.Context, string) ([]model.Turn, error), emit func(model.Turn)) error {
	var allErr error
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		turns, err := scan(ctx, path)
		if err != nil {
			allErr = errors.Join(allErr, fmt.Errorf("%q: %w", path, err))
		}
		for _, turn := range turns {
			emit(turn)
		}
	}
	return allErr
}
