// Package ingest discovers agent logs on disk and turns them into turns.
//
// Every scanner emits Turn values, marking source aggregates explicitly. Deduplication happens
// once, centrally, in Run — which is the whole point: agents replay history
// into new transcripts when a session is resumed, forked or compacted, and the
// only safe place to notice that a call has already been counted is a single
// global set keyed on the call's identity.
//
// On the audited machine this matters enormously. Half of all Claude assistant
// messages (2,407 of 4,839) were duplicates of earlier calls, so the previous
// session-level implementation reported 3.73M output tokens where the true
// deduplicated figure was 1.58M.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Scanner reads one agent's logs.
type Scanner interface {
	// Agent identifies which agent this scanner handles.
	Agent() model.Agent
	// Roots lists configured or detected source paths. Available skips scanners
	// with no paths; that does not infer whether the application is installed.
	Roots() []string
	// Scan walks the logs, calling emit once per API call found. emit is safe
	// to call from a single goroutine only; Run serialises it.
	Scan(ctx context.Context, emit func(model.Turn)) error
}

// Result is a completed scan.
type Result struct {
	Turns []model.Turn
	// CacheHits and CacheMisses count provider-level parsed scan cache lookups.
	// File-level reuse inside Claude and Codex is intentionally not included.
	CacheHits   int `json:"-"`
	CacheMisses int `json:"-"`
	// Duplicates counts calls dropped because an identical call had already
	// been seen — replayed history from resumed and forked transcripts.
	Duplicates int
	// Errors collects per-agent failures. A broken scanner degrades that one
	// agent's numbers; it never fails the whole run, because a user with one
	// unreadable log directory still deserves the rest of their data.
	Errors []error
}

// Run executes scanners concurrently and returns the deduplicated turns,
// sorted oldest-first.
//
// Scanners run in parallel because the work is dominated by reading thousands
// of small files; on the audited machine that is 317 Claude transcripts and
// 2,113 Codex rollouts.
func Run(ctx context.Context, scanners []Scanner) (*Result, error) {
	return run(ctx, scanners, "")
}

// RunCached executes scanners with a persistent parsed-result cache. An empty
// cacheDir preserves Run's uncached behavior.
func RunCached(ctx context.Context, scanners []Scanner, cacheDir string) (*Result, error) {
	if cacheDir == "" {
		return Run(ctx, scanners)
	}
	return run(ctx, scanners, cacheDir)
}

type scanBatch struct {
	turns []model.Turn
	err   error
	agent model.Agent
	hit   bool
	miss  bool
}

func run(ctx context.Context, scanners []Scanner, cacheDir string) (*Result, error) {
	scanners, sourceWarnings := distinctPiSources(scanners)
	if len(scanners) == 0 {
		return &Result{Errors: sourceWarnings}, nil
	}
	out := make([]scanBatch, len(scanners))

	sem := make(chan struct{}, max(1, runtime.NumCPU()))
	var wg sync.WaitGroup
	for i, s := range scanners {
		wg.Add(1)
		go func(i int, s Scanner) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// A panic in one scanner must not take the process down with it;
			// log formats change under us and a malformed file is not a reason
			// to lose every other agent's data.
			defer func() {
				if r := recover(); r != nil {
					out[i].err = fmt.Errorf("%s scanner panicked: %v", s.Agent(), r)
				}
			}()

			out[i] = scanScanner(ctx, s, cacheDir)
		}(i, s)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	res := &Result{Errors: sourceWarnings}
	seen := make(map[string]int, 1<<16)
	for _, b := range out {
		if b.hit {
			res.CacheHits++
		}
		if b.miss {
			res.CacheMisses++
		}
		if b.err != nil && !errors.Is(b.err, os.ErrNotExist) {
			res.Errors = append(res.Errors, b.err)
		}
		for _, t := range b.turns {
			if t.Key != "" {
				if kept, dup := seen[t.Key]; dup {
					res.Duplicates++
					// Claude Code can write several cumulative streaming snapshots
					// for one request. Keep the largest usage block, but retain the
					// first record's session, project and subagent attribution. A
					// copied transcript must not steal ownership of the original call.
					if moreCompleteUsage(t.Usage, res.Turns[kept].Usage) {
						res.Turns[kept].Usage = t.Usage
						res.Turns[kept].UnpricedReason = t.UnpricedReason
					}
					if t.Credits != nil && (res.Turns[kept].Credits == nil || *t.Credits > *res.Turns[kept].Credits) {
						res.Turns[kept].Credits = t.Credits
					}
					continue
				}
				seen[t.Key] = len(res.Turns)
			}
			res.Turns = append(res.Turns, t)
		}
	}
	markRuntimeOverlaps(res.Turns)
	sort.Slice(res.Turns, func(i, j int) bool {
		return res.Turns[i].Timestamp.Before(res.Turns[j].Timestamp)
	})
	return res, nil
}

