package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func writeGrokSession(t *testing.T, root, group, sessionID, cwd, kind, parentID, usage string) string {
	t.Helper()
	dir := filepath.Join(root, "sessions", group, sessionID)
	usage = strings.TrimSpace(usage)
	if strings.HasPrefix(usage, "{") {
		usage = fmt.Sprintf(`{"sessionId":%q,`, sessionID) + strings.TrimPrefix(usage, "{")
	}
	summary := fmt.Sprintf(
		`{"info":{"cwd":%q,"title":"must not be read"},"session_kind":%q,"parent_session_id":%q,"generated_summary":"must not be read"}`,
		cwd, kind, parentID,
	)
	writeFile(t, filepath.Join(dir, "summary.json"), summary)
	writeFile(t, filepath.Join(dir, "usage.json"), usage)
	return dir
}

func collectGrok(t *testing.T, scanner *Grok) ([]model.Turn, error) {
	t.Helper()
	var turns []model.Turn
	err := scanner.Scan(context.Background(), func(turn model.Turn) {
		turns = append(turns, turn)
	})
	return turns, err
}

func TestGrokEmitsPerModelRowsAndNormalizesGrossInput(t *testing.T) {
	root := t.TempDir()
	writeGrokSession(t, root, "project-a", "session-a", "/work/project-a", "main", "", `{
		"updatedAt":"2026-09-18T12:01:00Z",
		"session":{"inputTokens":999999,"outputTokens":999999},
		"turns":[{
			"turnNumber":7,
			"endedAt":"2026-09-18T12:00:00.123Z",
			"inputTokens":999999,
			"outputTokens":999999,
			"modelCalls":99,
			"primaryModelId":"enclosing-total-must-not-emit",
			"modelUsage":{
				"grok-4-fast":{"inputTokens":1000,"outputTokens":200,"cachedReadTokens":600,"cacheCreationTokens":100,"reasoningTokens":50,"totalTokens":1200,"modelCalls":1,"costUsdTicks":1200000},
				"grok-code-fast-1":{"inputTokens":400,"outputTokens":80,"cachedReadTokens":100,"cacheCreationTokens":50,"reasoningTokens":20,"totalTokens":480,"modelCalls":2,"costUsdTicks":480000}
			}
		}]
	}`)

	turns := scan(t, newGrokAt(root))
	if got, want := len(turns), 2; got != want {
		t.Fatalf("turns = %d, want %d", got, want)
	}

	first := turns[0]
	if first.Model != "grok-4-fast" {
		t.Fatalf("first model = %q, want grok-4-fast", first.Model)
	}
	if got, want := first.Usage, (model.Usage{Input: 300, Output: 200, CacheRead: 600, CacheWrite: 100, Reasoning: 50, ContextTokens: 1000}); got != want {
		t.Errorf("first usage = %+v, want %+v", got, want)
	}
	if first.Aggregate {
		t.Error("single-call model row marked aggregate")
	}
	if first.SessionID != "session-a" || first.Project != "/work/project-a" || first.Agent != model.AgentGrok {
		t.Errorf("identity = session %q project %q agent %q", first.SessionID, first.Project, first.Agent)
	}
	wantTime := time.Date(2026, 9, 18, 12, 0, 0, 123_000_000, time.UTC)
	if !first.Timestamp.Equal(wantTime) {
		t.Errorf("timestamp = %s, want %s", first.Timestamp, wantTime)
	}

	second := turns[1]
	if second.Model != "grok-code-fast-1" {
		t.Fatalf("second model = %q, want grok-code-fast-1", second.Model)
	}
	if got, want := second.Usage, (model.Usage{Input: 250, Output: 80, CacheRead: 100, CacheWrite: 50, Reasoning: 20}); got != want {
		t.Errorf("second usage = %+v, want %+v", got, want)
	}
	if !second.Aggregate {
		t.Error("two-call model row not marked aggregate")
	}
	if first.Key == "" || second.Key == "" || first.Key == second.Key {
		t.Errorf("keys are not distinct stable identities: %q %q", first.Key, second.Key)
	}
}

