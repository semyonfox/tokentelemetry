package ingest

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenClaudeUsesActualModelAndNativeIDAcrossArchiveCopies(t *testing.T) {
	root := t.TempDir()
	line := `{"type":"assistant","sessionId":"session","timestamp":"2026-09-01T12:00:00Z","uuid":"line-id","cwd":"/work/repo","isSidechain":true,"actualModel":"router/model-real","provider":"router","endpoint":"https://router.invalid/v1","message":{"id":"message-id","model":"requested-model","usage":{"input_tokens":10,"output_tokens":2,"cache_creation_input_tokens":3,"cache_read_input_tokens":4}}}`
	writeFile(t, filepath.Join(root, "project-a", "session.jsonl"), line)
	writeFile(t, filepath.Join(root, "archive", "copy.jsonl"), line)
	result, err := Run(context.Background(), []Scanner{newOpenClaudeAt(root)})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan: %v %v", err, result.Errors)
	}
	if len(result.Turns) != 1 || result.Duplicates != 1 {
		t.Fatalf("dedup failed: %+v", result)
	}
	turn := result.Turns[0]
	if turn.Model != "router/model-real" || turn.Provider != "router" || turn.Endpoint != "https://router.invalid/v1" || turn.Project != "/work/repo" || !turn.Subagent {
		t.Fatalf("metadata: %+v", turn)
	}
	if turn.Usage.Total() != 19 || turn.Usage.ContextTokens != 17 {
		t.Fatalf("usage: %+v", turn.Usage)
	}
}

func TestOpenClaudeUnknownModelIsUnpriced(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "p", "s.jsonl"), `{"type":"assistant","timestamp":"2026-09-01T12:00:00Z","message":{"usage":{"input_tokens":1}}}`)
	turns := scan(t, newOpenClaudeAt(root))
	if len(turns) != 1 || turns[0].Model != "unknown" || turns[0].UnpricedReason == "" {
		t.Fatalf("turns: %+v", turns)
	}
}
