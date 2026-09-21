package ingest

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func legacyMessage(ts, metrics string) string {
	return `{"type":"say","say":"api_req_started","ts":` + ts + `,"text":` + strconv.Quote(metrics) + `}`
}

func writeLegacyClineTask(t *testing.T, root, taskID string, messages ...string) {
	t.Helper()
	dir := filepath.Join(root, "tasks", taskID)
	writeFile(t, filepath.Join(dir, "ui_messages.json"), `[`+strings.Join(messages, ",")+`]`)
}

func writeLegacyClineHistory(t *testing.T, root, taskID, text string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "tasks", taskID, "api_conversation_history.json"),
		`[{"role":"user","content":[{"type":"text","text":`+strconv.Quote(text)+`}]}]`)
}

func TestLegacyClineKeepsDisjointInputAndHistoryMetadata(t *testing.T) {
	root := t.TempDir()
	writeLegacyClineTask(t, root, "task-1",
		`{"type":"say","say":"text","ts":1788307000000,"text":"ignored"}`,
		legacyMessage("1788307200000", `{"tokensIn":300,"tokensOut":40,"cacheReads":600,"cacheWrites":100,"cost":0.01}`),
		legacyMessage(`"2026-09-02T00:01:00Z"`, `{"tokensIn":200,"tokensOut":20}`),
	)
	writeLegacyClineHistory(t, root, "task-1", "<model>openrouter/author/model</model>\nCurrent Workspace Directory (/example/project)")

	turns := scan(t, newClineAt(root))
	if len(turns) != 2 {
		t.Fatalf("turns = %d: %+v", len(turns), turns)
	}
	if turns[0].Usage.Input != 300 || turns[0].Usage.ContextTokens != 1000 || turns[0].Usage.Total() != 1040 {
		t.Fatalf("classic net input was changed: %+v", turns[0].Usage)
	}
	if turns[0].Model != "openrouter/author/model" || turns[0].Project != "/example/project" || turns[0].SessionID != "task-1" {
		t.Fatalf("metadata lost: %+v", turns[0])
	}
	if turns[1].Timestamp.IsZero() || turns[0].Agent != "cline" {
		t.Fatalf("source attribution lost: %+v", turns)
	}
}

func TestLegacyClineUnknownModelStaysExplicitAndMalformedRowsDoNotAbort(t *testing.T) {
	root := t.TempDir()
	writeLegacyClineTask(t, root, "task",
		legacyMessage(`"not-a-time"`, `{broken`),
		legacyMessage(`"not-a-time"`, `{"tokensIn":10,"tokensOut":2}`),
		legacyMessage("1788307200002", `{"tokensIn":0,"tokensOut":0}`),
	)
	turns := scan(t, newClineAt(root))
	if len(turns) != 1 || turns[0].Model != "unknown" || !turns[0].Timestamp.IsZero() {
		t.Fatalf("unexpected fallback: %+v", turns)
	}
}

func TestLegacyClineDedupsSameTaskAcrossEditorStores(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	message := legacyMessage("1788307200000", `{"tokensIn":10,"tokensOut":2}`)
	writeLegacyClineTask(t, a, "task", message)
	writeLegacyClineTask(t, b, "task", message)
	result, err := Run(context.Background(), []Scanner{newClineAt(a, b)})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan: %v %v", err, result.Errors)
	}
	if len(result.Turns) != 1 || result.Duplicates != 1 {
		t.Fatalf("duplicate task copy counted: %+v", result)
	}
}
