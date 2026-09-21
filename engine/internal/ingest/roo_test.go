package ingest

import "testing"

func TestRooCurrentGrossAndLegacyNetInputContracts(t *testing.T) {
	root := t.TempDir()
	writeLegacyClineTask(t, root, "task",
		legacyMessage("1788307200000", `{"apiProtocol":"anthropic","tokensIn":1000,"tokensOut":40,"cacheReads":600,"cacheWrites":100}`),
		legacyMessage("1788307260000", `{"tokensIn":300,"tokensOut":20,"cacheReads":600,"cacheWrites":100}`),
	)
	turns := scan(t, newRooCodeAt(root))
	if len(turns) != 2 {
		t.Fatalf("turns = %d: %+v", len(turns), turns)
	}
	if turns[0].Usage.Input != 300 || turns[0].Usage.Total() != 1040 || turns[0].Usage.ContextTokens != 1000 {
		t.Fatalf("current Roo gross input not normalized: %+v", turns[0].Usage)
	}
	if turns[1].Usage.Input != 300 || turns[1].Usage.Total() != 1020 || turns[1].Usage.ContextTokens != 1000 {
		t.Fatalf("legacy Roo disjoint input changed: %+v", turns[1].Usage)
	}
	if turns[0].Agent != "roo-code" || turns[0].Key != "roo-code|task|0" {
		t.Fatalf("source identity lost: %+v", turns[0])
	}
}

func TestRooClampsCacheOverlapAndRejectsCorruptCounts(t *testing.T) {
	root := t.TempDir()
	writeLegacyClineTask(t, root, "task",
		legacyMessage("1788307200000", `{"apiProtocol":"openai","tokensIn":100,"tokensOut":10,"cacheReads":120,"cacheWrites":20}`),
		legacyMessage("1788307260000", `{"apiProtocol":"openai","tokensIn":100,"tokensOut":60000000}`),
	)
	turns := scan(t, newRooCodeAt(root))
	if len(turns) != 1 || turns[0].Usage.Input != 0 || turns[0].Usage.Total() != 150 {
		t.Fatalf("unexpected normalization: %+v", turns)
	}
}
