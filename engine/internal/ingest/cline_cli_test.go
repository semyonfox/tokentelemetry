package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func clineManifestJSON(id, modelID string) string {
	return `{"version":1,"session_id":"` + id + `","started_at":"2026-09-01T23:59:00Z",` +
		`"ended_at":"2026-09-02T00:02:00Z","provider":"fallback-provider","model":"` + modelID + `",` +
		`"cwd":"/fallback","workspace_root":"/example/project","metadata":{}}`
}

func clineMessagesJSON(id, agent, messages string) string {
	return `{"version":1,"updated_at":"2026-09-02T00:02:00Z","agent":"` + agent + `",` +
		`"sessionId":"` + id + `","messages":[` + messages + `]}`
}

func clineAssistantJSON(id, sessionID, modelID, provider string, ts, input, output, read, write int64) string {
	return `{"id":"` + id + `","sessionId":"` + sessionID + `","role":"assistant","ts":` + itoa(ts) +
		`,"modelInfo":{"id":"` + modelID + `","provider":"` + provider + `"},"metrics":{` +
		`"inputTokens":` + itoa(input) + `,"outputTokens":` + itoa(output) +
		`,"cacheReadTokens":` + itoa(read) + `,"cacheWriteTokens":` + itoa(write) + `}}`
}

func writeClineV1(t *testing.T, root, dirID, manifest, messages string) {
	t.Helper()
	dir := filepath.Join(root, dirID)
	writeFile(t, filepath.Join(dir, dirID+".json"), manifest)
	writeFile(t, filepath.Join(dir, dirID+".messages.json"), messages)
}

func TestClineCLIV1NetsGrossInputAndKeepsRouteIdentity(t *testing.T) {
	root := t.TempDir()
	writeClineV1(t, root, "stored-copy", clineManifestJSON("session-1", "fallback-model"),
		clineMessagesJSON("session-1", "subagent",
			clineAssistantJSON("message-1", "session-1", "provider/model-a", "gateway", 1_788_307_200_000, 1000, 40, 600, 100)+`,`+
				clineAssistantJSON("message-2", "session-1", "model-b", "direct", 1_788_307_260_000, 500, 20, 100, 0)))

	turns := scan(t, newClineCLIAt(root))
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2: %+v", len(turns), turns)
	}
	if got := sumUsage(turns); got != (model.Usage{Input: 700, Output: 60, CacheRead: 700, CacheWrite: 100, ContextTokens: 1000}) {
		t.Fatalf("usage = %+v", got)
	}
	if turns[0].Key != "cline-cli|session-1|message-1" || turns[0].Model != "provider/model-a" || turns[0].Provider != "gateway" {
		t.Fatalf("source identity lost: %+v", turns[0])
	}
	if turns[0].Project != "/example/project" || !turns[0].Subagent || turns[0].Timestamp.IsZero() {
		t.Fatalf("attribution lost: %+v", turns[0])
	}
	if turns[1].Model != "model-b" || turns[1].Provider != "direct" {
		t.Fatalf("model switch lost: %+v", turns[1])
	}
}

func TestClineCLIV1DedupsCopiedSessionByMessageID(t *testing.T) {
	root := t.TempDir()
	manifest := clineManifestJSON("session-1", "model")
	messages := clineMessagesJSON("session-1", "lead", clineAssistantJSON("message-1", "session-1", "model", "provider", 1_788_307_200_000, 100, 10, 20, 0))
	writeClineV1(t, root, "copy-a", manifest, messages)
	writeClineV1(t, root, "copy-b", manifest, messages)

	result, err := Run(context.Background(), []Scanner{newClineCLIAt(root)})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan: %v %v", err, result.Errors)
	}
	if len(result.Turns) != 1 || result.Duplicates != 1 {
		t.Fatalf("copy not deduplicated: %+v", result)
	}
}

func TestClineCLIV1UsesSessionRollupOnlyWithoutMessageMetrics(t *testing.T) {
	root := t.TempDir()
	manifest := `{"version":1,"session_id":"s","started_at":"2026-09-01T23:59:00Z",` +
		`"ended_at":"2026-09-02T00:02:00Z","model":"model","provider":"provider","workspace_root":"/p",` +
		`"metadata":{"usage":{"inputTokens":1000,"outputTokens":20,"cacheReadTokens":600,"cacheWriteTokens":100},` +
		`"aggregateUsage":{"inputTokens":9000,"outputTokens":9000}}}`
	writeClineV1(t, root, "s", manifest, clineMessagesJSON("s", "lead", `{"id":"m","role":"assistant"}`))

	turns := scan(t, newClineCLIAt(root))
	if len(turns) != 1 || !turns[0].Aggregate {
		t.Fatalf("rollup missing: %+v", turns)
	}
	if turns[0].Usage.Input != 300 || turns[0].Usage.Total() != 1020 || turns[0].Timestamp.UTC().Day() != 2 {
		t.Fatalf("wrong rollup: %+v", turns[0])
	}
}

func TestClineCLIV1RejectsUnknownVersionAndCorruptCounts(t *testing.T) {
	root := t.TempDir()
	writeClineV1(t, root, "future", `{"version":2,"session_id":"future"}`, clineMessagesJSON("future", "lead", ""))
	writeClineV1(t, root, "corrupt", clineManifestJSON("corrupt", "model"),
		clineMessagesJSON("corrupt", "lead", clineAssistantJSON("m", "corrupt", "model", "provider", 1_788_307_200_000, 100, 60_000_000, 0, 0)))
	if turns := scan(t, newClineCLIAt(root)); len(turns) != 0 {
		t.Fatalf("accepted unsupported/corrupt data: %+v", turns)
	}
}

func TestClineCLIRootOverridePrecedence(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLINE_DIR", filepath.Join(root, "ignored"))
	t.Setenv("CLINE_DATA_DIR", filepath.Join(root, "also-ignored"))
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLINE_SESSION_DATA_DIR", sessions)
	got := NewClineCLI().Roots()
	if len(got) != 1 || got[0] != sessions {
		t.Fatalf("roots = %v", got)
	}
}
