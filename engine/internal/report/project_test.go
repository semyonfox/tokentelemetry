package report

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
)

func makeGitFamily(t *testing.T) (main, nested, worktree string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	nested = filepath.Join(main, "engine")
	worktree = filepath.Join(base, "linked")
	gitDir := filepath.Join(main, ".git", "worktrees", "linked")
	for _, dir := range []string{filepath.Join(main, ".git"), nested, worktree, gitDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+gitDir+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return main, nested, worktree
}

func TestProjectCatalogNormalizesPathsAndUsesGitFamilies(t *testing.T) {
	main, nested, worktree := makeGitFamily(t)
	turns := []model.Turn{{Project: main}, {Project: nested}, {Project: worktree}}
	catalog := newProjectCatalog(turns, model.ProjectLineage{})
	want := normalizeProjectPath(main)
	for _, raw := range []string{main, nested, worktree} {
		if got := catalog.ref(model.Turn{Project: raw}).key; got != want {
			t.Errorf("project key for %q = %q, want %q", raw, got, want)
		}
	}

	for raw, want := range map[string]string{
		`c:\Users\u\repo\.\src\..`: "C:/Users/u/repo",
		`C:/Users/u/repo/`:         "C:/Users/u/repo",
		"/work//repo/./engine/..":  "/work/repo",
	} {
		if got := normalizeProjectPath(raw); got != want {
			t.Errorf("normalizeProjectPath(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestProjectCatalogDoesNotTrustMalformedGitPointer(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("not a Git pointer\n"), 0600); err != nil {
		t.Fatal(err)
	}

	catalog := newProjectCatalog([]model.Turn{{Project: nested}}, model.ProjectLineage{})
	if got, want := catalog.ref(model.Turn{Project: nested}).key, normalizeProjectPath(nested); got != want {
		t.Errorf("malformed .git grouped %q, want raw path %q", got, want)
	}
}

func TestProjectCatalogKeepsSameBasenameFamiliesSeparateAndLabelsThem(t *testing.T) {
	catalog := newProjectCatalog([]model.Turn{
		{Project: "/missing/personal/api"},
		{Project: "/missing/work/api"},
		{Project: "/a/products/swim"},
		{Project: "/b/products/swim"},
		{Project: "/c/personal/swim"},
	}, model.ProjectLineage{})
	personal := catalog.ref(model.Turn{Project: "/missing/personal/api"})
	work := catalog.ref(model.Turn{Project: "/missing/work/api"})
	if personal.key == work.key {
		t.Fatalf("same-basename paths were merged: %q", personal.key)
	}
	if got := catalog.label(personal.key); got != "personal/api" {
		t.Errorf("personal label = %q, want personal/api", got)
	}
	if got := catalog.label(work.key); got != "work/api" {
		t.Errorf("work label = %q, want work/api", got)
	}
	if got := catalog.label(catalog.ref(model.Turn{Project: "/c/personal/swim"}).key); got != "personal/swim" {
		t.Errorf("shortest unique label = %q, want personal/swim", got)
	}
}

func TestProjectBuildGroupsGitFamilyAndRetainsRecordedPaths(t *testing.T) {
	main, nested, worktree := makeGitFamily(t)
	turns := []model.Turn{
		turn("main", "cheap", main, local(2026, time.August, 1, 10, 0), 1_000_000),
		turn("nested", "cheap", nested, local(2026, time.August, 1, 11, 0), 2_000_000),
		turn("worktree", "cheap", worktree, local(2026, time.August, 1, 12, 0), 3_000_000),
	}
	rep := Build(turns, tbl(), Filter{}, Daily, 0, []Dimension{DimProject})
	if len(rep.ByProject) != 1 {
		t.Fatalf("project buckets = %d, want 1: %+v", len(rep.ByProject), rep.ByProject)
	}
	b := rep.ByProject[0]
	if want := normalizeProjectPath(main); b.Key != want {
		t.Errorf("project key = %q, want %q", b.Key, want)
	}
	if b.Label != "main" {
		t.Errorf("project label = %q, want main", b.Label)
	}
	wantPaths := []string{main, nested, worktree}
	sort.Strings(wantPaths)
	if len(b.ProjectPaths) != len(wantPaths) {
		t.Fatalf("project paths = %v, want %v", b.ProjectPaths, wantPaths)
	}
	for i := range wantPaths {
		if b.ProjectPaths[i] != wantPaths[i] {
			t.Errorf("project paths = %v, want %v", b.ProjectPaths, wantPaths)
			break
		}
	}
	if len(rep.Series) != 1 || len(rep.Series[0].Breakdown) != 1 {
		t.Fatalf("nested project grouping = %+v", rep.Series)
	}
	nestedBucket := rep.Series[0].Breakdown[0]
	if nestedBucket.Key != b.Key || nestedBucket.Label != b.Label {
		t.Errorf("nested project = %+v, want key/label from %+v", nestedBucket, b)
	}
}

func TestProjectFilterMatchesFamilyAndExactRecordedPath(t *testing.T) {
	main, nested, _ := makeGitFamily(t)
	turns := []model.Turn{
		turn("main", "cheap", main, local(2026, time.August, 1, 10, 0), 1),
		turn("nested", "cheap", nested, local(2026, time.August, 1, 11, 0), 1),
	}
	if got := Build(turns, tbl(), Filter{Projects: []string{filepath.Base(main)}}, Daily, 0, nil).MatchedTurns; got != 2 {
		t.Errorf("family filter matched %d turns, want 2", got)
	}
	if got := Build(turns, tbl(), Filter{Projects: []string{nested}}, Daily, 0, nil).MatchedTurns; got != 1 {
		t.Errorf("exact CWD filter matched %d turns, want 1", got)
	}
}

func TestProjectBuildUsesPersistedRootForDeletedWorktreeAndNestedCWD(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	worktree := filepath.Join(base, "deleted-worktree")
	nested := filepath.Join(worktree, "engine", "internal")
	if err := os.MkdirAll(filepath.Join(main, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	turns := []model.Turn{
		turn("worktree", "cheap", worktree, local(2026, time.August, 1, 10, 0), 1),
		turn("nested", "cheap", nested, local(2026, time.August, 1, 11, 0), 1),
	}
	lineage := model.ProjectLineage{WorktreeRoots: map[string]string{worktree: main}}

	rep := BuildWithProjectLineage(turns, tbl(), Filter{}, Daily, 0, nil, lineage)
	if len(rep.ByProject) != 1 {
		t.Fatalf("project buckets = %d, want 1: %+v", len(rep.ByProject), rep.ByProject)
	}
	if got, want := rep.ByProject[0].Key, normalizeProjectPath(main); got != want {
		t.Errorf("project key = %q, want %q", got, want)
	}
	wantPaths := []string{nested, worktree}
	sort.Strings(wantPaths)
	if got := rep.ByProject[0].ProjectPaths; len(got) != len(wantPaths) || got[0] != wantPaths[0] || got[1] != wantPaths[1] {
		t.Errorf("project paths = %v, want %v", got, wantPaths)
	}

	if got := BuildWithProjectLineage(turns, tbl(), Filter{Projects: []string{filepath.Base(main)}}, Daily, 0, nil, lineage).MatchedTurns; got != 2 {
		t.Errorf("family filter matched %d turns, want 2", got)
	}
	if got := BuildWithProjectLineage(turns, tbl(), Filter{Projects: []string{nested}}, Daily, 0, nil, lineage).MatchedTurns; got != 1 {
		t.Errorf("exact nested CWD filter matched %d turns, want 1", got)
	}
}

func TestProjectPersistedRootsUseLongestPathBoundaryMatch(t *testing.T) {
	lineage := model.ProjectLineage{WorktreeRoots: map[string]string{
		"/deleted/worktree":        "/repos/outer",
		"/deleted/worktree/nested": "/repos/inner",
	}}
	catalog := newProjectCatalog([]model.Turn{
		{Project: "/deleted/worktree/nested/src"},
		{Project: "/deleted/worktree-other"},
	}, lineage)
	if got := catalog.ref(model.Turn{Project: "/deleted/worktree/nested/src"}).key; got != "/repos/inner" {
		t.Errorf("longest mapped ancestor resolved to %q, want /repos/inner", got)
	}
	if got := catalog.ref(model.Turn{Project: "/deleted/worktree-other"}).key; got != "/deleted/worktree-other" {
		t.Errorf("non-boundary prefix resolved to %q", got)
	}
}

func TestProjectPersistedRootsRejectCaseInsensitiveWindowsAmbiguity(t *testing.T) {
	lineage := model.ProjectLineage{WorktreeRoots: map[string]string{
		`C:\Worktrees\App`: `C:\Repos\One`,
		`c:\worktrees\app`: `C:\Repos\Two`,
	}}
	turn := model.Turn{Project: `C:\Worktrees\App\src`}
	catalog := newProjectCatalog([]model.Turn{turn}, lineage)
	if got, want := catalog.ref(turn).key, `C:/Worktrees/App/src`; got != want {
		t.Errorf("ambiguous Windows alias resolved to %q, want raw path %q", got, want)
	}
}

func TestProjectLiveGitMetadataWinsOverPersistedRoot(t *testing.T) {
	main, _, worktree := makeGitFamily(t)
	catalog := newProjectCatalog(
		[]model.Turn{{Project: worktree}},
		model.ProjectLineage{WorktreeRoots: map[string]string{worktree: "/wrong/persisted/root"}},
	)
	if got, want := catalog.ref(model.Turn{Project: worktree}).key, normalizeProjectPath(main); got != want {
		t.Errorf("live Git root = %q, want %q", got, want)
	}
}

func TestProjectSessionLineageWinsOverLiveGitAndPathAlias(t *testing.T) {
	main, _, worktree := makeGitFamily(t)
	turn := model.Turn{Agent: model.AgentCodex, SessionID: "codex-session", Project: worktree}
	lineage := model.ProjectLineage{
		SessionRoots: map[model.ProjectSession]string{
			{Agent: model.AgentCodex, SessionID: "codex-session"}: "/repos/session-root",
		},
		WorktreeRoots: map[string]string{worktree: "/repos/path-root"},
	}
	catalog := newProjectCatalog([]model.Turn{turn}, lineage)
	if got := catalog.ref(turn).key; got != "/repos/session-root" {
		t.Errorf("session root = %q, want /repos/session-root (live root was %q)", got, main)
	}

	otherSession := turn
	otherSession.SessionID = "other"
	if got, want := catalog.ref(otherSession).key, normalizeProjectPath(main); got != want {
		t.Errorf("unmapped session root = %q, want live Git root %q", got, want)
	}
}

func TestProjectFilterMatchesSessionLineageWithoutRecordedCWD(t *testing.T) {
	turnWithNoCWD := turn("session", "cheap", "", local(2026, time.August, 1, 10, 0), 1)
	turnWithNoCWD.Agent = model.AgentCodex
	turnWithNoCWD.SessionID = "session"
	turns := []model.Turn{turnWithNoCWD}
	lineage := model.ProjectLineage{SessionRoots: map[model.ProjectSession]string{
		{Agent: model.AgentCodex, SessionID: "session"}: "/repos/session-root",
	}}
	filter := Filter{Projects: []string{"session-root"}}
	if got := BuildWithProjectLineage(turns, tbl(), filter, Daily, 0, nil, lineage).MatchedTurns; got != 1 {
		t.Errorf("session-root filter matched %d turns, want 1", got)
	}
}

func TestProjectSortingUsesCostThenTokensThenLabel(t *testing.T) {
	turns := []model.Turn{
		turn("alpha", "cheap", "/missing/alpha", local(2026, time.August, 1, 10, 0), 1_000_000),
		turn("zeta", "cheap", "/missing/zeta", local(2026, time.August, 1, 11, 0), 1_000_000),
		turn("small", "dear", "/missing/small", local(2026, time.August, 1, 12, 0), 10_000),
	}
	rows := Build(turns, tbl(), Filter{}, Daily, 0, nil).ByProject
	if len(rows) != 3 {
		t.Fatalf("project rows = %d, want 3", len(rows))
	}
	for i, want := range []string{"alpha", "zeta", "small"} {
		if rows[i].Label != want {
			t.Errorf("row %d label = %q, want %q", i, rows[i].Label, want)
		}
	}
}
