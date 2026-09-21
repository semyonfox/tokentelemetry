package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

const cursorLocalSchema = `CREATE TABLE cursorDiskKV(key TEXT PRIMARY KEY,value BLOB);`

func TestCursorLocalReadsMeasuredCountersWithoutEstimatingGaps(t *testing.T) {
	p := fixtureDB(t, cursorLocalSchema,
		`INSERT INTO cursorDiskKV VALUES
		('bubbleId:s:measured', '{"type":2,"createdAt":"2026-09-01T12:00:00Z","modelInfo":{"modelName":"recorded-model"},"tokenCount":{"inputTokens":100,"outputTokens":10}}'),
		('bubbleId:s:unknown', '{"type":2,"tokenCount":{"inputTokens":500,"outputTokens":30}}'),
		('bubbleId:s:empty', '{"type":2,"text":"Text must not become tokens","tokenCount":{"inputTokens":0,"outputTokens":0}}'),
		('bubbleId:s:missing', '{"type":2}'),
		('bubbleId:s:user', '{"type":1,"tokenCount":{"inputTokens":900,"outputTokens":0}}'),
		('composerData:s', '{"contextTokensUsed":999999,"usageData":{}}'),
		('agentKv:blob:s', '{"role":"assistant","content":"Text must not become tokens"}'),
		('bubbleId:s:invalid', 'not json'),
		('bubbleId:s:negative', '{"type":2,"tokenCount":{"inputTokens":-1,"outputTokens":10}}'),
		('bubbleId:s:overflow', '{"type":2,"tokenCount":{"inputTokens":9223372036854775807,"outputTokens":10}}')`)
	res, err := Run(context.Background(), []Scanner{&Cursor{dbPath: p}})
	if err != nil || len(res.Turns) != 2 || len(res.Errors) != 1 {
		t.Fatalf("scan: %+v %v", res, err)
	}
	if !strings.Contains(res.Errors[0].Error(), "2 assistant records without token counters") || !strings.Contains(res.Errors[0].Error(), "3 malformed accounting records") {
		t.Fatalf("missing gap disclosure: %v", res.Errors)
	}
	// Only the stored 100+10 and 500+30 are measured. Neither message text,
	// user bubbles nor the 999,999-token context gauge are usage additions.
	u := sumUsage(res.Turns)
	if u.Total() != 640 || u.Unclassified != 600 || u.Output != 40 || u.Input != 0 {
		t.Fatalf("incorrect native accounting: %+v", u)
	}
	for _, turn := range res.Turns {
		if turn.Agent != "cursor" || turn.SessionID != "s" || !turn.Aggregate || turn.UnpricedReason == "" {
			t.Fatalf("incomplete attribution: %+v", turn)
		}
		if turn.Usage.Output == 30 && (turn.Model != "unknown" || !turn.Timestamp.IsZero()) {
			t.Fatalf("invented model/time: %+v", turn)
		}
		if turn.Usage.Output == 10 && (turn.Model != "recorded-model" || turn.Timestamp.IsZero()) {
			t.Fatalf("lost source metadata: %+v", turn)
		}
	}
}

func TestCursorLocalDiscoveryAndCSVReplacement(t *testing.T) {
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA"} {
		t.Setenv(name, t.TempDir())
	}
	t.Setenv("TT_CURSOR_DB", "")
	t.Setenv("TT_CURSOR_CSV", "")
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(config, "Cursor", "User", "globalStorage", "state.vscdb")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(cursorLocalSchema + `INSERT INTO cursorDiskKV VALUES('bubbleId:s:b','{"type":2,"tokenCount":{"inputTokens":100,"outputTokens":10}}')`); err != nil {
		t.Fatal(err)
	}
	got := scan(t, NewCursor())
	if len(got) != 1 || got[0].Usage.Total() != 110 {
		t.Fatalf("automatic discovery: %+v", got)
	}
	csv := filepath.Join(t.TempDir(), "usage.csv")
	writeFile(t, csv, "Date,Model,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens", "2026-09-01T12:00:00Z,example,0,100,0,10,110")
	t.Setenv("TT_CURSOR_CSV", csv)
	got = scan(t, NewCursor())
	if len(got) != 1 || got[0].Usage.Total() != 110 || got[0].Usage.Unclassified != 0 {
		t.Fatalf("CSV must replace native history, not add to it: %+v", got)
	}
	// A mistyped configured export must not silently switch accounting source.
	t.Setenv("TT_CURSOR_CSV", filepath.Join(t.TempDir(), "missing.csv"))
	if err := NewCursor().Scan(context.Background(), func(model.Turn) { t.Error("unexpected fallback") }); err == nil {
		t.Fatal("missing configured export was not reported")
	}
}

func TestCursorLocalSeesWALAndUpdatedCountersOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;` + cursorLocalSchema + `INSERT INTO cursorDiskKV VALUES('bubbleId:s:b','{"type":2,"tokenCount":{"inputTokens":100,"outputTokens":10}}')`); err != nil {
		t.Fatal(err)
	}
	scanner := &Cursor{dbPath: p}
	for _, output := range []int64{10, 20} {
		if _, err := db.Exec(`UPDATE cursorDiskKV SET value=json_set(value,'$.tokenCount.outputTokens',?)`, output); err != nil {
			t.Fatal(err)
		}
		res, err := Run(context.Background(), []Scanner{scanner, scanner})
		if err != nil || len(res.Errors) != 0 || len(res.Turns) != 1 || res.Duplicates != 1 || res.Turns[0].Usage.Total() != 100+output {
			t.Fatalf("WAL/update accounting: %+v %v", res, err)
		}
	}
}
