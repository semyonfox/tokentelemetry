package ingest

import (
	"context"
	"path/filepath"
	"testing"
)

// the usage block is Anthropic's own compaction example: the top-level counts
// match the message step and leave the compaction step out
const claudeCompactedCall = `{"type":"assistant","timestamp":"2026-10-01T10:00:00Z","requestId":"req_1","sessionId":"s","message":{"id":"msg_1","model":"claude-opus-5","usage":{"input_tokens":23000,"output_tokens":1000,"speed":"fast","iterations":[{"type":"compaction","input_tokens":180000,"output_tokens":3500},{"type":"message","input_tokens":23000,"output_tokens":1000}]}}}`

func TestClaudeCountsCompactionStepsAndFastMode(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "projects", "-proj")
	writeFile(t, filepath.Join(proj, "a.jsonl"), claudeCompactedCall)
	// a resumed transcript replays the call, compaction step included
	writeFile(t, filepath.Join(proj, "b.jsonl"), claudeCompactedCall)

	res, err := Run(context.Background(), []Scanner{newClaudeAt(root)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Turns) != 2 || res.Duplicates != 2 {
		t.Fatalf("want 2 turns and 2 duplicates, got %d and %d: %+v", len(res.Turns), res.Duplicates, res.Turns)
	}
	var input, output int64
	for _, turn := range res.Turns {
		input += turn.Usage.Input
		output += turn.Usage.Output
		if turn.Model != "claude-opus-5-fast" {
			t.Fatalf("fast mode must price at the -fast rates: %q", turn.Model)
		}
	}
	// 180000 + 23000 and 3500 + 1000, the documented billed totals
	if input != 203000 || output != 4500 {
		t.Fatalf("billed input %d output %d", input, output)
	}
}

func TestClaudeCountsCompactionOnlyCall(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "projects", "-proj", "a.jsonl"),
		`{"type":"assistant","timestamp":"2026-10-01T10:00:00Z","requestId":"req_1","sessionId":"s","message":{"id":"msg_1","model":"claude-opus-5","usage":{"input_tokens":0,"output_tokens":0,"iterations":[{"type":"compaction","input_tokens":144,"output_tokens":276}]}}}`)
	res, err := Run(context.Background(), []Scanner{newClaudeAt(root)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Turns) != 1 || res.Turns[0].Usage.Input != 144 || res.Turns[0].Usage.Output != 276 {
		t.Fatalf("compaction-only call: %+v", res.Turns)
	}
}

func TestClaudeFastModeSurvivesSnapshotOrder(t *testing.T) {
	root := t.TempDir()
	early := `{"type":"assistant","timestamp":"2026-10-01T10:00:00Z","requestId":"req_1","sessionId":"s","message":{"id":"msg_1","model":"claude-opus-5","usage":{"input_tokens":5,"output_tokens":1}}}`
	final := `{"type":"assistant","timestamp":"2026-10-01T10:00:02Z","requestId":"req_1","sessionId":"s","message":{"id":"msg_1","model":"claude-opus-5","usage":{"input_tokens":5,"output_tokens":90,"speed":"fast"}}}`
	writeFile(t, filepath.Join(root, "projects", "-proj", "a.jsonl"), early, final)
	res, err := Run(context.Background(), []Scanner{newClaudeAt(root)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Turns) != 1 || res.Turns[0].Model != "claude-opus-5-fast" || res.Turns[0].Usage.Output != 90 {
		t.Fatalf("snapshots: %+v", res.Turns)
	}
}
