package ingest

import (
	"path/filepath"
	"testing"
)

func TestOpenCodeModernRowsMaskFrozenLegacyAndKeepOldHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	db := openKiloFixture(t, path, true, true)
	insertKiloCurrent(t, db, "new", "ses_child", "assistant", 1788256800000, `{"model":{"id":"m","providerID":"openai"},"tokens":{"input":100,"output":20,"cache":{"read":700,"write":50}}}`)
	insertKiloLegacy(t, db, "new", "ses_child", 1788256800000, `{"role":"assistant","modelID":"old","tokens":{"input":999,"output":999}}`)
	insertKiloLegacy(t, db, "old", "ses_main", 1788256800000, `{"role":"assistant","modelID":"old","tokens":{"input":10,"output":5}}`)
	turns := scan(t, &OpenCode{dbPath: path})
	if len(turns) != 2 {
		t.Fatal(turns)
	}
	var input, output int64
	var children int
	for _, turn := range turns {
		input += turn.Usage.Input
		output += turn.Usage.Output
		if turn.Subagent {
			children++
		}
		if string(turn.Agent) != "opencode" {
			t.Fatal(turn)
		}
	}
	if input != 110 || output != 25 || children != 1 {
		t.Fatalf("modern/legacy reconciliation failed: %d %d %d", input, output, children)
	}
	// The intermediate v2 schema has separate metadata and no legacy records.
	if _, err := db.Exec(`ALTER TABLE session RENAME TO session_v2; DROP TABLE message`); err != nil {
		t.Fatal(err)
	}
	turns = scan(t, &OpenCode{dbPath: path})
	if len(turns) != 1 || turns[0].Project != "/work/child" {
		t.Fatal(turns)
	}
}