func TestGrokFallsBackToTurnSummaryAndWarnsOnIncompleteUsage(t *testing.T) {
	root := t.TempDir()
	writeGrokSession(t, root, "project", "session", "/work/project", "main", "", `{
		"turns":[{
			"turnNumber":1,
			"endedAt":"2026-09-18T13:00:00Z",
			"inputTokens":900,
			"outputTokens":100,
			"cachedReadTokens":700,
			"cacheCreationTokens":50,
			"reasoningTokens":25,
			"totalTokens":1000,
			"modelCalls":1,
			"primaryModelId":"grok-code-fast-1",
			"usageIsIncomplete":true
		}]
	}`)

	turns, err := collectGrok(t, newGrokAt(root))
	if err == nil || !strings.Contains(err.Error(), "1 persisted turn(s) report incomplete usage") {
		t.Fatalf("warning = %v, want incomplete-usage warning", err)
	}
	if got, want := len(turns), 1; got != want {
		t.Fatalf("turns = %d, want %d", got, want)
	}
	if got, want := turns[0].Usage, (model.Usage{Input: 150, Output: 100, CacheRead: 700, CacheWrite: 50, Reasoning: 25, ContextTokens: 900}); got != want {
		t.Errorf("usage = %+v, want %+v", got, want)
	}
}

func TestGrokWarnsWhenNestedModelUsageIsIncomplete(t *testing.T) {
	root := t.TempDir()
	writeGrokSession(t, root, "project", "session", "/work/project", "main", "", `{
		"turns":[{
			"turnNumber":1,
			"endedAt":"2026-09-18T13:00:00Z",
			"modelUsage":{"grok-code-fast-1":{"inputTokens":10,"outputTokens":2,"totalTokens":12,"modelCalls":1,"usageIsIncomplete":true}}
		}]
	}`)

	turns, err := collectGrok(t, newGrokAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(turns))
	}
	if err == nil || !strings.Contains(err.Error(), "incomplete usage") {
		t.Fatalf("warning = %v, want incomplete-usage warning", err)
	}
}

