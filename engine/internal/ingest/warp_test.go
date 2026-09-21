package ingest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func makeWarpDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "warp.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE agent_conversations (conversation_id TEXT, conversation_data TEXT, last_modified_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWarpPreservesConversationModelTotalsAsUnclassified(t *testing.T) {
	path := makeWarpDB(t)
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	data := `{"conversation_usage_metadata":{"token_usage":[` +
		`{"model_id":"GPT-5.3 Codex (medium reasoning)","warp_tokens":300,"byok_tokens":20},` +
		`{"model_id":"GPT-5.3 Codex (medium reasoning)","warp_tokens":10,"byok_tokens":0},` +
		`{"model_id":"Claude Sonnet 4.6","warp_tokens":90,"byok_tokens":5}]}}`
	if _, err := db.Exec(`INSERT INTO agent_conversations VALUES (?, ?, ?)`, "conv-1", data, "2026-05-18 10:10:00"); err != nil {
		t.Fatal(err)
	}

	turns := scan(t, newWarpAt(path))
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	got := map[string]int64{}
	for _, turn := range turns {
		got[turn.Model] = turn.Usage.Unclassified
		if !turn.Aggregate || turn.UnpricedReason == "" || turn.Agent != model.Agent("warp") {
			t.Fatalf("turn = %+v", turn)
		}
		if turn.Usage.Input != 0 || turn.Usage.Output != 0 {
			t.Fatalf("invented split: %+v", turn.Usage)
		}
	}
	if got["GPT-5.3 Codex (medium reasoning)"] != 330 || got["Claude Sonnet 4.6"] != 95 {
		t.Fatalf("totals = %+v", got)
	}
}

func TestWarpStableIdentityDeduplicatesRepeatedDatabaseRows(t *testing.T) {
	path := makeWarpDB(t)
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	data := `{"conversation_usage_metadata":{"token_usage":[{"model_id":"m","warp_tokens":12,"byok_tokens":3}]}}`
	for range 2 {
		if _, err := db.Exec(`INSERT INTO agent_conversations VALUES (?, ?, ?)`, "conv", data, "2026-05-18 10:10:00"); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Run(context.Background(), []Scanner{newWarpAt(path)})
	if err != nil || len(result.Turns) != 1 || result.Duplicates != 1 {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestWarpOpensDatabaseReadOnly(t *testing.T) {
	path := makeWarpDB(t)
	turns := scan(t, newWarpAt(path))
	if len(turns) != 0 {
		t.Fatalf("turns = %d", len(turns))
	}
}

func TestWarpRetainsMeasuredUsageWithoutTimestamp(t *testing.T) {
	path := makeWarpDB(t)
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	data := `{"conversation_usage_metadata":{"token_usage":[{"model_id":"m","warp_tokens":12,"byok_tokens":3}]}}`
	if _, err := db.Exec(`INSERT INTO agent_conversations VALUES (?, ?, NULL)`, "conv", data); err != nil {
		t.Fatal(err)
	}
	var turns []model.Turn
	err := newWarpAt(path).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || len(turns) != 1 || !turns[0].Timestamp.IsZero() || turns[0].Usage.Unclassified != 15 || turns[0].UnpricedReason == "" {
		t.Fatalf("err = %v, turns = %+v", err, turns)
	}
}
