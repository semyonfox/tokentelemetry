package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestCopilotDiscoversExplicitExporterWithoutNativeStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "config"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("COPILOT_HOME", filepath.Join(home, "copilot"))
	t.Setenv(copilotOTelFileExporterPathEnv, "")
	path := filepath.Join(home, "copilot", "otel.jsonl")
	line := copilotOTelTestSpan("export-only", "span", "gpt-5")
	writeFile(t, path, line, line)
	if roots := NewCopilot().Roots(); len(roots) != 0 {
		t.Fatalf("unconfigured exporter discovered: %v", roots)
	}

	t.Setenv(copilotOTelFileExporterPathEnv, path)
	scanner := NewCopilot()
	if roots := scanner.Roots(); len(roots) != 1 || roots[0] != path {
		t.Fatalf("roots = %v, want configured exporter only", roots)
	}
	result, err := Run(context.Background(), []Scanner{scanner})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan = %#v, error = %v", result, err)
	}
	if len(result.Turns) != 1 || result.Duplicates != 1 || result.Turns[0].SessionID != "export-only" {
		t.Fatalf("repeated export span was not counted once: %#v", result)
	}
}

func TestCopilotNativeConversationWinsOverExport(t *testing.T) {
	for _, source := range []string{"ledger", "shutdown", "vscode"} {
		t.Run(source, func(t *testing.T) {
			cliRoot, vscodeRoot := t.TempDir(), t.TempDir()
			switch source {
			case "ledger":
				db := writeCopilotUsageStore(t, cliRoot)
				defer db.Close()
				insertCopilotUsage(t, db, "shared-session", "requested-model", 100, 7, 0, 0, 0, "", "2026-09-19T10:00:00Z")
			case "shutdown":
				writeFile(t, filepath.Join(cliRoot, "session-state", "shared-session", "events.jsonl"),
					`{"type":"session.shutdown","timestamp":"2026-09-19T10:02:03Z","data":{"modelMetrics":{"requested-model":{"usage":{"inputTokens":100,"outputTokens":7}}}}}`,
				)
			case "vscode":
				writeFile(t, filepath.Join(vscodeRoot, "workspaceStorage", "workspace", "chatSessions", "session.json"),
					copilotVSCodeSession("shared-session", copilotVSCodeRequestForAgent("agent-host-copilot", "shared-session", "request", "response", "requested-model", 1789812000000, 100, 0, 7)),
				)
			}
			path := filepath.Join(t.TempDir(), "otel.jsonl")
			writeFile(t, path,
				copilotOTelTestSpan("shared-session", "overlapping-span", "resolved-model"),
				copilotOTelTestSpan("shared-session", "possibly-uncovered-span", "another-model"),
				copilotOTelTestSpan("export-only", "unique-span", "gpt-5"),
			)
			scanner := newCopilotWithVSCode(cliRoot, []string{vscodeRoot})
			scanner.otelPath = path
			turns := scan(t, scanner)
			if len(turns) != 2 {
				t.Fatalf("turns = %#v, want native session and separate exported session", turns)
			}
			for _, turn := range turns {
				switch turn.SessionID {
				case "shared-session":
					if strings.HasPrefix(turn.Key, "copilot-otel|") || turn.Model != "requested-model" || turn.Usage.Total() != 107 {
						t.Fatalf("native conversation did not win: %#v", turn)
					}
				case "export-only":
					if !strings.HasPrefix(turn.Key, "copilot-otel|") || turn.Usage.Total() != 220 {
						t.Fatalf("uncovered export not retained: %#v", turn)
					}
				default:
					t.Fatalf("unexpected conversation: %#v", turn)
				}
			}
		})
	}
}

func copilotOTelTestSpan(sessionID, spanID, modelID string) string {
	return fmt.Sprintf(`{"type":"span","traceId":"trace","spanId":%q,"endTime":[1789812672,0],"attributes":{"gen_ai.operation.name":"chat","gen_ai.response.model":%q,"gen_ai.conversation.id":%q,"gen_ai.usage.input_tokens":200,"gen_ai.usage.output_tokens":20}}`, spanID, modelID, sessionID)
}

