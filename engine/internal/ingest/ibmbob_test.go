package ingest

import (
	"context"
	"testing"
)

func TestIBMBobPreservesBucketsButLeavesTaskWideModelUnpriced(t *testing.T) {
	root := t.TempDir()
	writeLegacyClineTask(t, root, "task", legacyMessage("1788307200000", `{"tokensIn":10,"tokensOut":2,"cacheReads":3,"cacheWrites":4}`))
	writeLegacyClineHistory(t, root, "task", "<model>provider/latest-model</model>\nCurrent Workspace Directory (/work/repo)")
	turns := scan(t, newIBMBobAt(root))
	if len(turns) != 1 {
		t.Fatalf("turns: %+v", turns)
	}
	turn := turns[0]
	if turn.Model != "provider/latest-model" || turn.UnpricedReason == "" || turn.Usage.Total() != 19 {
		t.Fatalf("turn: %+v", turn)
	}
}

func TestIBMBobDedupsCopiedTaskStores(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	line := legacyMessage("1788307200000", `{"tokensIn":10,"tokensOut":2}`)
	writeLegacyClineTask(t, a, "task", line)
	writeLegacyClineTask(t, b, "task", line)
	result, err := Run(context.Background(), []Scanner{newIBMBobAt(a, b)})
	if err != nil || len(result.Turns) != 1 || result.Duplicates != 1 {
		t.Fatalf("result: %+v err=%v", result, err)
	}
}
