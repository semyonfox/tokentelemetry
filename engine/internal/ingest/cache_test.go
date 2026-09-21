package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/cost"
	"github.com/semyonfox/tokentelemetry/engine/internal/model"
	"github.com/semyonfox/tokentelemetry/engine/internal/pricing"
)

type cacheTestScanner struct {
	agent  model.Agent
	inputs []string
	turns  []model.Turn
	calls  int
	err    error
	mutate func()
}

func (s *cacheTestScanner) Agent() model.Agent    { return s.agent }
func (s *cacheTestScanner) Roots() []string       { return append([]string(nil), s.inputs...) }
func (s *cacheTestScanner) cacheInputs() []string { return append([]string(nil), s.inputs...) }
func (s *cacheTestScanner) Scan(_ context.Context, emit func(model.Turn)) error {
	s.calls++
	for _, turn := range s.turns {
		emit(turn)
	}
	if s.mutate != nil {
		s.mutate()
	}
	return s.err
}

func cacheTestTurn(key string, output int64) model.Turn {
	return model.Turn{Key: key, SessionID: key, Agent: model.Agent("cache-test"), Timestamp: time.Unix(1_700_000_000, 0).UTC(), Model: "test-model", Project: "/work", Usage: model.Usage{Output: output}}
}

func TestRunCachedWarmHitAvoidsScanner(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "session.jsonl"), `{"usage":1}`)
	scanner := &cacheTestScanner{agent: "cache-test", inputs: []string{root}, turns: []model.Turn{cacheTestTurn("one", 1)}}

	cold, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if scanner.calls != 1 || cold.CacheMisses != 1 || cold.CacheHits != 0 || warm.CacheHits != 1 || warm.CacheMisses != 0 {
		t.Fatalf("calls=%d cold=%+v warm=%+v", scanner.calls, cold, warm)
	}
	if !reflect.DeepEqual(cold.Turns, warm.Turns) {
		t.Fatalf("cold turns = %+v, warm turns = %+v", cold.Turns, warm.Turns)
	}
	entries, err := filepath.Glob(filepath.Join(cacheDir, "providers", "*.json"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache entries = %v, err = %v", entries, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Dir(entries[0])); err != nil {
			t.Fatal(err)
		} else if info.Mode().Perm() != 0o700 {
			t.Fatalf("cache dir mode = %v", info.Mode().Perm())
		}
		if info, err := os.Stat(entries[0]); err != nil {
			t.Fatal(err)
		} else if info.Mode().Perm() != 0o600 {
			t.Fatalf("cache file mode = %v", info.Mode().Perm())
		}
	}
}

func TestRunCachedInvalidatesAppendAddDeleteAndWAL(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "history.db")
	writeFile(t, path, "db")
	scanner := &cacheTestScanner{agent: "cache-test", inputs: []string{path}, turns: []model.Turn{cacheTestTurn("one", 1)}}
	run := func() *Result {
		result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	run()
	if got := run().CacheHits; got != 1 {
		t.Fatalf("initial warm hits = %d", got)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("append")
	_ = f.Close()
	if got := run().CacheMisses; got != 1 {
		t.Fatalf("append misses = %d", got)
	}
	writeFile(t, path+"-wal", "committed-wal")
	if got := run().CacheMisses; got != 1 {
		t.Fatalf("WAL misses = %d", got)
	}
	if err := os.Remove(path + "-wal"); err != nil {
		t.Fatal(err)
	}
	if got := run().CacheMisses; got != 1 {
		t.Fatalf("WAL deletion misses = %d", got)
	}
	if scanner.calls != 4 {
		t.Fatalf("scanner calls = %d, want four cold parses", scanner.calls)
	}
}

func TestRunCachedDirectoryMembershipInvalidates(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "a.jsonl"), "a")
	scanner := &cacheTestScanner{agent: "cache-test", inputs: []string{root}, turns: []model.Turn{cacheTestTurn("one", 1)}}
	run := func() *Result {
		result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	run()
	writeFile(t, filepath.Join(root, "b.jsonl"), "b")
	if run().CacheMisses != 1 {
		t.Fatal("added source did not invalidate cache")
	}
	if err := os.Remove(filepath.Join(root, "b.jsonl")); err != nil {
		t.Fatal(err)
	}
	if run().CacheMisses != 1 {
		t.Fatal("deleted source did not invalidate cache")
	}
}