func TestCopilotReadsNativeUsageLedger(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "session-a", "claude-sonnet-4.6", 10_000, 800, 7_000, 500, 300, "", "2026-09-19T10:11:12.123456Z")

	turns := scan(t, newCopilotAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns = %#v, want one native ledger row", turns)
	}
	turn := turns[0]
	if turn.Agent != model.AgentCopilot || turn.SessionID != "session-a" || turn.Model != "claude-sonnet-4.6" || turn.Provider != "github-copilot" || turn.Aggregate {
		t.Fatalf("attribution = %#v", turn)
	}
	if !strings.HasPrefix(turn.Key, "copilot-store|") {
		t.Fatalf("key = %q, want source-namespaced native row", turn.Key)
	}
	wantTime := time.Date(2026, 9, 19, 10, 11, 12, 123_456_000, time.UTC)
	if !turn.Timestamp.Equal(wantTime) {
		t.Fatalf("timestamp = %s, want %s", turn.Timestamp, wantTime)
	}
	if got, want := turn.Usage, (model.Usage{Input: 2_500, Output: 800, CacheRead: 7_000, CacheWrite: 500, Reasoning: 300, ContextTokens: 10_000}); got != want {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
}

func TestCopilotReadsSQLiteUTCWithoutZone(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "session", "gpt-5", 10, 2, 0, 0, 0, "", "2026-09-19 10:11:12.123456")

	turns := scan(t, newCopilotAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns = %#v", turns)
	}
	want := time.Date(2026, 9, 19, 10, 11, 12, 123_456_000, time.UTC)
	if !turns[0].Timestamp.Equal(want) {
		t.Fatalf("timestamp = %s, want UTC %s", turns[0].Timestamp, want)
	}
}

