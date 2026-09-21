package ingest

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMuxAggregatesAndIndependentToolModels(t *testing.T) {
	root, copyRoot := t.TempDir(), t.TempDir()
	row := `{"id":"a","role":"assistant","metadata":{"model":"anthropic:claude-sonnet-4","timestamp":1788256800000,"usage":{"inputTokens":1000,"outputTokens":90,"cachedInputTokens":700,"reasoningTokens":20},"providerMetadata":{"anthropic":{"cacheCreationInputTokens":100}},"toolModelUsages":[{"toolCallId":"t","model":"openai:gpt-5","usage":{"inputTokens":50,"outputTokens":10}}]}}`
	for _, r := range []string{root, copyRoot} {
		writeFile(t, filepath.Join(r, "sessions", "ws", "chat.jsonl"), row)
	}
	writeFile(t, filepath.Join(root, "config.json"), `{"projects":[["/work/project",{"workspaces":[{"id":"ws"}]}]]}`)
	writeFile(t, filepath.Join(root, "sessions", "ws", "subagent-transcripts", "child", "chat.jsonl"), `{"id":"b","role":"assistant","metadata":{"model":"openai:gpt-5","usage":{"inputTokens":30,"outputTokens":5}}}`)
	// Pre-aggregated sidecars describe the same work and must be ignored.
	writeFile(t, filepath.Join(root, "sessions", "ws", "session-usage.json"), `{"inputTokens":999999}`)
	r, err := Run(context.Background(), []Scanner{&Mux{roots: []string{root, copyRoot}}})
	if err != nil || len(r.Errors) > 0 || len(r.Turns) != 3 || r.Duplicates != 2 {
		t.Fatalf("result=%+v error=%v", r, err)
	}
	var input, output, read, write int64
	var children int
	for _, turn := range r.Turns {
		input += turn.Usage.Input
		output += turn.Usage.Output
		read += turn.Usage.CacheRead
		write += turn.Usage.CacheWrite
		if !turn.Aggregate {
			t.Fatal("multi-step usage presented as a request")
		}
		if turn.Subagent {
			children++
		}
	}
	if input != 280 || output != 105 || read != 700 || write != 100 || children != 1 {
		t.Fatalf("incorrect decomposition: %d %d %d %d children=%d", input, output, read, write, children)
	}
}
