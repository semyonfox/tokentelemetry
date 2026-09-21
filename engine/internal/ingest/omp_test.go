package ingest

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOMPReadsMessagesAndModelUsageAndDedupsForkCopies(t *testing.T) {
	root := t.TempDir()
	header := `{"type":"session","version":3,"id":"parent","timestamp":"2026-09-01T00:00:00Z","cwd":"/real/project"}`
	call := `{"type":"message","id":"entry-1","parentId":null,"timestamp":"2026-09-01T00:01:00Z","message":{"role":"assistant","provider":"openrouter","model":"author/model","responseId":"response-1","usage":{"input":10,"output":2,"cacheRead":3,"cacheWrite":4}}}`
	writeFile(t, filepath.Join(root, "-project", "parent.jsonl"), header, call, `{"type":"model_usage","id":"entry-2","parentId":"entry-1","timestamp":"2026-09-01T00:02:00Z","provider":"anthropic","model":"claude-x","usage":{"input":5,"output":1,"cacheRead":0,"cacheWrite":0}}`)
	writeFile(t, filepath.Join(root, "-project", "agents", "child.jsonl"), `{"type":"session","version":3,"id":"child","timestamp":"2026-09-01T00:03:00Z","cwd":"/real/project","parentSession":"parent"}`, call)
	result, err := Run(context.Background(), []Scanner{newOMPAt(root)})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan: %v %v", err, result.Errors)
	}
	if len(result.Turns) != 2 || result.Duplicates != 1 {
		t.Fatalf("result: %+v", result)
	}
	if result.Turns[0].Project != "/real/project" || result.Turns[0].Usage.Total() != 19 {
		t.Fatalf("message: %+v", result.Turns[0])
	}
	if result.Turns[1].Model != "claude-x" || result.Turns[1].Usage.Total() != 6 {
		t.Fatalf("model usage: %+v", result.Turns[1])
	}
}
