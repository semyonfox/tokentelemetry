package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
	"github.com/VasiHemanth/tokentelemetry/engine/internal/pricing"
	"github.com/VasiHemanth/tokentelemetry/engine/internal/report"
)

func hermesFixture(t *testing.T, root string, modern bool) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	extra := ""
	if modern {
		extra = ", billing_mode TEXT DEFAULT '', task TEXT DEFAULT '', api_call_count INTEGER DEFAULT 0"
	}
	for _, q := range []string{
		`CREATE TABLE sessions(id TEXT PRIMARY KEY,model TEXT,billing_provider TEXT,billing_base_url TEXT,cwd TEXT,parent_session_id TEXT)`,
		`CREATE TABLE session_model_usage(session_id TEXT,model TEXT,billing_provider TEXT,billing_base_url TEXT,input_tokens INTEGER,output_tokens INTEGER,cache_read_tokens INTEGER,cache_write_tokens INTEGER,reasoning_tokens INTEGER,first_seen REAL,last_seen REAL` + extra + `)`,
		`INSERT INTO sessions VALUES('s','example','p','','/example','')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func addHermesRow(t *testing.T, db *sql.DB, usage model.Usage) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO session_model_usage(session_id,model,billing_provider,billing_base_url,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,first_seen,last_seen) VALUES('s','example','p','',?,?,?,?,?,?,?)`, usage.Input, usage.Output, usage.CacheRead, usage.CacheWrite, usage.Reasoning,
		time.Date(2026, 9, 1, 23, 59, 0, 0, time.Local).Unix(), time.Date(2026, 9, 2, 0, 2, 0, 0, time.Local).Unix())
	if err != nil {
		t.Fatal(err)
	}
}

func hermesRun(t *testing.T, root string) []model.Turn {
	t.Helper()
	res, err := Run(context.Background(), []Scanner{&Hermes{root: root}})
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("scan: %v %v", err, res.Errors)
	}
	return res.Turns
}

func sumUsage(turns []model.Turn) model.Usage {
	var u model.Usage
	for _, t := range turns {
		u.Add(t.Usage)
	}
	return u
}

func TestHermesDistinctTaskRouteRowsAndLargeAggregates(t *testing.T) {
	root := t.TempDir()
	db := hermesFixture(t, root, true)
	for i := range 4 {
		addHermesRow(t, db, model.Usage{Input: int64(i+1) * 100})
	}
	for _, q := range []string{
		`UPDATE session_model_usage SET task='aux' WHERE rowid=2`,
		`UPDATE session_model_usage SET billing_base_url='https://example.invalid' WHERE rowid=3`,
		`UPDATE session_model_usage SET billing_mode='subscription' WHERE rowid=4`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	turns := hermesRun(t, root)
	if len(turns) != 4 || sumUsage(turns).Input != 1000 {
		t.Fatalf("distinct DB rows collapsed: %+v", turns)
	}
	if _, err := db.Exec(`UPDATE session_model_usage SET input_tokens=60000000 WHERE rowid=1`); err != nil {
		t.Fatal(err)
	}
	turns = hermesRun(t, root)
	if got := sumUsage(turns).Input; got != 60_000_900 {
		t.Fatalf("session aggregate rejected: %d", got)
	}
	for _, turn := range turns {
		if !turn.Aggregate {
			t.Fatal("aggregate lacks qualification")
		}
	}
}

func TestHermesOldSchemaAndProfileSessionIdentity(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{root, filepath.Join(root, "profiles", "other")} {
		db := hermesFixture(t, dir, false)
		addHermesRow(t, db, model.Usage{Input: 100})
	}
	turns := hermesRun(t, root)
	if len(turns) != 2 || turns[0].SessionID == turns[1].SessionID {
		t.Fatalf("profile sessions collapsed: %+v", turns)
	}
}

func hermesLine(day, timePart string, n int, in, out, read, write int64, id string) string {
	return fmt.Sprintf("2026-09-%s %s,000 INFO [s] agent.conversation_loop: API call #%d: model=example provider=p in=%d out=%d total=%d latency=0.2s cache=%d/%d (50%%) write=%d id=%s", day, timePart, n, in, out, in+out, read, in, write, id)
}

func writeHermesLogs(t *testing.T, root string, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "logs", "agent.log"), lines...)
}

func TestHermesCompleteLogsSplitMidnightAndDedupRotation(t *testing.T) {
	root := t.TempDir()
	db := hermesFixture(t, root, true)
	// Gross inputs 1000 + 2000, minus cache reads 400+800 and writes
	// 100+200: 1500 fresh, 1200 read, 300 write, 30 output = 3030.
	addHermesRow(t, db, model.Usage{Input: 1500, Output: 30, CacheRead: 1200, CacheWrite: 300})
	if _, err := db.Exec(`UPDATE session_model_usage SET api_call_count=2`); err != nil {
		t.Fatal(err)
	}
	a := hermesLine("01", "23:59:30", 1, 1000, 10, 400, 100, "r1")
	b := hermesLine("02", "00:01:00", 2, 2000, 20, 800, 200, "r2")
	writeHermesLogs(t, root, a, b)
	writeFile(t, filepath.Join(root, "logs", "agent.log.1"), a)
	turns := hermesRun(t, root)
	if len(turns) != 2 || sumUsage(turns).Total() != 3030 {
		t.Fatalf("reconciliation: %+v", turns)
	}
	if turns[0].Aggregate || turns[1].Aggregate {
		t.Fatal("complete logs were not used")
	}
	if turns[0].Timestamp.Local().Day() != 1 || turns[1].Timestamp.Local().Day() != 2 {
		t.Fatalf("midnight attribution: %+v", turns)
	}
	if turns[0].Usage.Input != 500 || turns[1].Usage.Input != 1000 {
		t.Fatal("cache double counted")
	}
	if turns[0].Usage.ContextTokens != 1000 {
		t.Fatal("per-call context lost")
	}
}

