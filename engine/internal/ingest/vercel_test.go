package ingest

import (
	"context"
	"github.com/semyonfox/tokentelemetry/engine/internal/model"
	"path/filepath"
	"testing"
)

func TestVercelSnapshotUsesActualSingleGroupingContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	writeFile(t, path, `{"results":[{"model":"anthropic/claude-sonnet-4","input_tokens":1000,"output_tokens":50,"cached_input_tokens":700,"total_cost":123}]}`)
	turns := scan(t, &VercelGateway{path: path})
	if len(turns) != 1 || turns[0].Usage.Total() != 1050 || turns[0].Usage.Input != 0 || turns[0].UnpricedReason == "" || !turns[0].Timestamp.IsZero() {
		t.Fatalf("invented decomposition/date: %+v", turns)
	}
	// day+model is not the documented multi-dimensional response shape.
	writeFile(t, path, `{"results":[{"day":"2026-09-01","model":"m","input_tokens":1000,"output_tokens":50}]}`)
	var emitted int
	err := (&VercelGateway{path: path}).Scan(context.Background(), func(model.Turn) { emitted++ })
	if err == nil || emitted != 0 {
		t.Fatalf("unsupported grouping accepted: %v %d", err, emitted)
	}
}

func TestVercelRejectsMixedSnapshotsAndOverflowWithoutPartialTotals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	for _, raw := range []string{
		`{"results":[{"day":"2026-09-01","input_tokens":100,"output_tokens":10},{"model":"m","input_tokens":100,"output_tokens":10}]}`,
		`{"results":[{"model":"good","input_tokens":100,"output_tokens":10},{"model":"bad","input_tokens":9223372036854775807,"output_tokens":10}]}`,
	} {
		writeFile(t, path, raw)
		emitted := 0
		err := (&VercelGateway{path: path}).Scan(context.Background(), func(model.Turn) { emitted++ })
		if err == nil || emitted != 0 {
			t.Fatalf("partial/overflow report accepted: %v %d", err, emitted)
		}
	}
}
