package report

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectDeletedPathInsideLiveCheckoutJoinsIt(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	mkdirs(t, filepath.Join(repo, ".git"))
	agentWorktree := filepath.Join(repo, ".claude", "worktrees", "agent-1")
	deletedSrc := filepath.Join(repo, "backend", "src", "gone")

	catalog := newProjectCatalog(nil, model.ProjectLineage{})
	for _, raw := range []string{agentWorktree, deletedSrc} {
		if got, want := catalog.ref(model.Turn{Project: raw}).key, normalizeProjectPath(repo); got != want {
			t.Errorf("deleted %q grouped as %q, want %q", raw, got, want)
		}
	}

	// with no checkout above it, a deleted path keeps its recorded spelling
	orphan := filepath.Join(t.TempDir(), "gone", "away")
	if got, want := catalog.ref(model.Turn{Project: orphan}).key, normalizeProjectPath(orphan); got != want {
		t.Errorf("orphan grouped as %q, want %q", got, want)
	}
}

func TestProjectMovedRootsFollowRecordedMoves(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "owned", "swim")
	home := filepath.Join(base, "home")
	mkdirs(t, filepath.Join(repo, ".git"), home)
	oldRoot := filepath.Join(base, "personal", "swim")
	lineage := model.ProjectLineage{MovedRoots: map[string]string{
		oldRoot:                   repo,
		`C:\Users\u\code\swim`:    oldRoot,
		`C:\Users\u`:              home,
		filepath.Join(base, "x"):  filepath.Join(base, "y"),
		normalizeProjectPath("/"): repo,
	}}
	catalog := newProjectCatalog(nil, lineage)

	for _, raw := range []string{
		oldRoot,
		filepath.Join(oldRoot, "server"),
		// deleted from the new checkout too, so it needs the walk up as well
		filepath.Join(oldRoot, "scripts", "parser"),
		// moved twice, Windows then Linux
		`C:\Users\u\code\swim\client`,
	} {
		if got, want := catalog.ref(model.Turn{Project: raw}).key, normalizeProjectPath(repo); got != want {
			t.Errorf("moved %q grouped as %q, want %q", raw, got, want)
		}
	}

	// the moved home is not a checkout but still groups at its new place
	if got, want := catalog.ref(model.Turn{Project: `C:\Users\u`}).key, normalizeProjectPath(home); got != want {
		t.Errorf("moved home grouped as %q, want %q", got, want)
	}
	// a folder that never arrived keeps its recorded spelling instead of
	// inventing a path under the new home
	if got, want := catalog.ref(model.Turn{Project: `C:\Users\u\AppData\Game`}).key, "C:/Users/u/AppData/Game"; got != want {
		t.Errorf("unmatched Windows folder grouped as %q, want %q", got, want)
	}
	// a move whose target never existed is not evidence
	stale := filepath.Join(base, "x", "sub")
	if got, want := catalog.ref(model.Turn{Project: stale}).key, normalizeProjectPath(stale); got != want {
		t.Errorf("move to a missing root grouped %q as %q, want %q", stale, got, want)
	}
}

func TestProjectMovedRootIgnoredWhileOldPathStillExists(t *testing.T) {
	base := t.TempDir()
	oldRoot := filepath.Join(base, "old")
	newRepo := filepath.Join(base, "new")
	mkdirs(t, oldRoot, filepath.Join(newRepo, ".git"))
	catalog := newProjectCatalog(nil, model.ProjectLineage{MovedRoots: map[string]string{oldRoot: newRepo}})
	if got, want := catalog.ref(model.Turn{Project: oldRoot}).key, normalizeProjectPath(oldRoot); got != want {
		t.Errorf("existing old root grouped as %q, want %q", got, want)
	}
}

func TestProjectCommandDirOnlyMovesTurnsOutsideAnyRepo(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repoA := filepath.Join(base, "a")
	repoB := filepath.Join(base, "b")
	mkdirs(t, home, filepath.Join(repoA, ".git"), filepath.Join(repoB, ".git", "src"))
	at := local(2026, time.August, 1, 10, 0)
	worked := turn("worked", "cheap", home, at, 1_000_000)
	worked.CommandDir = filepath.Join(repoB, "src")
	chatted := turn("chatted", "cheap", home, at, 2_000_000)
	nowhere := turn("nowhere", "cheap", home, at, 4_000_000)
	nowhere.CommandDir = filepath.Join(base, "tmp-clone")
	inRepo := turn("in-repo", "dear", repoA, at, 1_000_000)
	inRepo.CommandDir = repoB

	rep := BuildWithProjectLineage([]model.Turn{worked, chatted, nowhere, inRepo}, tbl(), Filter{}, Daily, 0, nil, model.ProjectLineage{})
	// cheap is $1 and dear $100 per million output tokens
	want := map[string]float64{
		normalizeProjectPath(repoB): 1,
		normalizeProjectPath(home):  2 + 4,
		normalizeProjectPath(repoA): 100,
	}
	if len(rep.ByProject) != len(want) {
		t.Fatalf("projects = %+v, want %v", rep.ByProject, want)
	}
	for key, cost := range want {
		got, ok := find(rep.ByProject, key)
		if !ok || got.Cost != cost {
			t.Errorf("project %q cost = %v (found %v), want %v", key, got.Cost, ok, cost)
		}
	}
	if rep.Totals.Cost != 107 {
		t.Errorf("total cost = %v, want 107", rep.Totals.Cost)
	}
	b, _ := find(rep.ByProject, normalizeProjectPath(repoB))
	if len(b.ProjectPaths) != 1 || b.ProjectPaths[0] != worked.CommandDir {
		t.Errorf("repo b paths = %v, want the command location", b.ProjectPaths)
	}
}

// A host-recorded move of a folder that is not a checkout places the folder,
// but it is not evidence of where work happened in either direction.
func TestProjectMovedFolderNeitherBlocksNorBecomesCommandDir(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	repo := filepath.Join(base, "repo")
	movedScratch := filepath.Join(base, "scratch")
	mkdirs(t, home, movedScratch, filepath.Join(repo, ".git"))
	lineage := model.ProjectLineage{MovedRoots: map[string]string{
		`C:\Users\u`:         home,
		`C:\Users\u\scratch`: movedScratch,
	}}
	catalog := newProjectCatalog(nil, lineage)

	worked := catalog.ref(model.Turn{Project: `C:\Users\u`, CommandDir: repo})
	if got, want := worked.key, normalizeProjectPath(repo); got != want {
		t.Errorf("moved home with a repo command grouped as %q, want %q", got, want)
	}
	idle := catalog.ref(model.Turn{Project: `C:\Users\u`, CommandDir: `C:\Users\u\scratch`})
	if got, want := idle.key, normalizeProjectPath(home); got != want {
		t.Errorf("moved home with a non-repo command grouped as %q, want %q", got, want)
	}
}
