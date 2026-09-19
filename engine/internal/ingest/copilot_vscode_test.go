package ingest

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func collectCopilotVSCode(t *testing.T, roots ...string) ([]model.Turn, bool, error) {
	t.Helper()
	var turns []model.Turn
	found, err := scanCopilotVSCode(context.Background(), roots, func(turn model.Turn) {
		turns = append(turns, turn)
	})
	return turns, found, err
}

func copilotVSCodeRequest(sessionID, requestID, responseID, model string, timestamp, input, cached, output int64) string {
	return copilotVSCodeRequestForAgent(copilotVSCodeAgentHostCopilot, sessionID, requestID, responseID, model, timestamp, input, cached, output)
}

func copilotVSCodeRequestForAgent(agentID, sessionID, requestID, responseID, model string, timestamp, input, cached, output int64) string {
	return fmt.Sprintf(`{"requestId":%q,"responseId":%q,"responseTimestamp":%d,"agent":{"id":%q},"modelState":{"value":1,"completedAt":%d},"response":[],"modelTotals":[{"model":%q,"inputTokens":%d,"cachedTokens":%d,"outputTokens":%d}]}`,
		requestID, responseID, timestamp, agentID, timestamp+1, model, input, cached, output)
}

func copilotVSCodeSession(sessionID string, requests ...string) string {
	return fmt.Sprintf(`{"sessionId":%q,"requests":[%s]}`, sessionID, joinCopilotVSCodeRequests(requests))
}

func joinCopilotVSCodeRequests(requests []string) string {
	if len(requests) == 0 {
		return ""
	}
	joined := requests[0]
	for _, request := range requests[1:] {
		joined += "," + request
	}
	return joined
}

func TestCopilotVSCodeReplaysFinalMutationStateAndUsesModelTotals(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workspaceStorage", "workspace-a", "chatSessions", "session-a.jsonl")
	writeFile(t, path,
		`{"kind":0,"v":{"sessionId":"session-a","requests":[]}}`,
		`{"kind":2,"k":["requests"],"v":[{"requestId":"request-a","responseId":"response-a","responseTimestamp":1789812672123,"agent":{"id":"github.copilot.chat"},"modelState":{"value":0},"message":"private prompt","response":["private response"],"promptTokens":999999,"completionTokens":999999}]}`,
		`{"kind":1,"k":["requests",0,"modelTotals"],"v":[{"model":"claude-sonnet-4.6","inputTokens":1000,"cachedTokens":700,"outputTokens":80},{"model":"gpt-5","inputTokens":100,"cachedTokens":0,"outputTokens":20}]}`,
		`{"kind":1,"k":["requests",0,"modelState"],"v":{"value":1,"completedAt":1789812672999}}`,
	)

	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(turns) != 2 {
		t.Fatalf("found %v, turns = %+v, want two model totals", found, turns)
	}

	first := turns[0]
	if first.Key != identityKey("copilot-vscode-response", "request-a", "response-a", "claude-sonnet-4.6") || first.SessionID != "session-a" {
		t.Errorf("identity = %+v", first)
	}
	if first.Agent != model.AgentCopilot || first.Provider != "github-copilot" || first.Model != "claude-sonnet-4.6" || !first.Aggregate {
		t.Errorf("attribution = %+v", first)
	}
	if got, want := first.Usage, (model.Usage{Input: 300, CacheRead: 700, Output: 80}); got != want {
		t.Errorf("usage = %+v, want %+v", got, want)
	}
	if !first.Timestamp.Equal(time.UnixMilli(1789812672123).UTC()) {
		t.Errorf("timestamp = %s", first.Timestamp)
	}

	second := turns[1]
	if second.Model != "gpt-5" || second.Usage.Input != 100 || second.Usage.CacheRead != 0 || second.Usage.Output != 20 {
		t.Errorf("second turn = %+v", second)
	}
}

