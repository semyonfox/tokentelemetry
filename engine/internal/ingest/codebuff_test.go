package ingest

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCodebuffKeepsCreditsWithoutInventingTokensOrDollars(t *testing.T) {
	root := filepath.Join(t.TempDir(), "manicode")
	chat := filepath.Join(root, "projects", "slug", "chats", "chat-1")
	writeFile(t, filepath.Join(chat, "chat-messages.json"), `[{"id":"user","variant":"user","content":"hi"},{"id":"answer","variant":"ai","timestamp":"2026-09-01T00:00:00Z","credits":2.5,"metadata":{"runState":{"sessionState":{"mainAgentState":{"messageHistory":[{"role":"assistant","providerOptions":{"usage":{"inputTokens":999,"outputTokens":999}}}]}}}}}]`)
	writeFile(t, filepath.Join(chat, "run-state.json"), `{"sessionState":{"projectContext":{"cwd":"/work/repo"}}}`)
	turns := scan(t, newCodebuffAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns: %+v", turns)
	}
	turn := turns[0]
	if turn.Credits == nil || *turn.Credits != 2.5 || !turn.Usage.IsZero() {
		t.Fatalf("credits/tokens: %+v", turn)
	}
	if turn.Project != "/work/repo" || turn.Model != "unknown" {
		t.Fatalf("metadata: %+v", turn)
	}
}

func TestCodebuffDedupsCopiedMessagesByNativeID(t *testing.T) {
	base := t.TempDir()
	roots := []string{filepath.Join(base, "manicode"), filepath.Join(base, "archive")}
	line := `[{"id":"native-message","variant":"assistant","credits":1}]`
	for _, root := range roots {
		writeFile(t, filepath.Join(root, "projects", "p", "chats", "chat", "chat-messages.json"), line)
	}
	result, err := Run(context.Background(), []Scanner{newCodebuffAt(roots...)})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan: %v %v", err, result.Errors)
	}
	if len(result.Turns) != 1 || result.Duplicates != 1 {
		t.Fatalf("dedup: %+v", result)
	}
}

func TestCodebuffDropsOversizedRawCountBeforeNormalizing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "manicode")
	chat := filepath.Join(root, "projects", "p", "chats", "chat")
	writeFile(t, filepath.Join(chat, "chat-messages.json"), `[{"id":"bad","variant":"assistant","metadata":{"usage":{"inputTokens":1,"outputTokens":9223372036854775807,"reasoningOutputTokens":1}}},{"id":"good","variant":"assistant","metadata":{"usage":{"inputTokens":2}}}]`)
	turns := scan(t, newCodebuffAt(root))
	if len(turns) != 1 || turns[0].Usage.Input != 2 {
		t.Fatalf("turns: %+v", turns)
	}
}

func TestCodebuffNormalizesProviderGrossInputWhenDirectUsageExists(t *testing.T) {
	root := filepath.Join(t.TempDir(), "manicode-dev")
	chat := filepath.Join(root, "projects", "p", "chats", "chat")
	writeFile(t, filepath.Join(chat, "chat-messages.json"), `[{"id":"answer","variant":"assistant","timestamp":"2026-09-01T00:00:00Z","metadata":{"model":"provider/model","usage":{"inputTokens":100,"outputTokens":20,"cachedInputTokens":40,"reasoningOutputTokens":5}}}]`)
	turns := scan(t, newCodebuffAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns: %+v", turns)
	}
	u := turns[0].Usage
	if u.Input != 60 || u.CacheRead != 40 || u.Output != 20 || u.Reasoning != 5 || u.Total() != 120 {
		t.Fatalf("usage: %+v", u)
	}
}