func TestGrokForkFingerprintExcludesSessionIdentity(t *testing.T) {
	root := t.TempDir()
	parentUsage := `{
		"turns":[{
			"turnNumber":3,
			"endedAt":"2026-09-18T14:00:00Z",
			"modelUsage":{"grok-code-fast-1":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":70,"cacheCreationTokens":10,"reasoningTokens":5,"totalTokens":120,"modelCalls":1,"costUsdTicks":77}}
		}]
	}`
	forkUsage := `{
		"turns":[{
			"turnNumber":3,
			"endedAt":"2026-09-18T15:00:00+01:00",
			"modelUsage":{"grok-code-fast-1":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":70,"cacheCreationTokens":10,"reasoningTokens":5,"totalTokens":120,"modelCalls":1,"costUsdTicks":88}}
		}]
	}`
	writeGrokSession(t, root, "project", "a-parent", "/original", "main", "", parentUsage)
	writeGrokSession(t, root, "project", "b-fork", "/fork", "fork", "a-parent", forkUsage)

	result, err := Run(context.Background(), []Scanner{newGrokAt(root)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(result.Turns), 1; got != want {
		t.Fatalf("kept turns = %d, want %d", got, want)
	}
	if got, want := result.Duplicates, 1; got != want {
		t.Errorf("duplicates = %d, want %d", got, want)
	}
	if result.Turns[0].SessionID != "a-parent" {
		t.Errorf("kept session = %q, want original a-parent", result.Turns[0].SessionID)
	}
}

func TestGrokPrefersExactChildrenOverAmbiguousParentAggregate(t *testing.T) {
	root := t.TempDir()
	usage := func(turn int, timestamp string, output int) string {
		return fmt.Sprintf(`{"turns":[{"turnNumber":%d,"endedAt":%q,"primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":%d,"totalTokens":%d,"modelCalls":1}]}`,
			turn, timestamp, output, 10+output)
	}
	// The parent total contains its own one-call usage plus all three child
	// ledgers. usage.json has no child identity in that aggregate, so the
	// scanner must keep the exact child rows rather than guessing that the
	// parent folded them.
	parent := writeGrokSession(t, root, "project", "parent", "/work", "main", "", `{
		"turns":[{"turnNumber":1,"endedAt":"2026-09-18T15:05:00Z","primaryModelId":"grok-code-fast-1","inputTokens":40,"outputTokens":10,"totalTokens":50,"modelCalls":4}]
	}`)
	childA := writeGrokSession(t, root, "project", "child-a", "/work", "subagent", "parent", usage(2, "2026-09-18T15:01:00Z", 2))
	childB := writeGrokSession(t, root, "project", "child-b", "/work", "subagent_fork", "parent", usage(3, "2026-09-18T15:02:00Z", 3))
	childC := writeGrokSession(t, root, "project", "child-c", "/work", "subagent_resume", "parent", usage(4, "2026-09-18T15:03:00Z", 4))
	writeGrokSession(t, root, "project", "orphan", "/other", "subagent", "missing-parent", usage(5, "2026-09-18T15:04:00Z", 5))
	// A newer parent rewrite still cannot identify which child ledger it folded.
	childTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, child := range []string{childA, childB, childC} {
		if err := os.Chtimes(filepath.Join(child, "usage.json"), childTime, childTime); err != nil {
			t.Fatal(err)
		}
	}
	parentTime := childTime.Add(time.Minute)
	if err := os.Chtimes(filepath.Join(parent, "usage.json"), parentTime, parentTime); err != nil {
		t.Fatal(err)
	}

	turns, err := collectGrok(t, newGrokAt(root))
	if err == nil || !strings.Contains(err.Error(), "omitted 1 parent ledger(s)") {
		t.Fatalf("warning = %v, want ambiguous-parent warning", err)
	}
	if got, want := len(turns), 4; got != want {
		t.Fatalf("turns = %d, want three children plus orphan (%d)", got, want)
	}
	bySession := make(map[string]model.Turn, len(turns))
	for _, turn := range turns {
		bySession[turn.SessionID] = turn
	}
	if _, ok := bySession["parent"]; ok {
		t.Error("ambiguous parent aggregate was emitted")
	}
	for _, childID := range []string{"child-a", "child-b", "child-c"} {
		child, ok := bySession[childID]
		if !ok || !child.Subagent {
			t.Errorf("child attribution for %s = %+v, present %v", childID, child, ok)
		}
	}
	if orphan, ok := bySession["orphan"]; !ok || !orphan.Subagent {
		t.Errorf("orphan attribution = %+v, present %v", orphan, ok)
	}
	if got, want := totalOut(turns), int64(14); got != want {
		t.Errorf("output tokens = %d, want exact child/orphan total %d", got, want)
	}
}

func TestGrokOmitsAncestorAggregatesWhenNestedChildIsRetained(t *testing.T) {
	root := t.TempDir()
	usage := func(output int) string {
		return fmt.Sprintf(`{"turns":[{"turnNumber":1,"endedAt":"2026-09-18T15:00:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":%d,"totalTokens":%d,"modelCalls":1}]}`,
			output, 10+output)
	}
	writeGrokSession(t, root, "project", "grandparent", "/work", "main", "", usage(3))
	writeGrokSession(t, root, "project", "parent", "/work", "subagent", "grandparent", usage(2))
	writeGrokSession(t, root, "project", "child", "/work", "subagent", "parent", usage(1))

	turns, err := collectGrok(t, newGrokAt(root))
	if got, want := len(turns), 1; got != want {
		t.Fatalf("turns = %d, want retained nested child only (%d)", got, want)
	}
	if turns[0].SessionID != "child" || !turns[0].Subagent {
		t.Errorf("retained turn = %+v, want nested child", turns[0])
	}
	if err == nil || !strings.Contains(err.Error(), "omitted 2 parent ledger(s)") {
		t.Fatalf("warning = %v, want ancestor-aggregate warning", err)
	}
}

func TestGrokRetainsIncompleteChildWarningWhenParentIsAmbiguous(t *testing.T) {
	root := t.TempDir()
	writeGrokSession(t, root, "project", "parent", "/work", "main", "", `{
		"turns":[{"turnNumber":1,"endedAt":"2026-09-18T15:02:00Z","primaryModelId":"grok-code-fast-1","inputTokens":20,"outputTokens":2,"totalTokens":22,"modelCalls":2}]
	}`)
	writeGrokSession(t, root, "project", "child", "/work", "subagent", "parent", `{
		"turns":[{"turnNumber":1,"endedAt":"2026-09-18T15:01:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":1,"totalTokens":11,"modelCalls":1,"usageIsIncomplete":true}]
	}`)

	turns, err := collectGrok(t, newGrokAt(root))
	if got, want := len(turns), 1; got != want {
		t.Fatalf("turns = %d, want retained child only (%d)", got, want)
	}
	if turns[0].SessionID != "child" || !turns[0].Subagent {
		t.Errorf("retained turn = %+v, want incomplete child", turns[0])
	}
	if err == nil || !strings.Contains(err.Error(), "1 persisted turn(s) report incomplete usage") ||
		!strings.Contains(err.Error(), "omitted 1 parent ledger(s)") {
		t.Fatalf("warning = %v, want incomplete-child and ambiguous-parent warnings", err)
	}
}

func TestGrokUsesNewestLedgerForDuplicateSessionID(t *testing.T) {
	root := t.TempDir()
	usage := func(output int) string {
		return fmt.Sprintf(`{"turns":[{"turnNumber":1,"endedAt":"2026-09-18T16:00:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":%d,"totalTokens":%d,"modelCalls":1}]}`,
			output, 10+output)
	}
	stale := writeGrokSession(t, root, "stale-copy", "session", "/work", "main", "", usage(1))
	newer := writeGrokSession(t, root, "new-copy", "session", "/work", "main", "", usage(2))
	oldTime := time.Date(2026, 9, 18, 16, 1, 0, 0, time.UTC)
	newTime := oldTime.Add(time.Minute)
	if err := os.Chtimes(filepath.Join(stale, "usage.json"), oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(newer, "usage.json"), newTime, newTime); err != nil {
		t.Fatal(err)
	}

	turns := scan(t, newGrokAt(root))
	if got, want := len(turns), 1; got != want {
		t.Fatalf("turns = %d, want canonical newest ledger only (%d)", got, want)
	}
	if got, want := turns[0].Usage.Output, int64(2); got != want {
		t.Errorf("canonical output = %d, want newest ledger output %d", got, want)
	}
}

func TestGrokRejectsNonzeroUsageWithoutModelCalls(t *testing.T) {
	root := t.TempDir()
	writeGrokSession(t, root, "project", "session", "/work", "main", "", `{
		"turns":[{"turnNumber":1,"endedAt":"2026-09-18T16:00:00Z","primaryModelId":"grok-code-fast-1","inputTokens":2500000000000000,"outputTokens":1,"totalTokens":2500000000000001,"modelCalls":0}]
	}`)

	if turns := scan(t, newGrokAt(root)); len(turns) != 0 {
		t.Fatalf("turns = %+v, want nonzero zero-call usage skipped", turns)
	}
}

func TestGrokUsagePathsSkipSymlinks(t *testing.T) {
	root := t.TempDir()
	dir := writeGrokSession(t, root, "project", "session", "/work", "main", "", `{
		"turns":[{"turnNumber":1,"endedAt":"2026-09-18T16:00:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":1,"totalTokens":11,"modelCalls":1}]
	}`)
	link := filepath.Join(root, "sessions", "project", "linked", "usage.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "usage.json"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	paths, err := grokUsagePaths(context.Background(), filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(paths), 1; got != want {
		t.Fatalf("usage paths = %v, want only regular ledger (%d)", paths, want)
	}
	if paths[0] != filepath.Join(dir, "usage.json") {
		t.Errorf("usage path = %q, want regular ledger", paths[0])
	}
}

func TestGrokSkipsMalformedTornAndInvalidRecords(t *testing.T) {
	root := t.TempDir()

	brokenUsage := writeGrokSession(t, root, "project", "broken-usage", "/work", "main", "", `{"turns":[`)
	writeFile(t, filepath.Join(brokenUsage, "transcript.jsonl"), `{"secret":"scanner must not inspect this"}`)

	brokenSummary := writeGrokSession(t, root, "project", "broken-summary", "/work", "main", "", `{"turns":[]}`)
	writeFile(t, filepath.Join(brokenSummary, "summary.json"), `{"info":`)

	doubleJSON := writeGrokSession(t, root, "project", "double-json", "/work", "main", "", `{"turns":[]} {"turns":[]}`)
	writeFile(t, filepath.Join(doubleJSON, "transcript.jsonl"), "not json and not relevant")

	writeGrokSession(t, root, "project", "mixed", "/work", "main", "", `{
		"turns":[
			{"turnNumber":1,"endedAt":"not-a-time","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":1,"totalTokens":11,"modelCalls":1},
			{"turnNumber":2,"endedAt":"2026-09-18T16:01:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":1,"cachedReadTokens":11,"totalTokens":11,"modelCalls":1},
			{"turnNumber":3,"endedAt":"2026-09-18T16:02:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":1,"reasoningTokens":2,"totalTokens":11,"modelCalls":1},
			{"turnNumber":4,"endedAt":"2026-09-18T16:03:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":1,"totalTokens":12,"modelCalls":1},
			{"turnNumber":5,"endedAt":"2026-09-18T16:04:00Z","primaryModelId":"grok-code-fast-1","inputTokens":0,"outputTokens":0,"totalTokens":0,"modelCalls":1},
			{"turnNumber":6,"endedAt":"2026-09-18T16:05:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":1,"totalTokens":11,"modelCalls":1}
		]
	}`)

	turns := scan(t, newGrokAt(root))
	if got, want := len(turns), 1; got != want {
		t.Fatalf("turns = %d, want only valid row (%d)", got, want)
	}
	if turns[0].SessionID != "mixed" || turns[0].Usage.Input != 10 || turns[0].Usage.Output != 1 {
		t.Errorf("valid turn = %+v", turns[0])
	}
}

func TestGrokKeepsValidLedgerWithoutUsableSummary(t *testing.T) {
	root := t.TempDir()
	usage := `{"turns":[{"turnNumber":1,"endedAt":"2026-09-18T16:30:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":1,"totalTokens":11,"modelCalls":1}]}`
	missing := writeGrokSession(t, root, "project", "missing-summary", "/ignored", "main", "", usage)
	if err := os.Remove(filepath.Join(missing, "summary.json")); err != nil {
		t.Fatal(err)
	}
	corrupt := writeGrokSession(t, root, "project", "corrupt-summary", "/ignored", "main", "", usage)
	writeFile(t, filepath.Join(corrupt, "summary.json"), `{"info":`)

	turns := scan(t, newGrokAt(root))
	if got, want := len(turns), 2; got != want {
		t.Fatalf("turns = %d, want %d", got, want)
	}
	for _, turn := range turns {
		if turn.Project != "" || turn.Subagent {
			t.Errorf("unenriched turn = %+v", turn)
		}
	}
}

func TestGrokRequiresLedgerSessionID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "project", "directory-id")
	writeFile(t, filepath.Join(dir, "summary.json"), `{"info":{"cwd":"/work"}}`)
	writeFile(t, filepath.Join(dir, "usage.json"), `{"turns":[{"turnNumber":1,"endedAt":"2026-09-18T16:30:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":1,"totalTokens":11,"modelCalls":1}]}`)

	if turns := scan(t, newGrokAt(root)); len(turns) != 0 {
		t.Fatalf("turns = %d, want ledger without sessionId skipped", len(turns))
	}
}

