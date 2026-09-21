package ingest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestCodeWhalePreservesAggregateWithoutInventingSplit(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	row := `{"metadata":{"id":"s1","created_at":"2026-09-01T10:00:00Z","total_tokens":1234,"model":"deepseek-v3","workspace":"/work/repo","cost":{"session_cost_usd":1.2}}}`
	writeFile(t, filepath.Join(a, "s.json"), row)
	writeFile(t, filepath.Join(b, "copy.json"), row)
	turns := scan(t, newCodeWhaleAt(a, b))
	if len(turns) != 1 || turns[0].Usage.Unclassified != 1234 || !turns[0].Aggregate || turns[0].UnpricedReason == "" {
		t.Fatalf("turns=%+v", turns)
	}
}

func TestCodeWhaleRejectsOversizedAggregate(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "huge.json"), `{"metadata":{"id":"huge","created_at":"2026-09-01T10:00:00Z","total_tokens":9223372036854775807}}`)
	var turns []model.Turn
	err := newCodeWhaleAt(root).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || len(turns) != 0 {
		t.Fatalf("turns=%+v error=%v", turns, err)
	}
}

func TestCodeWhaleAcceptsAggregateBeyondPerCallLimit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "large.json"), `{"metadata":{"id":"large","created_at":"2026-09-01T10:00:00Z","total_tokens":60000000,"model":"deepseek-v3"}}`)
	turns := scan(t, newCodeWhaleAt(root))
	if len(turns) != 1 || turns[0].Usage.Unclassified != 60_000_000 {
		t.Fatalf("turns=%+v", turns)
	}
}
