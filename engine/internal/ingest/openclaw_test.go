package ingest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
	"github.com/semyonfox/tokentelemetry/engine/internal/pricing"
	"github.com/semyonfox/tokentelemetry/engine/internal/report"
)

func TestOpenClawDedupsCurrentSQLiteAndJSONLByNativeEntryID(t *testing.T) {
	root := t.TempDir()
	agent := filepath.Join(root, "main")
	dbPath := filepath.Join(agent, "agent", "openclaw-agent.sqlite")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`CREATE TABLE session_windows(session_id TEXT PRIMARY KEY,model_provider TEXT,model TEXT,parent_session_key TEXT,spawned_by TEXT)`, `CREATE TABLE transcript_events(session_id TEXT,seq INTEGER,event_json TEXT,created_at INTEGER)`, `INSERT INTO session_windows VALUES('session','openrouter','fallback-model','','')`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	header := `{"type":"session","id":"session","timestamp":"2026-09-01T00:00:00Z","cwd":"/work/repo"}`
	call := `{"type":"message","id":"entry-1","timestamp":"2026-09-01T00:01:00Z","message":{"role":"assistant","provider":"anthropic","model":"claude-x","usage":{"input":10,"output":2,"cacheRead":3,"cacheWrite":4}}}`
	if _, err := db.Exec(`INSERT INTO transcript_events VALUES('session',1,?,1),('session',2,?,2)`, header, call); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(agent, "sessions", "session.jsonl"), header, call)
	result, err := Run(context.Background(), []Scanner{newOpenClawAt(root)})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan: %v %v", err, result.Errors)
	}
	if len(result.Turns) != 1 || result.Duplicates != 1 {
		t.Fatalf("dedup: %+v", result)
	}
	turn := result.Turns[0]
	if turn.Project != "/work/repo" || turn.Model != "claude-x" || turn.Provider != "anthropic" || turn.Usage.Total() != 19 {
		t.Fatalf("turn: %+v", turn)
	}
}

func TestOpenClawUnknownModelRemainsUnpriced(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent", "sessions", "s.jsonl")
	writeFile(t, path, `{"type":"session","id":"s","cwd":"/p"}`, `{"type":"message","id":"e","timestamp":"2026-09-01T00:00:00Z","message":{"role":"assistant","usage":{"inputTokens":3,"outputTokens":1}}}`)
	turns := scan(t, newOpenClawAt(root))
	if len(turns) != 1 || turns[0].Model != "unknown" || turns[0].UnpricedReason == "" {
		t.Fatalf("turns: %+v", turns)
	}
}

func TestOpenClawCodexRuntimeLinkRequiresActiveBindingAndCodexWindow(t *testing.T) {
	for _, tc := range []struct {
		name, harness, bindingState, bindingSession, wantThread string
	}{
		{name: "exact active link", harness: "codex", bindingState: "active", bindingSession: "session", wantThread: "codex-thread"},
		{name: "embedded window", harness: "embedded", bindingState: "active", bindingSession: "session"},
		{name: "inactive binding", harness: "codex", bindingState: "detached", bindingSession: "session"},
		{name: "different session", harness: "codex", bindingState: "active", bindingSession: "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scanner, _ := openClawCodexFixture(t, tc.harness, tc.bindingState, tc.bindingSession)
			turns := scan(t, scanner)
			if len(turns) != 1 {
				t.Fatalf("turns: %+v", turns)
			}
			turn := turns[0]
			wantAgent := model.Agent("")
			if tc.wantThread != "" {
				wantAgent = model.AgentCodex
			}
			if turn.RuntimeAgent != wantAgent || turn.RuntimeSessionID != tc.wantThread {
				t.Fatalf("runtime link: %+v", turn)
			}
			if turn.Aggregate || turn.ExcludedReason != "" {
				t.Fatalf("reader decided overlap without native evidence: %+v", turn)
			}
		})
	}
}