func TestCopilotVSCodeAppliesPushReplacementAndDelete(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workspaceStorage", "workspace", "chatSessions", "session.jsonl")
	old := copilotVSCodeRequest("session", "old-request", "old-response", "gpt-5", 1789812000000, 10, 0, 1)
	kept := copilotVSCodeRequest("session", "kept-request", "kept-response", "gpt-5", 1789812100000, 20, 5, 2)
	deleted := copilotVSCodeRequest("session", "deleted-request", "deleted-response", "gpt-5", 1789812200000, 30, 10, 3)
	writeFile(t, path,
		fmt.Sprintf(`{"kind":0,"v":{"sessionId":"session","requests":[%s]}}`, old),
		fmt.Sprintf(`{"kind":2,"k":["requests"],"i":0,"v":[%s]}`, kept),
		fmt.Sprintf(`{"kind":2,"k":["requests"],"v":[%s]}`, deleted),
		`{"kind":3,"k":["requests",1,"modelTotals"]}`,
	)

	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(turns) != 1 {
		t.Fatalf("found %v, turns = %+v, want only replacement request", found, turns)
	}
	turn := turns[0]
	if turn.Key != identityKey("copilot-vscode-response", "kept-request", "kept-response", "gpt-5") {
		t.Errorf("key = %q", turn.Key)
	}
	if got, want := turn.Usage, (model.Usage{Input: 15, CacheRead: 5, Output: 2}); got != want {
		t.Errorf("usage = %+v, want %+v", got, want)
	}
}

func TestCopilotVSCodeDiscoversFlatAndEmptyWindowSessionsAndPrefersLog(t *testing.T) {
	root := t.TempDir()
	chatSessions := filepath.Join(root, "workspaceStorage", "workspace", "chatSessions")
	writeFile(t, filepath.Join(chatSessions, "same.json"), copilotVSCodeSession("same", copilotVSCodeRequest("same", "same-request", "same-response", "gpt-5", 1789812000000, 10, 0, 99)))
	writeFile(t, filepath.Join(chatSessions, "same.jsonl"),
		fmt.Sprintf(`{"kind":0,"v":%s}`, copilotVSCodeSession("same", copilotVSCodeRequest("same", "same-request", "same-response", "gpt-5", 1789812000000, 10, 0, 3))),
	)
	writeFile(t, filepath.Join(root, "globalStorage", "emptyWindowChatSessions", "empty.json"),
		copilotVSCodeSession("empty", copilotVSCodeRequest("empty", "empty-request", "empty-response", "claude-sonnet-4.6", 1789812100000, 20, 10, 4)),
	)

	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(turns) != 2 {
		t.Fatalf("found %v, turns = %+v, want log and empty-window session", found, turns)
	}
	bySession := make(map[string]model.Turn, len(turns))
	for _, turn := range turns {
		bySession[turn.SessionID] = turn
	}
	if got := bySession["same"].Usage.Output; got != 3 {
		t.Errorf("preferred log output = %d, want 3", got)
	}
	if got := bySession["empty"].Usage; got != (model.Usage{Input: 10, CacheRead: 10, Output: 4}) {
		t.Errorf("empty-window usage = %+v", got)
	}
}

func TestCopilotVSCodeDiscoversLegacyAndTransferredSessionsAndHonorsFlatSetting(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "settings.json"),
		`{`,
		`  // VS Code settings are JSONC, not ordinary JSON.`,
		`  "unrelated": "https://example.invalid/not-a-comment",`,
		`  "chat.useLogSessionStorage": false,`,
		`}`,
	)
	chatSessions := filepath.Join(root, "workspaceStorage", "workspace", "chatSessions")
	writeFile(t, filepath.Join(chatSessions, "same.json"),
		copilotVSCodeSession("same", copilotVSCodeRequest("same", "same-request", "same-response", "gpt-5", 1789812000000, 10, 0, 9)),
	)
	writeFile(t, filepath.Join(chatSessions, "same.jsonl"),
		fmt.Sprintf(`{"kind":0,"v":%s}`, copilotVSCodeSession("same", copilotVSCodeRequest("same", "same-request", "same-response", "gpt-5", 1789812000000, 10, 0, 1))),
	)
	writeFile(t, filepath.Join(root, "workspaceStorage", "no-workspace", "chatSessions", "legacy.json"),
		copilotVSCodeSession("legacy", copilotVSCodeRequest("legacy", "legacy-request", "legacy-response", "gpt-5", 1789812100000, 20, 0, 2)),
	)
	writeFile(t, filepath.Join(root, "globalStorage", "transferredChatSessions", "transferred.json"),
		copilotVSCodeSession("transferred", copilotVSCodeRequest("transferred", "transferred-request", "transferred-response", "gpt-5", 1789812200000, 30, 0, 3)),
	)

	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(turns) != 3 {
		t.Fatalf("found %v, turns = %+v, want flat, legacy and transferred records", found, turns)
	}
	bySession := make(map[string]model.Turn, len(turns))
	for _, turn := range turns {
		bySession[turn.SessionID] = turn
	}
	if bySession["same"].Usage.Output != 9 || bySession["legacy"].Usage.Output != 2 || bySession["transferred"].Usage.Output != 3 {
		t.Fatalf("sessions = %+v", bySession)
	}
}

