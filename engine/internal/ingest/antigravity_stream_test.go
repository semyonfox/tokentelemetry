package ingest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestAntigravityStreamResultsDoNotAddStepsOrCopiedExports(t *testing.T) {
	root := t.TempDir()
	rows := []string{
		`{"event":"init","conversation_id":"c","init":{"cwd":"/work"}}`,
		`{"event":"step_update","step_update":{"conversation_id":"c","step_index":2,"state":"DONE","usage":{"input_tokens":278,"output_tokens":4,"cache_read_tokens":30214}}}`,
		`{"event":"result","result":{"conversation_id":"c","num_turns":2,"usage":{"input_tokens":278,"output_tokens":4,"cache_read_tokens":30214}}}`,
	}
	writeFile(t, filepath.Join(root, "capture.jsonl"), rows...)
	writeFile(t, filepath.Join(root, "copy.jsonl"), rows...)
	r, err := Run(context.Background(), []Scanner{&Antigravity{streamRoot: root}})
	if err != nil || len(r.Errors) > 0 || len(r.Turns) != 1 || r.Duplicates != 1 {
		t.Fatalf("result=%+v err=%v", r, err)
	}
	turn := r.Turns[0]
	if turn.Usage.Total() != 30496 || !turn.Aggregate || !turn.Timestamp.IsZero() || turn.Model != "unknown" || turn.Project != "/work" {
		t.Fatalf("incorrect result: %+v", turn)
	}
}

func TestAntigravityStreamCumulativeResultsAndConflictingCopies(t *testing.T) {
	root := t.TempDir()
	first := `{"event":"result","result":{"conversation_id":"c","num_turns":1,"usage":{"input_tokens":30384,"output_tokens":4}}}`
	second := `{"event":"result","result":{"conversation_id":"c","num_turns":2,"usage":{"input_tokens":30662,"output_tokens":8,"cache_read_tokens":30214}}}`
	writeFile(t, filepath.Join(root, "full.jsonl"), first, second)
	writeFile(t, filepath.Join(root, "copy.jsonl"), second)
	r, err := Run(context.Background(), []Scanner{&Antigravity{streamRoot: root}})
	if err != nil || len(r.Errors) > 0 || len(r.Turns) != 1 || r.Turns[0].Usage.Total() != 60884 {
		t.Fatalf("cumulative turns added twice: %+v %v", r, err)
	}

	writeFile(t, filepath.Join(root, "conflict.jsonl"), `{"event":"result","result":{"conversation_id":"c","num_turns":2,"usage":{"input_tokens":40000,"output_tokens":1}}}`)
	var turns []model.Turn
	err = (&Antigravity{streamRoot: root}).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || len(turns) != 0 {
		t.Fatalf("conflicting snapshots guessed: %+v %v", turns, err)
	}
}

func TestAntigravityExplicitStreamDirectoryReplacesNativeDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	nativeRoot := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	writeAntigravityDB(t, nativeRoot, "native", antigravityTestBlob(antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "native", seconds: 1_800_000_100, input: 100, output: 10,
	}))
	streamRoot := t.TempDir()
	writeFile(t, filepath.Join(streamRoot, "capture.jsonl"), `{"event":"result","result":{"conversation_id":"stream","num_turns":1,"usage":{"input_tokens":5,"output_tokens":2}}}`)
	t.Setenv("TT_ANTIGRAVITY_DIR", streamRoot)

	scanner := NewAntigravity()
	if roots := scanner.Roots(); len(roots) != 1 || roots[0] != streamRoot {
		t.Fatalf("roots = %#v, want explicit stream root", roots)
	}
	result, err := Run(context.Background(), []Scanner{scanner})
	if err != nil || len(result.Errors) != 0 || len(result.Turns) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if turn := result.Turns[0]; turn.SessionID != "stream" || turn.Usage.Total() != 7 || !turn.Aggregate {
		t.Fatalf("explicit import did not replace native source: %+v", turn)
	}
}

func TestAntigravityInvalidExplicitStreamDirectoryDoesNotFallBackToNative(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	nativeRoot := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	writeAntigravityDB(t, nativeRoot, "native", antigravityTestBlob(antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "native", seconds: 1_800_000_101, input: 100, output: 10,
	}))
	t.Setenv("TT_ANTIGRAVITY_DIR", filepath.Join(home, "missing"))

	scanner := NewAntigravity()
	if len(scanner.Roots()) != 0 {
		t.Fatalf("invalid explicit import unexpectedly fell back to native roots: %#v", scanner.Roots())
	}
}