func TestRunCachedCorruptEntryFallsBackFresh(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "source"), "one")
	scanner := &cacheTestScanner{agent: "cache-test", inputs: []string{root}, turns: []model.Turn{cacheTestTurn("one", 1)}}
	if _, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir); err != nil {
		t.Fatal(err)
	}
	entries, _ := filepath.Glob(filepath.Join(cacheDir, "providers", "*.json"))
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	if err := os.WriteFile(entries[0], []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if scanner.calls != 2 || result.CacheMisses != 1 || len(result.Turns) != 1 {
		t.Fatalf("calls=%d result=%+v", scanner.calls, result)
	}
}

func TestRunCachedSourceMutationDuringScanIsNotSaved(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "source")
	writeFile(t, path, "one")
	scanner := &cacheTestScanner{agent: "cache-test", inputs: []string{root}, turns: []model.Turn{cacheTestTurn("one", 1)}}
	scanner.mutate = func() {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err == nil {
			_, _ = f.WriteString("x")
			_ = f.Close()
		}
	}
	if _, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir); err != nil {
		t.Fatal(err)
	}
	scanner.mutate = nil
	if result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir); err != nil || result.CacheMisses != 1 {
		t.Fatalf("second scan result=%+v err=%v", result, err)
	}
	if result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir); err != nil || result.CacheHits != 1 {
		t.Fatalf("third scan result=%+v err=%v", result, err)
	}
	if scanner.calls != 2 {
		t.Fatalf("scanner calls = %d", scanner.calls)
	}
}

func TestRunCachedRerunsDedupAndRuntimeOverlap(t *testing.T) {
	cacheDir := t.TempDir()
	nativeRoot, wrapperRoot := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(nativeRoot, "source"), "native")
	writeFile(t, filepath.Join(wrapperRoot, "source"), "wrapper")
	native := cacheTestTurn("shared", 5)
	native.Agent, native.SessionID = model.AgentCodex, "thread"
	duplicate := native
	wrapper := cacheTestTurn("wrapper", 7)
	wrapper.Agent, wrapper.RuntimeAgent, wrapper.RuntimeSessionID = model.Agent("hermes"), model.AgentCodex, "thread"
	a := &cacheTestScanner{agent: model.AgentCodex, inputs: []string{nativeRoot}, turns: []model.Turn{native, duplicate}}
	b := &cacheTestScanner{agent: model.Agent("hermes"), inputs: []string{wrapperRoot}, turns: []model.Turn{wrapper}}

	cold, err := RunCached(context.Background(), []Scanner{a, b}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := RunCached(context.Background(), []Scanner{a, b}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cold.Duplicates != 1 || warm.Duplicates != 1 || warm.CacheHits != 2 || !reflect.DeepEqual(cold.Turns, warm.Turns) {
		t.Fatalf("cold=%+v warm=%+v", cold, warm)
	}
	if len(warm.Turns) != 2 || !strings.Contains(warm.Turns[1].ExcludedReason, "overlaps") {
		t.Fatalf("overlap not recomputed: %+v", warm.Turns)
	}
	wrapperOnly, err := RunCached(context.Background(), []Scanner{b}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(wrapperOnly.Turns) != 1 || wrapperOnly.Turns[0].ExcludedReason != "" || wrapperOnly.Turns[0].Usage.Output != 7 {
		t.Fatalf("cached runtime marker leaked into wrapper-only scan: %+v", wrapperOnly.Turns)
	}
}

func TestRunCachedSanitizesEndpointWithoutChangingBillingLabel(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "source"), "one")
	turn := cacheTestTurn("one", 1)
	turn.Endpoint = "https://chatgpt.com/backend-api/codex?account=private"
	scanner := &cacheTestScanner{agent: model.AgentCodex, inputs: []string{root}, turns: []model.Turn{turn}}
	cold, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if cold.Turns[0].Endpoint != "https://chatgpt.com" || !reflect.DeepEqual(cold.Turns, warm.Turns) || warm.CacheHits != 1 {
		t.Fatalf("cold=%+v warm=%+v", cold, warm)
	}
	if cost.Of(cold.Turns[0], &pricing.Table{}).Billing != cost.BillingSubscription ||
		cost.Of(turn, &pricing.Table{}).Billing != cost.BillingSubscription {
		t.Fatal("endpoint sanitization changed billing classification")
	}
}