func TestHermesPartialConflictingAndAmbiguousLogsKeepDatabase(t *testing.T) {
	for _, scenario := range []string{"partial", "extra", "conflict", "task", "old-write", "call-count"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			db := hermesFixture(t, root, true)
			addHermesRow(t, db, model.Usage{Input: 1500, Output: 30, CacheRead: 1200, CacheWrite: 300})
			a := hermesLine("01", "23:59:30", 1, 1000, 10, 400, 100, "r1")
			b := hermesLine("02", "00:01:00", 2, 2000, 20, 800, 200, "r2")
			lines := []string{a, b}
			want := int64(3030)
			switch scenario {
			case "partial":
				lines = lines[:1]
			case "extra":
				lines = append(lines, hermesLine("02", "00:02:00", 3, 1000, 10, 400, 100, "r3"))
			case "conflict":
				lines = append(lines, hermesLine("01", "23:59:30", 1, 1200, 10, 400, 100, "r1"))
			case "task":
				addHermesRow(t, db, model.Usage{Input: 100})
				if _, err := db.Exec(`UPDATE session_model_usage SET task='aux' WHERE rowid=2`); err != nil {
					t.Fatal(err)
				}
				want += 100
			case "old-write":
				lines = []string{"2026-09-01 23:59:30,000 INFO [s] agent.conversation_loop: API call #1: model=example provider=p in=3000 out=30 total=3030 latency=0.2s cache=1200/3000 (40%)"}
			case "call-count":
				if _, err := db.Exec(`UPDATE session_model_usage SET api_call_count=3`); err != nil {
					t.Fatal(err)
				}
			}
			writeHermesLogs(t, root, lines...)
			turns := hermesRun(t, root)
			if sumUsage(turns).Total() != want {
				t.Fatalf("totals changed: %+v", turns)
			}
			for _, turn := range turns {
				if !turn.Aggregate {
					t.Fatal("ambiguous data marked precise")
				}
			}
		})
	}
}

func TestHermesReasoningMetadataIsNotLostOrDoubleCharged(t *testing.T) {
	root := t.TempDir()
	db := hermesFixture(t, root, false)
	addHermesRow(t, db, model.Usage{Input: 500, Output: 10, CacheRead: 400, CacheWrite: 100, Reasoning: 7})
	writeHermesLogs(t, root, hermesLine("01", "23:59:30", 1, 1000, 10, 400, 100, "r1"))
	turns := hermesRun(t, root)
	u := sumUsage(turns)
	if u.Total() != 1010 || u.Reasoning != 7 || len(turns) != 2 {
		t.Fatalf("reasoning metadata: %+v", turns)
	}
}

func TestParseHermesCallRejectsCorruptCounts(t *testing.T) {
	for _, line := range []string{
		hermesLine("01", "23:59:30", 1, 100, 10, 99, 99, "r1"),
		hermesLine("01", "23:59:30", 1, 100_000_000, 10, 0, 0, "r1"),
		hermesLine("99", "23:59:30", 1, 100, 10, 0, 0, "r1"),
	} {
		if _, ok := parseHermesCall(line); ok {
			t.Fatalf("accepted corrupt line: %s", line)
		}
	}
}

func TestHermesDailyCostsReconcileIndependently(t *testing.T) {
	root := t.TempDir()
	db := hermesFixture(t, root, true)
	addHermesRow(t, db, model.Usage{Input: 1500, Output: 30, CacheRead: 1200, CacheWrite: 300})
	writeHermesLogs(t, root, hermesLine("01", "23:59:30", 1, 1000, 10, 400, 100, "r1"), hermesLine("02", "00:01:00", 2, 2000, 20, 800, 200, "r2"))
	table := &pricing.Table{Models: map[string]*pricing.Model{"example": {ID: "example", Rates: []pricing.Rate{
		{From: pricing.MustParseDate("2026-01-01"), In: 2, Out: 4, CacheRead: 1, CacheWrite: 3},
		{From: pricing.MustParseDate("2026-09-02"), In: 4, Out: 8, CacheRead: 2, CacheWrite: 6},
	}}}}
	rep := report.Build(hermesRun(t, root), table, report.Filter{}, report.Daily, 0, nil)
	// Independently: day 1=(500*2+10*4+400*1+100*3)/1M=$0.00174.
	// Day 2=(1000*4+20*8+800*2+200*6)/1M=$0.00696; total=$0.00870.
	if len(rep.Series) != 2 || rep.Totals.Usage.Total() != 3030 || rep.Totals.AggregateRecords != 0 {
		t.Fatalf("bad report: %+v", rep)
	}
	for i, want := range []float64{0.00174, 0.00696} {
		if math.Abs(rep.Series[i].Cost-want) > 1e-12 {
			t.Fatalf("day %d cost %.8f, want %.8f", i, rep.Series[i].Cost, want)
		}
	}
	if math.Abs(rep.Totals.Cost-0.00870) > 1e-12 {
		t.Fatal(rep.Totals.Cost)
	}
}
