package ingest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestCursorCSVSnapshotPreservesDuplicateRowsAndCacheBuckets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "usage.csv")
	writeFile(t, p, "Date,Kind,Model,Max Mode,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens,Cost", `2026-02-17T18:48:04.421Z,Included,claude-4.6,No,970,3,21440,785,23198,0.04`, `2026-02-17T18:48:04.421Z,Included,claude-4.6,No,970,3,21440,785,23198,0.04`)
	turns := scan(t, newCursorAt(p))
	want := model.Usage{Input: 3, Output: 785, CacheRead: 21440, CacheWrite: 970, ContextTokens: 22413}
	if len(turns) != 2 || turns[0].Key == turns[1].Key || turns[0].Usage != want {
		t.Fatalf("turns=%+v", turns)
	}
}

func TestCursorCSVAcceptsAggregateTotalBeyondPerCallLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "usage.csv")
	writeFile(t, p, "Date,Model,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens", `2026-02-17T18:48:04Z,auto,0,60000000,0,1,60000001`)
	turns := scan(t, newCursorAt(p))
	if len(turns) != 1 || turns[0].Usage.Total() != 60_000_001 {
		t.Fatalf("turns=%+v", turns)
	}
}

func TestCursorCSVRejectsOversizedCountBeforeSumming(t *testing.T) {
	p := filepath.Join(t.TempDir(), "usage.csv")
	writeFile(t, p, "Date,Model,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens", `2026-02-17T18:48:04Z,auto,9223372036854775807,1,1,1,4`)
	var turns []model.Turn
	err := newCursorAt(p).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || !strings.Contains(err.Error(), "implausible token count") || len(turns) != 0 {
		t.Fatalf("turns=%+v error=%v", turns, err)
	}
}
func TestCursorCSVMarksIrreconcilableTotalUnclassified(t *testing.T) {
	p := filepath.Join(t.TempDir(), "usage.csv")
	writeFile(t, p, "Date,Model,Input (w/ Cache Write),Input (w/o Cache Write),Cache Read,Output Tokens,Total Tokens", `2026-02-17T18:48:04Z,auto,1,2,3,4,99`)
	turns := scan(t, newCursorAt(p))
	if len(turns) != 1 || turns[0].Usage.Unclassified != 99 || turns[0].UnpricedReason == "" {
		t.Fatalf("turns=%+v", turns)
	}
}