func TestCopilotVSCodeIncludesCancelledAndFailedUsage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workspaceStorage", "workspace", "chatSessions", "terminal.json")
	writeFile(t, path, `{"sessionId":"terminal","requests":[
  {"requestId":"cancelled","responseId":"cancelled-response","responseTimestamp":1789812000000,"agent":{"id":"github.copilot.chat"},"modelState":{"value":2},"response":[],"modelTotals":[{"model":"gpt-5","inputTokens":10,"cachedTokens":2,"outputTokens":3}]},
  {"requestId":"failed","responseId":"failed-response","responseTimestamp":1789812000001,"agent":{"id":"github.copilot.chat"},"modelState":{"value":3},"response":[],"modelTotals":[{"model":"gpt-5","inputTokens":20,"cachedTokens":4,"outputTokens":5}]},
  {"requestId":"needs-input","responseId":"needs-input-response","responseTimestamp":1789812000002,"agent":{"id":"github.copilot.chat"},"modelState":{"value":4},"response":[],"modelTotals":[{"model":"gpt-5","inputTokens":30,"cachedTokens":6,"outputTokens":7}]}
]}`)

	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(turns) != 2 {
		t.Fatalf("found %v, turns = %+v, want cancelled and failed records", found, turns)
	}
	if turns[0].Usage != (model.Usage{Input: 8, CacheRead: 2, Output: 3}) || turns[1].Usage != (model.Usage{Input: 16, CacheRead: 4, Output: 5}) {
		t.Fatalf("usage = %+v", turns)
	}
}

func TestCopilotVSCodeSkipsNonCopilotParticipant(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workspaceStorage", "workspace", "chatSessions", "participants.json")
	writeFile(t, path, copilotVSCodeSession("participants",
		copilotVSCodeRequestForAgent("example.extension.agent", "participants", "other-request", "other-response", "gpt-5", 1789812000000, 100, 0, 10),
		copilotVSCodeRequest("participants", "copilot-request", "copilot-response", "gpt-5", 1789812000001, 20, 5, 2),
	))

	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(turns) != 1 || turns[0].Key != identityKey("copilot-vscode-response", "copilot-request", "copilot-response", "gpt-5") {
		t.Fatalf("participant attribution = %#v", turns)
	}
}

func TestCopilotVSCodeRecognizesOnlyKnownCopilotParticipantIDs(t *testing.T) {
	for _, agentID := range []string{
		"github.copilot.chat",
		copilotVSCodeAgentHostCopilot,
	} {
		if !copilotVSCodeIsCopilotRequest(map[string]any{"agent": map[string]any{"id": agentID}}) {
			t.Errorf("Copilot agent ID %q was rejected", agentID)
		}
	}
	// The Copilot CLI Agent Host shares the native session-state journal, so
	// that scanner is its single source of truth rather than this VS Code copy.
	for _, agentID := range []string{"", "agent-host-other", "agent-host-copilotcli", "example.extension.agent"} {
		if copilotVSCodeIsCopilotRequest(map[string]any{"agent": map[string]any{"id": agentID}}) {
			t.Errorf("non-Copilot agent ID %q was accepted", agentID)
		}
	}
}

func TestCopilotVSCodeDeduplicatesCopiedSessionResponses(t *testing.T) {
	root := t.TempDir()
	copy := copilotVSCodeSession("copied-session", copilotVSCodeRequest("copied-session", "request", "response", "gpt-5", 1789812000000, 10, 0, 2))
	writeFile(t, filepath.Join(root, "workspaceStorage", "workspace-a", "chatSessions", "source.json"), copy)
	writeFile(t, filepath.Join(root, "globalStorage", "emptyWindowChatSessions", "copied.json"), copy)

	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(turns) != 1 {
		t.Fatalf("found %v, turns = %+v, want one copied response", found, turns)
	}
}

