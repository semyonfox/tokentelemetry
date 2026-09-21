package ingest

import (
	"path/filepath"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestKimiCodeCorrelatesUsageWithActualRequestModel(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "sessions", "wd_repo_123", "session_s1")
	writeFile(t, filepath.Join(session, "state.json"), `{"workDir":"/work/repo"}`)
	writeFile(t, filepath.Join(session, "agents", "main", "wire.jsonl"),
		`{"type":"llm.request","agentId":"main","time":1788256800000,"turnStep":"7.2","model":"gpt-5.6","modelAlias":"fast"}`,
		`{"type":"usage.record","agentId":"main","usageScope":"turn","time":1788256801000,"model":"fast","usage":{"inputOther":100,"output":20,"inputCacheRead":700,"inputCacheCreation":50}}`)
	turns := scan(t, newKimiCodeAt(root))
	want := model.Usage{Input: 100, Output: 20, CacheRead: 700, CacheWrite: 50, ContextTokens: 850}
	if len(turns) != 1 || turns[0].Usage != want || turns[0].Model != "gpt-5.6" || turns[0].Project != "/work/repo" {
		t.Fatalf("turns = %+v, want usage %+v", turns, want)
	}
}

func TestKimiCodeSkipsUnpairedMirroredUsageAndKeepsSubagentRequest(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "sessions", "wd_repo_123", "session_s1")
	writeFile(t, filepath.Join(session, "agents", "main", "wire.jsonl"),
		`{"type":"usage.record","agentId":"main","time":1788256801000,"model":"fast","usage":{"inputOther":100,"output":20}}`)
	writeFile(t, filepath.Join(session, "agents", "agent-1", "wire.jsonl"),
		`{"type":"llm.request","agentId":"agent-1","time":1788256800000,"turnStep":"7.2","model":"kimi-k2"}`,
		`{"type":"usage.record","agentId":"other-agent","usageScope":"session","time":1788256800500,"usage":{"inputOther":999,"output":999}}`,
		`{"type":"usage.record","agentId":"agent-1","usageScope":"turn","time":1788256801000,"usage":{"inputOther":10,"output":2,"inputCacheRead":3,"inputCacheCreation":1}}`)
	turns := scan(t, newKimiCodeAt(root))
	if len(turns) != 1 || !turns[0].Subagent || turns[0].Usage.Total() != 16 {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestKimiCodeMirroredChildEventsCannotConsumeParentRequest(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "sessions", "workspace", "session_parent")
	writeFile(t, filepath.Join(session, "agents", "main", "wire.jsonl"),
		`{"type":"llm.request","agentId":"main","time":1788256800000,"model":"parent-model","modelAlias":"parent"}`,
		`{"type":"llm.request","agentId":"child-1","time":1788256800100,"model":"child-model","modelAlias":"child"}`,
		`{"type":"usage.record","agentId":"child-1","usageScope":"turn","time":1788256800200,"model":"child","usage":{"inputOther":90,"output":9,"inputCacheRead":0,"inputCacheCreation":0}}`,
		`{"type":"usage.record","agentId":"main","usageScope":"turn","time":1788256800300,"model":"parent","usage":{"inputOther":10,"output":2,"inputCacheRead":3,"inputCacheCreation":1}}`)
	turns := scan(t, newKimiCodeAt(root))
	if len(turns) != 1 || turns[0].Model != "parent-model" || turns[0].Usage.Total() != 16 {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestKimiCodeSessionScopeIsAPerGenerationRecord(t *testing.T) {
	root := t.TempDir()
	session := filepath.Join(root, "sessions", "workspace", "session_compaction")
	writeFile(t, filepath.Join(session, "agents", "main", "wire.jsonl"),
		`{"type":"llm.request","agentId":"main","kind":"compaction","time":1788256800000,"model":"compact-model","modelAlias":"compact"}`,
		`{"type":"usage.record","agentId":"main","usageScope":"session","time":1788256801000,"model":"compact","usage":{"inputOther":40,"output":8,"inputCacheRead":5,"inputCacheCreation":2}}`)
	turns := scan(t, newKimiCodeAt(root))
	if len(turns) != 1 || turns[0].Model != "compact-model" || turns[0].Usage.Total() != 55 {
		t.Fatalf("turns = %+v", turns)
	}
}