func TestGrokUsesLedgerSessionIDInsteadOfDirectoryName(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "project", "directory-name")
	writeFile(t, filepath.Join(dir, "usage.json"), `{"sessionId":"ledger-id","turns":[{"turnNumber":1,"endedAt":"2026-09-18T16:30:00Z","primaryModelId":"grok-code-fast-1","inputTokens":10,"outputTokens":1,"totalTokens":11,"modelCalls":1}]}`)

	turns := scan(t, newGrokAt(root))
	if len(turns) != 1 || turns[0].SessionID != "ledger-id" {
		t.Fatalf("turns = %+v, want ledger sessionId", turns)
	}
}

func TestGrokAllowsLargeAggregatesButRejectsOversizedSingleCalls(t *testing.T) {
	root := t.TempDir()
	writeGrokSession(t, root, "project", "session", "/work", "main", "", `{
		"turns":[
			{"turnNumber":1,"endedAt":"2026-09-18T17:00:00Z","primaryModelId":"grok-code-fast-1","inputTokens":60000000,"outputTokens":5000000,"totalTokens":65000000,"modelCalls":2},
			{"turnNumber":2,"endedAt":"2026-09-18T17:01:00Z","primaryModelId":"grok-code-fast-1","inputTokens":60000000,"outputTokens":1,"totalTokens":60000001,"modelCalls":1}
		]
	}`)

	turns := scan(t, newGrokAt(root))
	if got, want := len(turns), 1; got != want {
		t.Fatalf("turns = %d, want aggregate only (%d)", got, want)
	}
	if !turns[0].Aggregate || turns[0].Usage.Input != 60_000_000 || turns[0].Usage.ContextTokens != 0 {
		t.Errorf("aggregate turn = %+v", turns[0])
	}
}

func TestNewGrokUsesGrokHomeSessionsDirectory(t *testing.T) {
	root := t.TempDir()
	writeGrokSession(t, root, filepath.Join("deeply", "nested"), "session", "/work", "main", "", `{"turns":[]}`)
	t.Setenv("GROK_HOME", root)

	scanner := NewGrok()
	want := filepath.Join(root, "sessions")
	if got := scanner.Roots(); len(got) != 1 || got[0] != want {
		t.Fatalf("roots = %v, want [%s]", got, want)
	}
}
