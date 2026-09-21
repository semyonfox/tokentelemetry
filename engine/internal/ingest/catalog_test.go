package ingest

import "testing"

func TestCatalogSeparatesScannersFromSourceLimits(t *testing.T) {
	// Discovery must not inspect the developer's agent directories.
	for _, name := range []string{
		"CLAUDE_CONFIG_DIR", "CLAUDE_CONFIG_DIRS", "CLINE_DATA_DIR", "CLINE_DIR", "CLINE_SESSION_DATA_DIR", "CODEBUFF_DATA_DIR", "CODEWHALE_HOME", "CODEX_HOME", "COPILOT_HOME", "COPILOT_OTEL_FILE_EXPORTER_PATH", "DSH_HOME", "FACTORY_DIR", "FORGE_CONFIG", "GEMINI_CONFIG_DIR", "GOOSE_PATH_ROOT", "GROK_HOME", "HERMES_HOME", "KILO_DB", "KIMI_CODE_HOME", "KIMI_SHARE_DIR", "LINGTAI_HOME", "LINGTAI_TUI_HOME", "OPENCLAUDE_CONFIG_DIR", "OPENCLAW_STATE_DIR", "OPENCODE_DATA_DIR", "OPENCODE_DB", "PI_CODING_AGENT_DIR", "QUICKWORK_HOME", "QWEN_DATA_DIR", "QWEN_HOME", "TT_ANTIGRAVITY_DIR", "TT_CURSOR_AGENT_DIR", "TT_CURSOR_CSV", "TT_CURSOR_DB", "TT_VERCEL_REPORT", "VIBE_HOME", "WARP_DB_PATH", "ZS_DATA_DIR",
	} {
		t.Setenv(name, "")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	seen := map[string]bool{}
	for _, s := range All() {
		id := string(s.Agent())
		if seen[id] || SourceLimitation(id) != "" {
			t.Fatalf("duplicate or source-limited scanner %s", id)
		}
		seen[id] = true
	}
	if len(seen) != 40 {
		t.Fatalf("registered %d scanners, want 40", len(seen))
	}
	rows := Catalog()
	if len(rows) != 42 {
		t.Fatalf("catalog has %d entries, want 42", len(rows))
	}
	for _, row := range rows {
		if row.Status == "source-limited" && (seen[row.Agent] || row.Coverage == "") {
			t.Fatalf("invalid limitation: %+v", row)
		}
		if row.ExplicitOnly && row.Agent != "cursor-agent" && row.Agent != "vercel-gateway" {
			t.Fatalf("unexpected opt-in scanner: %+v", row)
		}
	}
}

func TestScannerSelectionPreservesCaseInsensitiveAgentFilters(t *testing.T) {
	if !Selected(&Claude{}, []string{"CLAUDE"}) || Selected(&Codex{}, []string{"CLAUDE"}) {
		t.Fatal("scanner filtering disagrees with report agent filtering")
	}
	if Selected(&CursorAgent{}, nil) || !Selected(&CursorAgent{}, []string{"CURSOR-AGENT"}) {
		t.Fatal("explicit importer selection ignored")
	}
	if SourceLimitation("GROKBOT") == "" {
		t.Fatal("source limitation bypassed by case")
	}
}