func TestRunCachedUnsafeEndpointBypassesPersistence(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "source"), "one")
	turn := cacheTestTurn("one", 1)
	turn.Endpoint = "https://user:secret@example.com/private"
	scanner := &cacheTestScanner{agent: "cache-test", inputs: []string{root}, turns: []model.Turn{turn}}
	for range 2 {
		result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
		if err != nil || len(result.Errors) != 0 || result.CacheHits != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	if scanner.calls != 2 {
		t.Fatalf("scanner calls = %d", scanner.calls)
	}
}

func TestRunCachedScannerErrorBypassesPersistenceAndPreservesWarning(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(root, "source"), "one")
	scanner := &cacheTestScanner{
		agent: "cache-test", inputs: []string{root}, turns: []model.Turn{cacheTestTurn("one", 1)}, err: errors.New("source warning"),
	}
	for range 2 {
		result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
		if err != nil || len(result.Errors) != 1 || result.Errors[0].Error() != "source warning" || result.CacheHits != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	if scanner.calls != 2 {
		t.Fatalf("scanner calls = %d", scanner.calls)
	}
}

func TestCachedParsedFileWarmHitAndAppendInvalidation(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	writeFile(t, path, "one")
	ctx := withFileCache(context.Background(), cacheDir, "test-namespace", model.AgentCodex)
	parses := 0
	parse := func() []model.Turn {
		parses++
		return []model.Turn{cacheTestTurn("one", int64(parses))}
	}
	first := cachedParsedFile(ctx, path, "parser-v1", parse)
	second := cachedParsedFile(ctx, path, "parser-v1", parse)
	if parses != 1 || !reflect.DeepEqual(first, second) {
		t.Fatalf("parses=%d first=%+v second=%+v", parses, first, second)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("two")
	_ = f.Close()
	third := cachedParsedFile(ctx, path, "parser-v1", parse)
	if parses != 2 || third[0].Usage.Output != 2 {
		t.Fatalf("parses=%d third=%+v", parses, third)
	}
}

func TestRunCachedIgnoresSQLiteSHMButBypassesNestedSymlink(t *testing.T) {
	root, cacheDir := t.TempDir(), t.TempDir()
	db := filepath.Join(root, "history.db")
	writeFile(t, db, "db")
	scanner := &cacheTestScanner{agent: "cache-test", inputs: []string{root}, turns: []model.Turn{cacheTestTurn("one", 1)}}
	if _, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, db+"-shm", "reader bookkeeping")
	if result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir); err != nil || result.CacheHits != 1 {
		t.Fatalf("SHM result=%+v err=%v", result, err)
	}

	target := filepath.Join(t.TempDir(), "target.jsonl")
	writeFile(t, target, "target")
	link := filepath.Join(root, "nested.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	for range 2 {
		result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir)
		if err != nil || result.CacheHits != 0 || result.CacheMisses != 1 {
			t.Fatalf("nested symlink result=%+v err=%v", result, err)
		}
	}
}

func TestRunCachedRootSymlinkTracksResolvedTarget(t *testing.T) {
	target, cacheDir := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(target, "source"), "one")
	parent := t.TempDir()
	link := filepath.Join(parent, "linked-root")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	scanner := &cacheTestScanner{agent: "cache-test", inputs: []string{link}, turns: []model.Turn{cacheTestTurn("one", 1)}}
	if _, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir); err != nil {
		t.Fatal(err)
	}
	if result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir); err != nil || result.CacheHits != 1 {
		t.Fatalf("warm result=%+v err=%v", result, err)
	}
	writeFile(t, filepath.Join(target, "source"), "changed-size")
	if result, err := RunCached(context.Background(), []Scanner{scanner}, cacheDir); err != nil || result.CacheMisses != 1 {
		t.Fatalf("retarget result=%+v err=%v", result, err)
	}
}

func BenchmarkRunCachedWarm(b *testing.B) {
	root, cacheDir := b.TempDir(), b.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("one"), 0o600); err != nil {
		b.Fatal(err)
	}
	scanner := &cacheTestScanner{agent: "cache-bench", inputs: []string{root}, turns: []model.Turn{cacheTestTurn("one", 1)}}
	_, _ = RunCached(context.Background(), []Scanner{scanner}, cacheDir)
	b.ResetTimer()
	for range b.N {
		_, _ = RunCached(context.Background(), []Scanner{scanner}, cacheDir)
	}
}