func TestCopilotReadsCompleteOlderLedgerWithoutReasoningCounter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-store.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE assistant_usage_events (
		id INTEGER PRIMARY KEY, session_id TEXT, model TEXT, input_tokens INTEGER,
		output_tokens INTEGER, cache_read_tokens INTEGER, cache_write_tokens INTEGER,
		created_at TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO assistant_usage_events VALUES
		(1, 'older', 'gpt-5', 100, 10, 60, 20, '2026-09-19T10:11:12Z')`); err != nil {
		t.Fatal(err)
	}

	turns := scan(t, newCopilotAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns = %#v", turns)
	}
	if got, want := turns[0].Usage, (model.Usage{Input: 20, Output: 10, CacheRead: 60, CacheWrite: 20, ContextTokens: 100}); got != want {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
}

func TestCopilotRejectsContradictoryNativeCacheBuckets(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "bad", "gpt-5", 100, 2, 90, 20, 0, "", "2026-09-19T10:00:00Z")
	insertCopilotUsage(t, db, "negative", "gpt-5", 100, 2, -10, -20, -1, "", "2026-09-19T10:00:01Z")

	var turns []model.Turn
	err := newCopilotAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || !strings.Contains(err.Error(), "no shutdown aggregate") {
		t.Fatalf("invalid group was silently lost: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %#v, want only recoverable negative row", turns)
	}
	if got, want := turns[0].Usage, (model.Usage{Input: 100, Output: 2, ContextTokens: 100}); got != want {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
}

func TestCopilotZeroUsageRowDoesNotDuplicateLaterValidPair(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "session", "gpt-5", 0, 0, 0, 0, 0, "", "2026-09-19T10:00:00Z")
	insertCopilotUsage(t, db, "session", "gpt-5", 20, 3, 4, 1, 1, "", "2026-09-19T10:01:00Z")

	turns := scan(t, newCopilotAt(root))
	if len(turns) != 1 || turns[0].Usage != (model.Usage{Input: 15, Output: 3, CacheRead: 4, CacheWrite: 1, Reasoning: 1, ContextTokens: 20}) {
		t.Fatalf("turns = %#v, want one valid request", turns)
	}
}

func TestCopilotShutdownJournalFallback(t *testing.T) {
	root := t.TempDir()
	eventPath := filepath.Join(root, "session-state", "session-fallback", "events.jsonl")
	writeFile(t, eventPath,
		`{"type":"assistant.message","timestamp":"2026-09-19T10:00:00Z","data":{"text":"private content the reader must ignore"}}`,
		`{"type":"session.usage_checkpoint","timestamp":"2026-09-19T10:01:00Z","data":{"totalNanoAiu":12345}}`,
		`{"type":"session.shutdown","id":"shutdown","agentId":"subagent-1","timestamp":"2026-09-19T10:02:03Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":1000,"outputTokens":70,"cacheReadTokens":600,"cacheWriteTokens":100,"reasoningTokens":20}}}}}`,
	)

	turns := scan(t, newCopilotAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns = %#v, want only shutdown aggregate", turns)
	}
	turn := turns[0]
	if turn.Key != identityKey("copilot-shutdown", "session-fallback", "gpt-5") || !turn.Aggregate || !turn.Subagent {
		t.Fatalf("identity = %#v", turn)
	}
	if got, want := turn.Usage, (model.Usage{Input: 300, Output: 70, CacheRead: 600, CacheWrite: 100, Reasoning: 20}); got != want {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
}

func TestCopilotLeavesAgentHostCLIToNativeJournal(t *testing.T) {
	cliRoot := t.TempDir()
	vscodeRoot := t.TempDir()
	writeFile(t, filepath.Join(cliRoot, "session-state", "cli-session", "events.jsonl"),
		`{"type":"session.shutdown","timestamp":"2026-09-19T10:02:03Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":100,"outputTokens":7}}}}}`,
	)
	writeFile(t, filepath.Join(vscodeRoot, "workspaceStorage", "workspace", "chatSessions", "cli.json"),
		copilotVSCodeSession("vscode-session", copilotVSCodeRequestForAgent("agent-host-copilotcli", "vscode-session", "request", "response", "gpt-5", 1789812000000, 100, 0, 7)),
	)

	turns := scan(t, newCopilotWithVSCode(cliRoot, []string{vscodeRoot}))
	if len(turns) != 1 || !turns[0].Aggregate || !strings.HasPrefix(turns[0].Key, "copilot-shutdown|") {
		t.Fatalf("CLI journal and VS Code copy were both counted: %#v", turns)
	}
}

func TestCopilotLedgerWinsOverShutdownAggregate(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "session", "gpt-5", 100, 10, 20, 5, 2, "", "2026-09-19T10:00:00Z")
	writeFile(t, filepath.Join(root, "session-state", "session", "events.jsonl"),
		`{"type":"session.shutdown","timestamp":"2026-09-19T10:02:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":100,"outputTokens":10,"cacheReadTokens":20,"cacheWriteTokens":5,"reasoningTokens":2}}}}}`,
	)

	turns := scan(t, newCopilotAt(root))
	if len(turns) != 1 || !strings.HasPrefix(turns[0].Key, "copilot-store|") || turns[0].Usage.Input != 75 {
		t.Fatalf("turns = %#v, want only per-request ledger", turns)
	}
}

func TestCopilotKeepsEveryMatchingLedgerRow(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "session", "gpt-5", 100, 10, 20, 5, 2, "", "2026-09-19T10:00:00Z")
	insertCopilotUsage(t, db, "session", "gpt-5", 50, 6, 10, 4, 1, "", "2026-09-19T10:01:00Z")
	writeFile(t, filepath.Join(root, "session-state", "session", "events.jsonl"),
		`{"type":"session.shutdown","timestamp":"2026-09-19T10:02:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":150,"outputTokens":16,"cacheReadTokens":30,"cacheWriteTokens":9,"reasoningTokens":3}}}}}`,
	)

	turns := scan(t, newCopilotAt(root))
	if len(turns) != 2 {
		t.Fatalf("turns = %#v, want both exact ledger rows", turns)
	}
	for _, turn := range turns {
		if turn.Aggregate {
			t.Fatalf("turn = %#v, want per-request ledger row", turn)
		}
	}
}

func TestCopilotShutdownFillsModelsMissingFromLedger(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "session", "gpt-5", 100, 10, 20, 5, 2, "", "2026-09-19T10:00:00Z")
	writeFile(t, filepath.Join(root, "session-state", "session", "events.jsonl"),
		`{"type":"session.shutdown","timestamp":"2026-09-19T10:02:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":100,"outputTokens":10,"cacheReadTokens":20,"cacheWriteTokens":5,"reasoningTokens":2}},"claude-sonnet-4.6":{"usage":{"inputTokens":50,"outputTokens":4}}}}}`,
	)

	turns := scan(t, newCopilotAt(root))
	if len(turns) != 2 {
		t.Fatalf("turns = %#v, want ledger row plus uncovered shutdown model", turns)
	}
	byModel := make(map[string]model.Turn, len(turns))
	for _, turn := range turns {
		byModel[turn.Model] = turn
	}
	if got := byModel["gpt-5"]; got.Aggregate || got.Usage.Input != 75 {
		t.Fatalf("ledger turn = %#v", got)
	}
	if got := byModel["claude-sonnet-4.6"]; !got.Aggregate || got.Usage.Input != 50 || got.Usage.Output != 4 {
		t.Fatalf("shutdown turn = %#v", got)
	}
}

func TestCopilotPartialLedgerEmitsOnlyShutdownResidual(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "session", "gpt-5", 100, 10, 20, 5, 2, "", "2026-09-19T10:00:00Z")
	writeFile(t, filepath.Join(root, "session-state", "session", "events.jsonl"),
		`{"type":"session.shutdown","timestamp":"2026-09-19T10:02:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":200,"outputTokens":30,"cacheReadTokens":50,"cacheWriteTokens":10,"reasoningTokens":3}}}}}`,
	)

	turns := scan(t, newCopilotAt(root))
	if len(turns) != 2 || turns[0].Aggregate || !turns[1].Aggregate {
		t.Fatalf("turns = %#v, want exact row plus uncovered residual", turns)
	}
	if got, want := turns[1].Usage, (model.Usage{Input: 65, Output: 20, CacheRead: 30, CacheWrite: 5, Reasoning: 1}); got != want {
		t.Fatalf("usage = %#v, want %#v", got, want)
	}
	var total model.Usage
	for _, turn := range turns {
		total.Add(turn.Usage)
	}
	if got, want := total, (model.Usage{Input: 140, Output: 30, CacheRead: 50, CacheWrite: 10, Reasoning: 3, ContextTokens: 100}); got != want {
		t.Fatalf("total = %#v, want %#v", got, want)
	}
}

func TestCopilotFallsBackWhenStoreLacksCompleteUsageSchema(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session-store.db")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE assistant_usage_events (
		id INTEGER PRIMARY KEY, session_id TEXT, model TEXT, input_tokens INTEGER,
		cache_read_tokens INTEGER, cache_write_tokens INTEGER, created_at TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "session-state", "session", "events.jsonl"),
		`{"type":"session.shutdown","timestamp":"2026-09-19T10:02:00Z","data":{"modelMetrics":{"gpt-5":{"usage":{"inputTokens":10,"outputTokens":2}}}}}`,
	)

	var turns []model.Turn
	err = newCopilotAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if len(turns) != 1 || !turns[0].Aggregate {
		t.Fatalf("turns = %#v, want shutdown fallback", turns)
	}
	if err == nil || !strings.Contains(err.Error(), "unrecognised Copilot assistant_usage_events layout") {
		t.Fatalf("error = %v, want schema warning", err)
	}
}

func TestCopilotCompactionKeepsTokensAndDisclosesUnknownCacheSplit(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "session", "gpt-5", 100, 10, 90, 0, 2, "compaction", "2026-09-19T10:00:00Z")

	var turns []model.Turn
	err := newCopilotAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if len(turns) != 1 || turns[0].Usage.Total() != 110 || turns[0].Usage.Input != 10 {
		t.Fatalf("turns = %#v", turns)
	}
	if err == nil || !strings.Contains(err.Error(), "cache-write breakdown") {
		t.Fatalf("error = %v, want cache split warning", err)
	}
}

func TestCopilotStoreRowsAfterShutdownDoNotConsumeOlderAggregate(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "later", "gpt-5", 100, 10, 40, 10, 2, "", "2026-09-19T12:00:00Z")
	writeFile(t, filepath.Join(root, "session-state", "later", "events.jsonl"),
		copilotShutdownLine("2026-09-19T11:00:00Z", `{"gpt-5":{"usage":{"inputTokens":80,"outputTokens":8,"cacheReadTokens":30,"cacheWriteTokens":10,"reasoningTokens":1}}}`),
	)

	turns := scan(t, newCopilotAt(root))
	if len(turns) != 2 {
		t.Fatalf("turns = %#v, want older aggregate and later exact row", turns)
	}
	if turns[0].Aggregate || !turns[1].Aggregate {
		t.Fatalf("source precision = %#v", turns)
	}
}

func TestCopilotMixedStoreAndShutdownTotalsKeepOnlyExactRows(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "mismatch", "gpt-5", 100, 10, 30, 10, 1, "", "2026-09-19T10:00:00Z")
	writeFile(t, filepath.Join(root, "session-state", "mismatch", "events.jsonl"),
		copilotShutdownLine("2026-09-19T11:00:00Z", `{"gpt-5":{"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":50,"cacheWriteTokens":20,"reasoningTokens":2}}}`),
	)

	var turns []model.Turn
	err := newCopilotAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || !strings.Contains(err.Error(), "do not reconcile componentwise") {
		t.Fatalf("error = %v, want componentwise mismatch", err)
	}
	if len(turns) != 1 || turns[0].Aggregate {
		t.Fatalf("turns = %#v, want only authoritative request row", turns)
	}
}

func TestCopilotModelAliasReconcilesOnlyUnknownResidual(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "alias", "gpt-5", 60, 6, 30, 10, 1, "", "2026-09-19T10:00:00Z")
	writeFile(t, filepath.Join(root, "session-state", "alias", "events.jsonl"),
		copilotShutdownLine("2026-09-19T11:00:00Z", `{"gpt-5-2026-08-01":{"usage":{"inputTokens":100,"outputTokens":10,"cacheReadTokens":50,"cacheWriteTokens":20,"reasoningTokens":2}}}`),
	)

	var turns []model.Turn
	err := newCopilotAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || !strings.Contains(err.Error(), "model identifiers differ") {
		t.Fatalf("error = %v, want model-attribution diagnostic", err)
	}
	if len(turns) != 2 || turns[0].Aggregate || !turns[1].Aggregate {
		t.Fatalf("turns = %#v", turns)
	}
	residual := turns[1]
	if residual.Model != "unknown" || residual.UnpricedReason == "" {
		t.Fatalf("residual attribution = %#v", residual)
	}
	if got, want := residual.Usage, (model.Usage{Input: 10, Output: 4, CacheRead: 20, CacheWrite: 10, Reasoning: 1}); got != want {
		t.Fatalf("residual = %#v, want %#v", got, want)
	}
}

func TestCopilotInvalidStorePairFallsBackAsAWhole(t *testing.T) {
	root := t.TempDir()
	db := writeCopilotUsageStore(t, root)
	defer db.Close()
	insertCopilotUsage(t, db, "bad", "gpt-5", 100, 10, 40, 10, 2, "", "2026-09-19T10:00:00Z")
	insertCopilotUsage(t, db, "bad", "gpt-5", 20, 5, 30, 0, 1, "", "2026-09-19T10:01:00Z")
	writeFile(t, filepath.Join(root, "session-state", "bad", "events.jsonl"),
		copilotShutdownLine("2026-09-19T11:00:00Z", `{"gpt-5":{"usage":{"inputTokens":120,"outputTokens":15,"cacheReadTokens":50,"cacheWriteTokens":10,"reasoningTokens":3}}}`),
	)

	var turns []model.Turn
	err := newCopilotAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || !strings.Contains(err.Error(), "invalid request row") {
		t.Fatalf("error = %v", err)
	}
	if len(turns) != 1 || !turns[0].Aggregate || turns[0].Usage.Total() != 135 {
		t.Fatalf("turns = %#v", turns)
	}
}

func TestCopilotShutdownSnapshotsAreDeltasAcrossCompaction(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "session-state", "compact", "events.jsonl"),
		copilotShutdownLine("2026-09-19T09:00:00Z", `{"gpt-5":{"usage":{"inputTokens":100,"outputTokens":10,"cacheReadTokens":40,"cacheWriteTokens":10,"reasoningTokens":2}}}`),
		`{"type":"session.compaction_complete","timestamp":"2026-09-19T09:30:00Z","data":{"success":true}}`,
		copilotShutdownLine("2026-09-19T10:00:00Z", `{"gpt-5":{"usage":{"inputTokens":30,"outputTokens":4,"cacheReadTokens":10,"cacheWriteTokens":5,"reasoningTokens":1}}}`),
	)

	var turns []model.Turn
	err := newCopilotAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || !strings.Contains(err.Error(), "pre-compaction usage may be missing") {
		t.Fatalf("error = %v", err)
	}
	if len(turns) != 2 || turns[0].Usage.Total() != 110 || turns[1].Usage != (model.Usage{Input: 15, Output: 4, CacheRead: 10, CacheWrite: 5, Reasoning: 1}) {
		t.Fatalf("turns = %#v", turns)
	}
}

func TestCopilotMixedDirectionShutdownSnapshotIsExcluded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "session-state", "mixed", "events.jsonl"),
		copilotShutdownLine("2026-09-19T09:00:00Z", `{"gpt-5":{"usage":{"inputTokens":100,"outputTokens":10,"cacheReadTokens":40,"cacheWriteTokens":10,"reasoningTokens":2}}}`),
		copilotShutdownLine("2026-09-19T10:00:00Z", `{"gpt-5":{"usage":{"inputTokens":90,"outputTokens":12,"cacheReadTokens":30,"cacheWriteTokens":10,"reasoningTokens":2}}}`),
		copilotShutdownLine("2026-09-19T11:00:00Z", `{"gpt-5":{"usage":{"inputTokens":150,"outputTokens":15,"cacheReadTokens":60,"cacheWriteTokens":15,"reasoningTokens":3}}}`),
	)

	var turns []model.Turn
	err := newCopilotAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || !strings.Contains(err.Error(), "mixed-direction") {
		t.Fatalf("error = %v", err)
	}
	if len(turns) != 2 || turns[0].Usage.Total() != 110 || turns[1].Usage != (model.Usage{Input: 25, Output: 5, CacheRead: 20, CacheWrite: 5, Reasoning: 1}) {
		t.Fatalf("turns = %#v", turns)
	}
}

func TestCopilotInvalidShutdownSnapshotDoesNotAdvanceBaseline(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "session-state", "invalid-middle", "events.jsonl"),
		copilotShutdownLine("2026-09-19T09:00:00Z", `{"gpt-5":{"usage":{"inputTokens":100,"outputTokens":10,"cacheReadTokens":40,"cacheWriteTokens":10,"reasoningTokens":2}}}`),
		copilotShutdownLine("2026-09-19T10:00:00Z", `{"gpt-5":{"usage":{"inputTokens":120,"outputTokens":12,"cacheReadTokens":130,"cacheWriteTokens":10,"reasoningTokens":2}}}`),
		copilotShutdownLine("2026-09-19T11:00:00Z", `{"gpt-5":{"usage":{"inputTokens":150,"outputTokens":15,"cacheReadTokens":60,"cacheWriteTokens":15,"reasoningTokens":3}}}`),
	)

	var turns []model.Turn
	err := newCopilotAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || !strings.Contains(err.Error(), "invalid shutdown usage") {
		t.Fatalf("error = %v", err)
	}
	if len(turns) != 2 || turns[1].Usage != (model.Usage{Input: 25, Output: 5, CacheRead: 20, CacheWrite: 5, Reasoning: 1}) {
		t.Fatalf("turns = %#v", turns)
	}
}

func copilotShutdownLine(timestamp, metrics string) string {
	return fmt.Sprintf(`{"type":"session.shutdown","timestamp":%q,"data":{"modelMetrics":%s}}`, timestamp, metrics)
}

func writeCopilotUsageStore(t *testing.T, root string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "session-store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE assistant_usage_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id TEXT NOT NULL,
		model TEXT NOT NULL,
		input_tokens INTEGER,
		output_tokens INTEGER,
		cache_read_tokens INTEGER,
		cache_write_tokens INTEGER,
		reasoning_tokens INTEGER,
		initiator TEXT,
		created_at TEXT
	)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func insertCopilotUsage(t *testing.T, db *sql.DB, sessionID, modelID string, input, output, cacheRead, cacheWrite, reasoning int64, initiator, createdAt string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO assistant_usage_events (
		session_id, model, input_tokens, output_tokens, cache_read_tokens,
		cache_write_tokens, reasoning_tokens, initiator, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, modelID, input, output, cacheRead, cacheWrite, reasoning, initiator, createdAt,
	); err != nil {
		t.Fatal(err)
	}
}
