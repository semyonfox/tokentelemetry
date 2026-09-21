package ingest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestQwenNetsGrossPromptAndIncludesThoughtsInOutput(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "project", "chats", "s.jsonl"),
		`{"uuid":"u1","sessionId":"s1","timestamp":"2026-09-01T10:00:00Z","type":"assistant","cwd":"/work/qwen","model":"qwen3","usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":80,"thoughtsTokenCount":20,"cachedContentTokenCount":700}}`)
	turns := scan(t, newQwenAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns = %d", len(turns))
	}
	want := model.Usage{Input: 300, Output: 100, CacheRead: 700, Reasoning: 20, ContextTokens: 1000}
	if turns[0].Usage != want || turns[0].Project != "/work/qwen" {
		t.Fatalf("turn = %+v, want usage %+v", turns[0], want)
	}
}

func TestQwenForkedHistoryDeduplicatesByRecordedOrigin(t *testing.T) {
	root := t.TempDir()
	original := `{"uuid":"u1","sessionId":"parent","timestamp":"2026-09-01T10:00:00Z","type":"assistant","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":0,"cachedContentTokenCount":3}}`
	fork := `{"uuid":"u1","sessionId":"child","timestamp":"2026-09-01T10:00:00Z","type":"assistant","forkedFrom":{"sessionId":"parent","messageUuid":"u1"},"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":0,"cachedContentTokenCount":3}}`
	writeFile(t, filepath.Join(root, "p", "chats", "a.jsonl"), original)
	writeFile(t, filepath.Join(root, "p", "chats", "archive", "b.jsonl"), fork)
	result, err := Run(context.Background(), []Scanner{newQwenAt(root)})
	if err != nil || len(result.Errors) != 0 || len(result.Turns) != 1 || result.Duplicates != 1 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}
