package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeMultipleRootsAndNestedArchives(t *testing.T) {
	base := t.TempDir()
	native := filepath.Join(base, "native")
	archive := filepath.Join(base, "archive")
	replayed := claudeLine("message-a", "request-a", "claude-sonnet-4", "2026-09-01T10:00:00Z", 100, 30, 200, 20)
	writeFile(t, filepath.Join(native, "projects", "project", "original.jsonl"), replayed)
	writeFile(t, filepath.Join(archive, "device", "claude", "project", "copy.jsonl"), replayed)
	writeFile(t, filepath.Join(archive, "device", "claude", "project", "session", "subagents", "child.jsonl"),
		claudeLine("message-b", "request-b", "claude-sonnet-4", "2026-09-01T11:00:00Z", 50, 40, 80, 10))
	t.Setenv("CLAUDE_CONFIG_DIRS", native+","+archive+","+filepath.Join(native, "projects"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(base, "ignored"))
	s := NewClaude()
	if got := len(s.Roots()); got != 2 {
		t.Fatalf("roots = %v, want two distinct roots", s.Roots())
	}
	r, err := Run(context.Background(), []Scanner{s})
	if err != nil || len(r.Turns) != 2 || r.Duplicates != 1 {
		t.Fatalf("result = %+v, err = %v", r, err)
	}
	if !r.Turns[1].Subagent || r.Turns[1].Project != "/proj" || totalOut(r.Turns) != 70 {
		t.Fatalf("attribution or usage lost: %+v", r.Turns)
	}
}

func TestClaudeDefaultXDGAndLegacyRoots(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "custom-config")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CLAUDE_CONFIG_DIRS", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, dir := range []string{filepath.Join(xdg, "claude", "projects"), filepath.Join(home, ".claude", "projects")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if roots := NewClaude().Roots(); len(roots) != 2 {
		t.Fatalf("roots = %v, want XDG and legacy", roots)
	}
	// A missing explicit location must not cause an unrelated default scan.
	t.Setenv("CLAUDE_CONFIG_DIRS", filepath.Join(home, "missing"))
	if roots := NewClaude().Roots(); len(roots) != 0 {
		t.Fatalf("missing override fell back to %v", roots)
	}
}
