package ingest

import (
	"context"
	"path/filepath"
	"testing"
)

func TestQuickdeskMeasuredOnlyAndDuplicateMultiplicity(t *testing.T) {
	root := t.TempDir()
	row := `{"Model":"m","session_id":"s","InputTokens":100,"OutputTokens":10,"_aws":{"Timestamp":1788256800000}}`
	writeFile(t, filepath.Join(root, "profiles.json"), `{"entries":[{"data_path":"copy"}]}`)
	for _, r := range []string{root, filepath.Join(root, "copy")} {
		writeFile(t, filepath.Join(r, "metrics", "metrics-2026-09-01.jsonl"), row, row, `{"ToolName":"read"}`, `{"Model":"m","InputTokens":999,"OutputTokens":2}`)
	}
	r, err := Run(context.Background(), []Scanner{&Quickdesk{root: root}})
	if err != nil || len(r.Errors) > 0 || len(r.Turns) != 2 || r.Duplicates != 2 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	for _, turn := range r.Turns {
		if turn.Usage.Input != 0 || turn.Usage.Unclassified != 100 || turn.UnpricedReason == "" {
			t.Fatalf("undocumented input priced: %+v", turn)
		}
	}
}
