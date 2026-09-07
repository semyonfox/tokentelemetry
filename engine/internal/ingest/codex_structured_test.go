package ingest

import (
	"context"
	"encoding/json"
	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
	"path/filepath"
	"strings"
	"testing"
)

func structuredLine(owner, turn, response, ts string, u codexUsage) string {
	b, err := json.Marshal(map[string]interface{}{
		"type": "token_usage_record", "timestamp": ts, "payload": map[string]interface{}{
			"thread_id": owner, "turn_id": turn, "response_id": response, "session_id": owner, "root_turn_id": turn,
			"usage": u, "turn_token_usage": u, "thread_token_usage": u,
		},
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func codexContextWithID(id, modelID string) string {
	return `{"type":"turn_context","payload":{"turn_id":"` + id + `","model":"` + modelID + `","cwd":"/example"}}`
}

func TestCodexStructuredFastCallsAndRepeatedCounters(t *testing.T) {
	root := t.TempDir()
	u := codexUsage{Input: 1000, Cached: 600, Output: 20}
	writeFile(t, filepath.Join(root, "sessions", "rollout-child.jsonl"),
		codexMeta("child", "parent", "subagent", "example"),
		codexContextWithID("turn-a", "example"),
		structuredLine("child", "turn-a", "r1", "2026-09-01T12:00:00.100Z", u),
		codexTokens("2026-09-01T12:00:00.101Z", 1000, 600, 20),
		structuredLine("child", "turn-a", "r2", "2026-09-01T12:00:00.200Z", u),
		codexTokens("2026-09-01T12:00:00.201Z", 1000, 600, 20),
	)
	turns := codexAccounted(t, root)
	// Two independently identified responses with equal usage are two calls.
	if len(turns) != 2 || sumUsage(turns).Total() != 2040 || sumUsage(turns).Input != 800 {
		t.Fatalf("structured calls: %+v", turns)
	}
	for _, v := range turns {
		if v.ReplayHeuristic || !v.Subagent {
			t.Fatalf("wrong attribution: %+v", v)
		}
	}
}

func TestCodexStructuredCopiedParentAndDuplicateFiles(t *testing.T) {
	root := t.TempDir()
	parent := structuredLine("parent", "p-turn", "r1", "2026-09-01T23:59:00Z", codexUsage{Input: 100, Output: 10})
	writeFile(t, filepath.Join(root, "sessions", "rollout-parent.jsonl"), codexMeta("parent", "", "user", "model-a"), codexContextWithID("p-turn", "model-a"), parent)
	writeFile(t, filepath.Join(root, "sessions", "rollout-parent-copy.jsonl"), codexMeta("parent", "", "user", "model-a"), codexContextWithID("p-turn", "model-a"), parent)
	writeFile(t, filepath.Join(root, "sessions", "rollout-child.jsonl"),
		codexMeta("child", "parent", "subagent", "model-b"),
		codexContextWithID("p-turn", "model-a"),
		structuredLine("parent", "p-turn", "r1", "2026-09-02T12:00:00Z", codexUsage{Input: 100, Output: 10}),
		codexTokens("2026-09-02T12:00:00Z", 100, 0, 10),
		codexContextWithID("c-turn", "model-b"),
		structuredLine("child", "c-turn", "r2", "2026-09-02T12:00:00.100Z", codexUsage{Input: 200, Output: 20}),
	)
	turns := codexAccounted(t, root)
	if len(turns) != 2 || sumUsage(turns).Total() != 330 {
		t.Fatalf("copy counted: %+v", turns)
	}
	for _, v := range turns {
		if v.SessionID == "parent" && (v.Model != "model-a" || v.Timestamp.UTC().Day() != 1 || v.Subagent) {
			t.Fatalf("copied ownership: %+v", v)
		}
	}
}

func TestCodexStructuredUpgradeKeepsLegacyHistory(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sessions", "rollout-upgrade.jsonl"),
		codexMeta("s", "", "user", "old"),
		codexContextWithID("old-turn", "old"),
		codexTokens("2026-09-01T12:00:00Z", 100, 0, 10),
		codexContextWithID("new-turn", "new"),
		// token_count may arrive before or after the durable response record.
		codexTokens("2026-09-02T12:00:00Z", 200, 0, 20),
		structuredLine("s", "new-turn", "r2", "2026-09-02T12:00:00Z", codexUsage{Input: 200, Output: 20}),
	)
	turns := codexAccounted(t, root)
	if len(turns) != 2 || sumUsage(turns).Total() != 330 {
		t.Fatalf("upgrade lost or doubled history: %+v", turns)
	}
	if turns[0].Model != "old" || turns[1].Model != "new" {
		t.Fatalf("model switch lost: %+v", turns)
	}
}

func TestCodexInvalidStructuredUsageDoesNotSuppressLegacy(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sessions", "rollout-invalid.jsonl"),
		codexMeta("s", "", "user", "example"), codexContextWithID("turn-a", "example"),
		structuredLine("s", "turn-a", "r1", "2026-09-01T12:00:00Z", codexUsage{Input: 100, Cached: 200, Output: 10}),
		codexTokens("2026-09-01T12:00:00Z", 100, 0, 10),
	)
	turns := codexAccounted(t, root)
	if len(turns) != 1 || sumUsage(turns).Total() != 110 {
		t.Fatalf("legacy fallback lost: %+v", turns)
	}
}

func TestCodexStructuredCacheBucketsDoNotOverlap(t *testing.T) {
	u, ok := structuredCodexUsage(codexUsage{Input: 1000, Cached: 600, CacheWrite: 100, Output: 20, Reasoning: 5})
	if !ok || u.Input != 300 || u.Total() != 1020 || u.Reasoning != 5 {
		t.Fatalf("bad normalization: %+v", u)
	}
}

func codexAccounted(t *testing.T, root string) []model.Turn {
	t.Helper()
	res, err := Run(context.Background(), []Scanner{newCodexAt(root)})
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("scan: %v %v", err, res.Errors)
	}
	return res.Turns
}

func TestCodexLegacyCacheWritesAreNotFreshInput(t *testing.T) {
	root := t.TempDir()
	line := codexTokens("2026-09-01T12:00:00Z", 1000, 600, 20)
	line = strings.ReplaceAll(line, `"cache_write_input_tokens":0`, `"cache_write_input_tokens":100`)
	writeFile(t, filepath.Join(root, "sessions", "rollout-cache.jsonl"), codexMeta("s", "", "user", "example"), line)
	turns := codexAccounted(t, root)
	if len(turns) != 1 || turns[0].Usage.Input != 300 || turns[0].Usage.Total() != 1020 {
		t.Fatalf("cache write double counted: %+v", turns)
	}
}