func TestOpenClawCodexRuntimeOverlapRequiresScannedNativeThread(t *testing.T) {
	for _, tc := range []struct {
		name, nativeThread string
		included, excluded int
	}{
		{name: "matching native thread", nativeThread: "codex-thread", included: 1, excluded: 1},
		{name: "different native thread", nativeThread: "other-thread", included: 2},
		{name: "wrapper alone", included: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scanner, root := openClawCodexFixture(t, "codex", "active", "session")
			scanners := []Scanner{scanner}
			if tc.nativeThread != "" {
				codexRoot := filepath.Join(root, "codex")
				writeFile(t, filepath.Join(codexRoot, "sessions", "rollout-native.jsonl"),
					codexMeta(tc.nativeThread, "", "user", "gpt-example"),
					codexContextWithID("turn", "gpt-example"),
					structuredLine(tc.nativeThread, "turn", "response", "2026-09-01T00:01:00Z", codexUsage{Input: 10, Output: 2}),
				)
				scanners = append(scanners, &Codex{root: codexRoot})
			}
			result, err := Run(context.Background(), scanners)
			if err != nil || len(result.Errors) != 0 {
				t.Fatalf("scan: %v %v", err, result.Errors)
			}
			rep := report.Build(result.Turns, &pricing.Table{}, report.Filter{}, report.Daily, result.Duplicates, nil)
			if rep.Totals.Turns != tc.included || len(rep.ExcludedOverlaps) != tc.excluded || rep.Totals.Usage.Total() != int64(tc.included)*12 {
				t.Fatalf("overlap accounting: totals=%+v excluded=%+v turns=%+v", rep.Totals, rep.ExcludedOverlaps, result.Turns)
			}
			if tc.excluded == 1 && (rep.ExcludedOverlaps[0].Agent != model.Agent("openclaw") || rep.ExcludedOverlaps[0].RuntimeSessionID != "codex-thread") {
				t.Fatalf("excluded evidence: %+v", rep.ExcludedOverlaps)
			}
		})
	}
}

func openClawCodexFixture(t *testing.T, harness, bindingState, bindingSession string) (*OpenClaw, string) {
	t.Helper()
	root := t.TempDir()
	agentsRoot := filepath.Join(root, "agents")
	agentDB := filepath.Join(agentsRoot, "main", "agent", "openclaw-agent.sqlite")
	if err := os.MkdirAll(filepath.Dir(agentDB), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", agentDB)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE session_windows(session_id TEXT PRIMARY KEY,model_provider TEXT,model TEXT,parent_session_key TEXT,spawned_by TEXT,agent_harness_id TEXT)`,
		`CREATE TABLE transcript_events(session_id TEXT,seq INTEGER,event_json TEXT,created_at INTEGER)`,
		`INSERT INTO session_windows VALUES('session','openai','gpt-example','','',?)`,
		`INSERT INTO transcript_events VALUES('session',1,'{"type":"session","id":"session","cwd":"/work"}',1)`,
		`INSERT INTO transcript_events VALUES('session',2,'{"type":"message","id":"entry","timestamp":"2026-09-01T00:01:00Z","message":{"role":"assistant","provider":"openai","model":"gpt-example","usage":{"input":10,"output":2}}}',2)`,
	}
	for i, statement := range statements {
		var execErr error
		if i == 2 {
			_, execErr = db.Exec(statement, harness)
		} else {
			_, execErr = db.Exec(statement)
		}
		if execErr != nil {
			db.Close()
			t.Fatal(execErr)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	stateDB := filepath.Join(root, "state", "openclaw.sqlite")
	if err := os.MkdirAll(filepath.Dir(stateDB), 0o755); err != nil {
		t.Fatal(err)
	}
	state, err := sql.Open("sqlite", stateDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Exec(`CREATE TABLE plugin_state_entries(plugin_id TEXT,namespace TEXT,entry_key TEXT,value_json TEXT,created_at INTEGER,expires_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if bindingState != "" {
		if _, err := state.Exec(`INSERT INTO plugin_state_entries VALUES('codex','app-server-thread-bindings','binding',json_object('sessionId',?,'state',?,'binding',json_object('threadId','codex-thread')),1,NULL)`, bindingSession, bindingState); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	return newOpenClawAtState(stateDB, agentsRoot), root
}
