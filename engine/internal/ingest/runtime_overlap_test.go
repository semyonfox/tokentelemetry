package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
	"github.com/semyonfox/tokentelemetry/engine/internal/pricing"
	"github.com/semyonfox/tokentelemetry/engine/internal/report"
)

func TestHermesCodexRuntimeOverlapRequiresPersistedMatchingThread(t *testing.T) {
	for _, tc := range []struct {
		name, binding, task string
		native, directLogs  bool
		included, excluded  int
		filter              report.Filter
	}{
		{name: "linked aggregate", binding: "native", native: true, included: 1, excluded: 1},
		{name: "shared backend without link", native: true, included: 2},
		{name: "different native thread", binding: "other", native: true, included: 2},
		{name: "auxiliary direct usage", binding: "native", task: "review", native: true, included: 2},
		{name: "wrapper selected alone", binding: "native", included: 1},
		{name: "native outside report date range", binding: "native", native: true, included: 1, filter: report.Filter{From: "2026-09-02", To: "2026-09-02"}},
		{name: "wrapper filter after combined scan", binding: "native", native: true, included: 1, filter: report.Filter{Agents: []string{"hermes"}}},
		{name: "normal loop logs are independent", binding: "native", native: true, directLogs: true, included: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			hermesRoot, codexRoot := filepath.Join(root, "hermes"), filepath.Join(root, "codex")
			db := hermesFixture(t, hermesRoot, true)
			addHermesRow(t, db, model.Usage{Input: 100, Output: 10})
			if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN model_config TEXT`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE sessions SET model_config=json_object('codex_thread_id', ?); UPDATE session_model_usage SET billing_provider='openai-codex'`, tc.binding); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE session_model_usage SET task=?`, tc.task); err != nil {
				t.Fatal(err)
			}
			if tc.directLogs {
				writeHermesLogs(t, hermesRoot, `2026-09-02 00:02:00,000 INFO [s] agent.conversation_loop: API call #1: model=example provider=openai-codex in=100 out=10 total=110 latency=0.2s id=independent`)
			}
			scanners := []Scanner{&Hermes{root: hermesRoot}}
			if tc.native {
				stamp := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local).Format(time.RFC3339)
				writeFile(t, filepath.Join(codexRoot, "sessions", "rollout-native.jsonl"), codexMeta("native", "", "user", "example"), codexContextWithID("turn", "example"), structuredLine("native", "turn", "response", stamp, codexUsage{Input: 100, Output: 10}))
				scanners = append(scanners, &Codex{root: codexRoot})
			}
			res, err := Run(context.Background(), scanners)
			if err != nil || len(res.Errors) > 0 {
				t.Fatalf("scan: %v %v", err, res.Errors)
			}
			rep := report.Build(res.Turns, &pricing.Table{}, tc.filter, report.Daily, 0, nil)
			if rep.Totals.Turns != tc.included || len(rep.ExcludedOverlaps) != tc.excluded || rep.Totals.Usage.Total() != int64(tc.included)*110 {
				t.Fatalf("overlap accounting: totals=%+v excluded=%+v", rep.Totals, rep.ExcludedOverlaps)
			}
			if tc.excluded > 0 && (rep.ExcludedOverlaps[0].Usage.Total() != 110 || rep.ExcludedOverlaps[0].RuntimeSessionID != "native") {
				t.Fatal("excluded evidence lost")
			}
		})
	}
}

func TestSharedModelDoesNotIdentifyARequest(t *testing.T) {
	turns := []model.Turn{
		{Agent: model.AgentCodex, SessionID: "codex", Model: "gpt-example", Usage: model.Usage{Input: 100}},
		{Agent: model.AgentOpenCode, SessionID: "opencode", Model: "gpt-example", Usage: model.Usage{Input: 100}, Aggregate: true},
		{Agent: model.AgentHermes, SessionID: "hermes", Model: "gpt-example", Usage: model.Usage{Input: 100}, Aggregate: true},
	}
	markRuntimeOverlaps(turns)
	for _, turn := range turns {
		if turn.ExcludedReason != "" {
			t.Fatalf("independent call suppressed: %+v", turn)
		}
	}
}

func TestPiAndOMPReadOnePhysicalLedgerOnce(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "project", "s.jsonl"),
		`{"type":"session","id":"s","version":3,"cwd":"/fixture"}`,
		`{"type":"message","id":"m","timestamp":"2026-09-01T10:00:00Z","message":{"role":"assistant","model":"example","provider":"openai","usage":{"input":100,"output":10}}}`)
	res, err := Run(context.Background(), []Scanner{&Pi{root: root}, &OMP{root: root}})
	if err != nil || len(res.Errors) != 1 || len(res.Turns) != 1 || res.Turns[0].Agent != "omp" || sumUsage(res.Turns).Total() != 110 {
		t.Fatalf("same ledger duplicated: %+v %v", res, err)
	}
	other := t.TempDir()
	writeFile(t, filepath.Join(other, "project", "s.jsonl"),
		`{"type":"session","id":"other","version":3,"cwd":"/fixture"}`,
		`{"type":"message","id":"other-call","timestamp":"2026-09-01T10:00:00Z","message":{"role":"assistant","model":"example","provider":"openai","usage":{"input":100,"output":10}}}`)
	res, err = Run(context.Background(), []Scanner{&Pi{root: root}, &OMP{root: other}})
	if err != nil || len(res.Errors) != 0 || len(res.Turns) != 2 || sumUsage(res.Turns).Total() != 220 {
		t.Fatalf("independent ledgers collapsed: %+v %v", res, err)
	}
}
