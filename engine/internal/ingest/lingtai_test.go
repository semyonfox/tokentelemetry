package ingest

import (
	"context"
	"path/filepath"
	"testing"
)

func TestLingTaiUsesGrossInputContractAndParentDaemonMirrorOnly(t *testing.T) {
	root := t.TempDir()
	agent := filepath.Join(root, "agent-a")
	writeFile(t, filepath.Join(agent, ".agent.json"), `{"agent_id":"agent-id","nickname":"Project Agent","llm":{"model":"current-not-historical"}}`)
	writeFile(t, filepath.Join(agent, "logs", "token_ledger.jsonl"), `{"source":"main","ts":"2026-09-01T00:00:00Z","input":100,"output":20,"thinking":3,"cached":40,"model":"model-a","endpoint":"https://provider.invalid/v1"}`, `{"source":"daemon","em_id":"em-1","run_id":"run-1","ts":"2026-09-01T00:01:00Z","input":"50","output":"5","thinking":"2","cached":"10","model":"model-b"}`)
	writeFile(t, filepath.Join(agent, "daemons", "run-1", "logs", "token_ledger.jsonl"), `{"source":"daemon","run_id":"run-1","ts":"2026-09-01T00:01:00Z","input":50,"output":5,"thinking":2,"cached":10,"model":"model-b"}`)
	turns := scan(t, newLingTaiAt(root))
	if len(turns) != 2 {
		t.Fatalf("mirrored nested ledger counted: %+v", turns)
	}
	if turns[0].Usage.Input != 60 || turns[0].Usage.Output != 23 || turns[0].Usage.Reasoning != 3 || turns[0].Usage.Total() != 123 || turns[0].Project != "Project Agent" {
		t.Fatalf("main: %+v", turns[0])
	}
	if !turns[1].Subagent || turns[1].SessionID != "run-1" || turns[1].Usage.Total() != 57 {
		t.Fatalf("daemon: %+v", turns[1])
	}
}

func TestLingTaiDoesNotApplyCurrentManifestModelToHistory(t *testing.T) {
	root := t.TempDir()
	agent := filepath.Join(root, "a")
	writeFile(t, filepath.Join(agent, ".agent.json"), `{"agent_id":"a","llm":{"model":"current-model"}}`)
	writeFile(t, filepath.Join(agent, "logs", "token_ledger.jsonl"), `{"ts":"2026-09-01T00:00:00Z","input":1}`)
	turns := scan(t, newLingTaiAt(root))
	if len(turns) != 1 || turns[0].Model != "unknown" || turns[0].UnpricedReason == "" {
		t.Fatalf("turns: %+v", turns)
	}
}

func TestLingTaiDedupsCopiedLedgerRowsByAPICallID(t *testing.T) {
	base := t.TempDir()
	roots := []string{filepath.Join(base, "active"), filepath.Join(base, "archive")}
	line := `{"api_call_id":"api-native","ts":"2026-09-01T00:00:00Z","input":3,"model":"m"}`
	for _, root := range roots {
		writeFile(t, filepath.Join(root, "agent", "logs", "token_ledger.jsonl"), line)
	}
	result, err := Run(context.Background(), []Scanner{newLingTaiAt(roots...)})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan: %v %v", err, result.Errors)
	}
	if len(result.Turns) != 1 || result.Duplicates != 1 {
		t.Fatalf("dedup: %+v", result)
	}
}

func TestLingTaiDropsOversizedLedgerRowBeforeAddingThinking(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "agent", "logs", "token_ledger.jsonl")
	writeFile(t, path, `{"input":1,"output":9223372036854775807,"thinking":1,"model":"m"}`, `{"input":2,"model":"m"}`)
	turns := scan(t, newLingTaiAt(root))
	if len(turns) != 1 || turns[0].Usage.Input != 2 {
		t.Fatalf("turns: %+v", turns)
	}
}
