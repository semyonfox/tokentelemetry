package ingest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestCursorSDKResultsReconcileAndDeduplicate(t *testing.T) {
	root := t.TempDir()
	row := `{"id":"run-1","status":"finished","model":{"id":"m"},"usage":{"inputTokens":100,"outputTokens":50,"cacheReadTokens":700,"cacheWriteTokens":200,"totalTokens":1050,"reasoningTokens":20}}`
	writeFile(t, filepath.Join(root, "one.json"), row)
	writeFile(t, filepath.Join(root, "copy.json"), row)
	r, err := Run(context.Background(), []Scanner{&CursorAgent{root: root}})
	if err != nil || len(r.Errors) > 0 || len(r.Turns) != 1 || r.Duplicates != 1 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	u := r.Turns[0]
	if u.Usage.Input != 100 || u.Usage.Output != 50 || u.Usage.Reasoning != 20 || u.UnpricedReason == "" || !u.Timestamp.IsZero() {
		t.Fatalf("SDK net buckets or missing time mishandled: %+v", u)
	}
	if Selected(&CursorAgent{}, nil) || !Selected(&CursorAgent{}, []string{"cursor-agent"}) || Selected(&VercelGateway{}, nil) {
		t.Fatal("overlapping imports must require selection")
	}
}

func TestCursorSDKRejectsDoubleCountedReasoning(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "bad.json"), `{"id":"run-1","status":"finished","usage":{"inputTokens":100,"outputTokens":50,"totalTokens":170,"reasoningTokens":20}}`)
	var n int
	err := (&CursorAgent{root: root}).Scan(context.Background(), func(model.Turn) { n++ })
	if err == nil || n != 0 {
		t.Fatalf("nonreconciling total accepted: %d %v", n, err)
	}
}
