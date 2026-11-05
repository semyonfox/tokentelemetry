package ingest

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestCommandDirFindsExplicitLocationsOnly(t *testing.T) {
	for input, want := range map[string]string{
		// a cd inside the command moves on from where it started
		`{"cmd":"cd /code/swim && pnpm test","workdir":"/home/u"}`:                 "/code/swim",
		`{"cmd":"cd \"/code/quoted\" && make"}`:                                    "/code/quoted",
		`{"command":["bash","-lc","cd /code/argv && ls"],"workdir":"/home/u"}`:     "/code/argv",
		`{"cmd":["git","-C","/code/my repo","status"],"workdir":"/home/u"}`:        "/code/my repo",
		`{"workdir":"/code/only","cmd":"ls"}`:                                      "/code/only",
		`{"cmd":"pnpm test","workdir":"/code/swim"}`:                               "/code/swim",
		`text(await tools.exec_command({cmd:"ls", workdir: '/code/swim/client'}))`: "/code/swim/client",
		`cd /code/a && git -C /code/b status`:                                      "/code/b",
		`cd ~/dotfiles && stow -R zsh`:                                             "/home/u/dotfiles",
		`exec_command({cmd:"cd /code/a\nls"})`:                                     "/code/a",
		`make -C /code/tool build`:                                                 "/code/tool",
		"*** Begin Patch\n*** Update File: /code/swim/server/app.ts\n@@":           "/code/swim/server",
		// reading somewhere else is not working there
		`cat /code/other/README.md && rg foo /code/third`: "",
		`cd src && pnpm build`:                            "",
		`echo abcd /code/x`:                               "",
	} {
		if got := commandDir(input, "/home/u"); got != want {
			t.Errorf("commandDir(%q) = %q, want %q", input, got, want)
		}
	}
}

func codexCall(callType, field, value string) string {
	encoded, _ := json.Marshal(value)
	return `{"timestamp":"2026-08-01T10:00:01.500Z","type":"response_item","payload":{"type":"` +
		callType + `","name":"exec","` + field + `":` + string(encoded) + `}}`
}

// Each call's usage goes to where that response's tool calls worked, and a
// response with no located call stays with the session's cwd.
func TestCodexTurnsCarryTheirOwnCommandDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "sessions", "2026", "08", "01", "rollout-x-loc.jsonl"),
		codexMeta("loc", "", "user", "gpt-5.6-terra"),
		codexCall("function_call", "arguments", `{"cmd":"pnpm test","workdir":"/code/swim"}`),
		// the output of a call is never a location, even when it names one
		`{"timestamp":"2026-08-01T10:00:01.600Z","type":"response_item","payload":{"type":"function_call_output","output":"cd /code/elsewhere"}}`,
		codexTokens("2026-08-01T10:00:02.000Z", 100, 0, 10),
		codexTokens("2026-08-01T10:00:03.000Z", 200, 0, 20),
		codexCall("custom_tool_call", "input", "*** Begin Patch\n*** Update File: /code/notes/a.md\n"),
		codexTokens("2026-08-01T10:00:04.000Z", 300, 0, 30),
	)
	turns := scan(t, newCodexAt(root))
	want := []string{"/code/swim", "", "/code/notes"}
	if len(turns) != len(want) {
		t.Fatalf("got %d turns, want %d", len(turns), len(want))
	}
	for i, x := range turns {
		if x.CommandDir != want[i] || x.Project != "/proj" {
			t.Errorf("turn %d dir %q project %q, want dir %q project /proj", i, x.CommandDir, x.Project, want[i])
		}
	}
}

// Claude Code writes one call as a record per content block, and the copy that
// survives deduplication need not be the one holding the tool use.
func TestClaudeCommandDirSpansSplitRecords(t *testing.T) {
	root := t.TempDir()
	usage := `"usage":{"input_tokens":10,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}`
	record := func(msgID, content string) string {
		return `{"type":"assistant","timestamp":"2026-08-01T10:00:00Z","requestId":"req_` + msgID +
			`","sessionId":"s1","cwd":"/home/u","message":{"id":"` + msgID +
			`","model":"claude-opus-5","content":` + content + `,` + usage + `}}`
	}
	writeFile(t, filepath.Join(root, "projects", "-home-u", "s1.jsonl"),
		record("msg_1", `[{"type":"text","text":"cd /code/ignored"}]`),
		record("msg_1", `[{"type":"tool_use","name":"Edit","input":{"file_path":"/code/swim/app.ts"}}]`),
		record("msg_2", `[{"type":"tool_use","name":"Read","input":{"file_path":"/code/other/x.ts"}}]`),
		record("msg_3", `[{"type":"tool_use","name":"Bash","input":{"command":"git -C /code/notes log"}}]`),
		`{"type":"user","message":{"content":"plain text, not blocks"},"usage":"ignored"}`,
	)
	res, err := Run(t.Context(), []Scanner{newClaudeAt(root)})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, x := range res.Turns {
		got[x.Key] = x.CommandDir
	}
	want := map[string]string{
		claudeKey("msg_1", "req_msg_1"): "/code/swim",
		claudeKey("msg_2", "req_msg_2"): "",
		claudeKey("msg_3", "req_msg_3"): "/code/notes",
	}
	if len(got) != len(want) {
		t.Fatalf("turns = %v, want %v", got, want)
	}
	for key, dir := range want {
		if got[key] != dir {
			t.Errorf("%s dir = %q, want %q", key, got[key], dir)
		}
	}
}