func TestCopilotVSCodeKeepsMoreCompleteMigratedCopy(t *testing.T) {
	root := t.TempDir()
	stale := copilotVSCodeSession("copied", copilotVSCodeRequest("copied", "request", "response", "gpt-5", 1789812000000, 10, 0, 1))
	updated := copilotVSCodeSession("copied", copilotVSCodeRequest("copied", "request", "response", "gpt-5", 1789812000000, 30, 10, 7))
	writeFile(t, filepath.Join(root, "globalStorage", "emptyWindowChatSessions", "stale.json"), stale)
	writeFile(t, filepath.Join(root, "workspaceStorage", "workspace", "chatSessions", "updated.json"), updated)

	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(turns) != 1 {
		t.Fatalf("found %v, turns = %+v, want one reconciled record", found, turns)
	}
	if got, want := turns[0].Usage, (model.Usage{Input: 20, CacheRead: 10, Output: 7}); got != want {
		t.Fatalf("usage = %+v, want %+v", got, want)
	}
}

func TestCopilotVSCodeSkipsIncompleteAmbiguousAndTopLevelCounters(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workspaceStorage", "workspace", "chatSessions", "mixed.json")
	writeFile(t, path, `{
  "sessionId":"mixed",
  "requests":[
    {"requestId":"pending","responseId":"pending-response","responseTimestamp":1789812000000,"agent":{"id":"github.copilot.chat"},"modelState":{"value":0},"response":[],"modelTotals":[{"model":"gpt-5","inputTokens":10,"cachedTokens":0,"outputTokens":1}]},
    {"requestId":"fallback","responseId":"fallback-response","responseTimestamp":1789812000000,"agent":{"id":"github.copilot.chat"},"modelState":{"value":1},"response":[],"promptTokens":900,"completionTokens":100},
    {"requestId":"bad-cache","responseId":"bad-cache-response","responseTimestamp":1789812000000,"agent":{"id":"github.copilot.chat"},"modelState":{"value":1},"response":[],"modelTotals":[{"model":"gpt-5","inputTokens":10,"cachedTokens":11,"outputTokens":1}]},
    {"requestId":"bad-time","responseId":"bad-time-response","responseTimestamp":123,"agent":{"id":"github.copilot.chat"},"modelState":{"value":1},"response":[],"modelTotals":[{"model":"gpt-5","inputTokens":10,"cachedTokens":0,"outputTokens":1}]},
    {"requestId":"good","responseId":"good-response","responseTimestamp":1789812000000,"agent":{"id":"github.copilot.chat"},"modelState":{"value":1},"response":[],"modelTotals":[{"model":"gpt-5","inputTokens":10,"cachedTokens":2,"outputTokens":3}]}
  ]
}`)

	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(turns) != 1 {
		t.Fatalf("found %v, turns = %+v, want only complete exact row", found, turns)
	}
	if turn := turns[0]; turn.Key != identityKey("copilot-vscode-response", "good", "good-response", "gpt-5") || turn.Usage != (model.Usage{Input: 8, CacheRead: 2, Output: 3}) {
		t.Errorf("turn = %+v", turn)
	}
}

func TestCopilotVSCodeAcceptsLegacyCompletedSnapshotWithoutModelState(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "globalStorage", "emptyWindowChatSessions", "legacy.json")
	writeFile(t, path, `{"sessionId":"legacy","requests":[{"requestId":"request","responseId":"response","responseTimestamp":"2026-09-19T10:11:12Z","agent":{"id":"github.copilot.chat"},"response":[],"modelTotals":[{"model":"claude-opus-4.6","inputTokens":40,"cachedTokens":30,"outputTokens":5}]}]}`)

	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(turns) != 1 {
		t.Fatalf("found %v, turns = %+v", found, turns)
	}
	if turns[0].Timestamp.Format(time.RFC3339) != "2026-09-19T10:11:12Z" || turns[0].Usage != (model.Usage{Input: 10, CacheRead: 30, Output: 5}) {
		t.Errorf("legacy turn = %+v", turns[0])
	}
}

func TestCopilotVSCodeReportsNoStoreWhenRootsHaveNoSessionFiles(t *testing.T) {
	root := t.TempDir()
	turns, found, err := collectCopilotVSCode(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if found || len(turns) != 0 {
		t.Fatalf("found %v, turns = %+v, want no store", found, turns)
	}
}
