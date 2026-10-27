package ingest

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

const ampFixture = `{
  "id": "T-thread-a",
  "messages": [
    {"role": "user", "messageId": 0},
    {"role": "assistant", "messageId": 1, "usage": {
      "model": "claude-haiku-4-5-20251001",
      "inputTokens": 10, "outputTokens": 178,
      "cacheCreationInputTokens": 986, "cacheReadInputTokens": 11372,
      "totalInputTokens": 12368, "timestamp": "2026-01-19T11:42:10.652Z"}},
    {"role": "assistant", "messageId": 2, "usage": {
      "model": "claude-haiku-4-5-20251001", "totalTokens": 345,
      "timestamp": "2026-01-19T11:43:00Z"}},
    "garbage"
  ],
  "usageLedger": {"events": [
    {"id": "event-1", "timestamp": "2026-01-19T11:42:10Z", "model": "claude-haiku-4-5-20251001",
     "tokens": {"input": 12368, "output": 178}, "toMessageId": 1, "credits": 0.25},
    {"id": "event-2", "timestamp": "2026-01-19T11:44:00Z", "model": "gpt-5",
     "tokens": {"input": 400, "output": 50}, "credits": 0.5}
  ]}
}`

func TestAmpCountsMessagesOnceAndKeepsUnmatchedLedgerCalls(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "threads", "T-thread-a.json"), ampFixture)
	// an archive copy of the same thread must not count twice
	archive := t.TempDir()
	writeFile(t, filepath.Join(archive, "threads", "nested", "T-thread-a.json"), ampFixture)

	result, err := Run(context.Background(), []Scanner{newAmpAt(root, archive, root)})
	if err != nil || len(result.Errors) != 0 {
		t.Fatalf("scan: %v %v", err, result.Errors)
	}
	if len(result.Turns) != 3 || result.Duplicates != 3 {
		t.Fatalf("want 3 turns and 3 duplicates, got %d and %d: %+v", len(result.Turns), result.Duplicates, result.Turns)
	}

	measured := result.Turns[0]
	want := model.Usage{Input: 10, Output: 178, CacheWrite: 986, CacheRead: 11372, ContextTokens: 12368}
	if measured.Usage != want || measured.Model != "claude-haiku-4-5-20251001" || measured.SessionID != "T-thread-a" {
		t.Fatalf("measured message: %+v", measured)
	}
	// the matched ledger event adds credits and no tokens
	if measured.Credits == nil || *measured.Credits != 0.25 {
		t.Fatalf("matched ledger credits: %+v", measured.Credits)
	}
	if !measured.Timestamp.Equal(time.Date(2026, 1, 19, 11, 42, 10, 652e6, time.UTC)) {
		t.Fatalf("timestamp: %v", measured.Timestamp)
	}

	totalOnly := result.Turns[1]
	if totalOnly.Usage != (model.Usage{Unclassified: 345}) {
		t.Fatalf("a bare total must stay unclassified: %+v", totalOnly.Usage)
	}

	ledger := result.Turns[2]
	if ledger.Model != "gpt-5" || ledger.Usage != (model.Usage{Input: 400, Output: 50}) || *ledger.Credits != 0.5 {
		t.Fatalf("unmatched ledger event: %+v", ledger)
	}

	// 10+178+986+11372 + 345 + 400+50, recomputed by hand
	var total int64
	for _, turn := range result.Turns {
		total += turn.Usage.Total()
	}
	if total != 13341 {
		t.Fatalf("total tokens: %d", total)
	}
}

func TestAmpReadsDataDirOverride(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "threads", "T-b.json"), `{"messages":[{"role":"assistant","messageId":"m1","timestamp":1768822930652,"usage":{"model":"claude-x","inputTokens":1,"outputTokens":2}}]}`)
	t.Setenv("AMP_DATA_DIR", " , "+root)
	turns := scan(t, NewAmp())
	if len(turns) != 1 || turns[0].SessionID != "T-b" || turns[0].Usage.Total() != 3 {
		t.Fatalf("turns: %+v", turns)
	}
	if !turns[0].Timestamp.Equal(time.UnixMilli(1768822930652)) {
		t.Fatalf("millisecond timestamp: %v", turns[0].Timestamp)
	}
}