func scanScanner(ctx context.Context, scanner Scanner, cacheDir string) scanBatch {
	batch := scanBatch{agent: scanner.Agent()}
	if cacheDir == "" {
		batch.turns, batch.err = scanFresh(ctx, scanner)
		return batch
	}
	inputs, cacheable := cacheInputsForScanner(scanner)
	if !cacheable || len(inputs) == 0 {
		batch.turns, batch.err = scanFresh(ctx, scanner)
		return batch
	}
	batch.miss = true
	namespace := cacheNamespace(scanner)
	before, stable, err := sourceFingerprint(inputs)
	if err != nil || !stable {
		batch.turns, batch.err = scanFresh(ctx, scanner)
		return batch
	}
	cachePath := scannerCachePath(cacheDir, scanner, inputs)
	if turns, hit := loadScanCache(cachePath, namespace, before); hit {
		after, afterStable, statErr := sourceFingerprint(inputs)
		currentInputs, currentCacheable := cacheInputsForScanner(scanner)
		if statErr == nil && afterStable && after == before && currentCacheable && sameCacheInputs(inputs, currentInputs) {
			batch.turns = turns
			batch.hit = true
			batch.miss = false
			return batch
		}
	}
	scanCtx := withFileCache(ctx, cacheDir, namespace, scanner.Agent())
	turns, scanErr := scanFresh(scanCtx, scanner)
	safeTurns, safe := sanitizeCachedTurns(turns)
	if safe {
		// Return the same endpoint form on cold and warm runs. The host, and
		// therefore billing classification, is unchanged.
		turns = safeTurns
	}
	batch.turns, batch.err = turns, scanErr
	after, afterStable, statErr := sourceFingerprint(inputs)
	currentInputs, currentCacheable := cacheInputsForScanner(scanner)
	if scanErr == nil && safe && statErr == nil && afterStable && after == before && currentCacheable &&
		sameCacheInputs(inputs, currentInputs) && ctx.Err() == nil {
		_ = writeScanCache(cachePath, namespace, before, turns)
	}
	return batch
}

func scanFresh(ctx context.Context, scanner Scanner) ([]model.Turn, error) {
	var turns []model.Turn
	err := scanner.Scan(ctx, func(turn model.Turn) {
		if turn.Agent == "" {
			turn.Agent = scanner.Agent()
		}
		turns = append(turns, turn)
	})
	return turns, err
}

// Runtime mirrors cannot be added to their executor's own ledger. A stored
// session link proves the overlap, but does not provide the per-call split
// needed to subtract a mixed aggregate. Retain it separately for inspection.
func markRuntimeOverlaps(turns []model.Turn) {
	type source struct {
		agent   model.Agent
		session string
	}
	native := map[source]bool{}
	for _, turn := range turns {
		if turn.SessionID != "" && turn.RuntimeSessionID == "" && !turn.Usage.IsZero() {
			native[source{turn.Agent, turn.SessionID}] = true
		}
	}
	for i := range turns {
		t := &turns[i]
		if t.RuntimeAgent != "" && t.RuntimeSessionID != "" && native[source{t.RuntimeAgent, t.RuntimeSessionID}] {
			t.ExcludedReason = fmt.Sprintf("%s record overlaps its explicitly linked %s session; native history counted, mirrored or mixed usage excluded without guessing a residual", t.Agent, t.RuntimeAgent)
		}
	}
}

// OMP is a Pi fork and honors Pi's directory override. If both scanners point
// at the very same ledger, its superset parser reads it once. Distinct roots
// remain independent; shared model names play no part in this decision.
func distinctPiSources(scanners []Scanner) ([]Scanner, []error) {
	var ompRoots []os.FileInfo
	for _, s := range scanners {
		if s.Agent() != model.Agent("omp") {
			continue
		}
		for _, root := range s.Roots() {
			if info, err := os.Stat(root); err == nil {
				ompRoots = append(ompRoots, info)
			}
		}
	}
	if len(ompRoots) == 0 {
		return scanners, nil
	}
	var out []Scanner
	var warnings []error
	for _, s := range scanners {
		shared := false
		if s.Agent() == model.AgentPi {
			roots := s.Roots()
			if len(roots) == 1 {
				if info, err := os.Stat(roots[0]); err == nil {
					for _, other := range ompRoots {
						if os.SameFile(info, other) {
							shared = true
							break
						}
					}
				}
			}
		}
		if shared {
			warnings = append(warnings, errors.New("pi and omp resolve to the same session ledger; scanned once with the OMP reader"))
		} else {
			out = append(out, s)
		}
	}
	return out, warnings
}

// moreCompleteUsage orders cumulative snapshots without adding their fields.
// Adding would bill one streamed response several times. Total billable tokens
// is the primary signal; the remaining fields break ties when a later snapshot
// supplies detail that does not change that total.
func moreCompleteUsage(a, b model.Usage) bool {
	if a.Total() != b.Total() {
		return a.Total() > b.Total()
	}
	if a.CacheWrite1h != b.CacheWrite1h {
		return a.CacheWrite1h > b.CacheWrite1h
	}
	if a.Reasoning != b.Reasoning {
		return a.Reasoning > b.Reasoning
	}
	return a.ContextTokens > b.ContextTokens
}

// homeDir returns the user's home directory, honouring the overrides the
// agents themselves respect so a relocated config is still found.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return ""
}

// existingDir returns dir if it is a readable directory, else "".
func existingDir(dir string) string {
	if dir == "" {
		return ""
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return ""
	}
	return dir
}

// envDir resolves an environment override to a directory, falling back to
// $HOME/def when unset.
func envDir(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return existingDir(v)
	}
	h := homeDir()
	if h == "" {
		return ""
	}
	return existingDir(filepath.Join(h, def))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
