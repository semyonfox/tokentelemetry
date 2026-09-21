package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Grok reads the durable per-turn ledger written by Grok Build 1.0.14 and
// newer. The ledger contains accounting only; conversation content lives in
// separate files this scanner never opens.
type Grok struct {
	root string
}

func NewGrok() *Grok {
	return &Grok{root: envDir("GROK_HOME", ".grok")}
}

func newGrokAt(root string) *Grok { return &Grok{root: root} }

func (g *Grok) Agent() model.Agent { return model.AgentGrok }

func (g *Grok) Roots() []string {
	if g.root == "" {
		return nil
	}
	if sessions := existingDir(filepath.Join(g.root, "sessions")); sessions != "" {
		return []string{sessions}
	}
	if root := existingDir(g.root); root != "" {
		return []string{root}
	}
	return nil
}

type grokUsageFile struct {
	SessionID string          `json:"sessionId"`
	UpdatedAt string          `json:"updatedAt"`
	Turns     []grokTurnUsage `json:"turns"`
}

type grokUsageSummary struct {
	InputTokens         int64                       `json:"inputTokens"`
	OutputTokens        int64                       `json:"outputTokens"`
	CachedReadTokens    int64                       `json:"cachedReadTokens"`
	CacheCreationTokens int64                       `json:"cacheCreationTokens"`
	ReasoningTokens     int64                       `json:"reasoningTokens"`
	TotalTokens         int64                       `json:"totalTokens"`
	ModelCalls          int64                       `json:"modelCalls"`
	UsageIsIncomplete   bool                        `json:"usageIsIncomplete"`
	PrimaryModelID      string                      `json:"primaryModelId"`
	ModelUsage          map[string]grokUsageSummary `json:"modelUsage"`
}

type grokTurnUsage struct {
	TurnNumber int64  `json:"turnNumber"`
	EndedAt    string `json:"endedAt"`
	grokUsageSummary
}

// grokSessionSummary deliberately excludes titles, generated summaries and
// every conversation-derived field from summary.json.
type grokSessionSummary struct {
	Info struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	} `json:"info"`
	Created            string `json:"created_at"`
	Updated            string `json:"updated_at"`
	Last               string `json:"last_active_at"`
	SessionKind        string `json:"session_kind"`
	ParentSessionID    string `json:"parent_session_id"`
	ForkedAt           string `json:"forked_at"`
	InheritedPrefixLen *int64 `json:"inherited_prefix_len"`
}

type grokLedger struct {
	sessionID string
	project   string
	kind      string
	parentID  string
	path      string
	modified  time.Time
	usage     grokUsageFile
	summary   grokSessionSummary
}

func (g *Grok) Scan(ctx context.Context, emit func(model.Turn)) error {
	roots := g.Roots()
	if len(roots) == 0 {
		return nil
	}

	paths, err := grokUsagePaths(ctx, roots[0])
	if err != nil {
		return err
	}
	var scanErr error
	ledgers := make([]grokLedger, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		ledger, ok, readErr := readGrokLedger(path)
		if readErr != nil {
			scanErr = errors.Join(scanErr, readErr)
		}
		if !ok {
			continue
		}
		ledgers = append(ledgers, ledger)
	}

	bySession := grokSessionIndexes(ledgers)
	suppressedChildren := grokSuppressedChildren(ledgers, bySession)
	omittedParents := grokOmittedParentSessions(ledgers, bySession, suppressedChildren)
	incomplete := grokIncompleteTurnCount(ledgers, bySession)
	for i, ledger := range ledgers {
		// Atomic rewrites and interrupted restores can leave several copies of
		// one session ledger on disk. The newest durable copy is canonical.
		if bySession[ledger.sessionID] != i {
			continue
		}
		if omittedParents[ledger.sessionID] || suppressedChildren[ledger.sessionID] {
			continue
		}
		child := isGrokChildKind(ledger.kind)
		for _, row := range ledger.usage.Turns {
			if err := ctx.Err(); err != nil {
				return err
			}
			origin := grokTurnOrigin(ledger, row, ledgers, bySession, make(map[string]bool))
			owner := ledger
			if ownerIndex, ok := bySession[origin]; ok {
				owner = ledgers[ownerIndex]
			}
			for _, turn := range grokTurns(owner, row, child, origin) {
				emit(turn)
			}
		}
	}

	legacyErr := scanGrokLegacyFallbacks(ctx, roots[0], paths, emit)
	scanErr = errors.Join(scanErr, legacyErr)
	if incomplete > 0 && len(omittedParents) > 0 {
		scanErr = errors.Join(scanErr, fmt.Errorf("grok: %d persisted turn(s) report incomplete usage; omitted %d parent ledger(s) whose aggregates may overlap retained child usage", incomplete, len(omittedParents)))
	} else if incomplete > 0 {
		scanErr = errors.Join(scanErr, fmt.Errorf("grok: %d persisted turn(s) report incomplete usage", incomplete))
	} else if len(omittedParents) > 0 {
		scanErr = errors.Join(scanErr, fmt.Errorf("grok: omitted %d parent ledger(s) whose aggregates may overlap retained child usage", len(omittedParents)))
	}
	return scanErr
}

func grokUsagePaths(ctx context.Context, root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			// A concurrently removed or unreadable session must not hide the rest.
			return nil
		}
		if d.IsDir() || d.Name() != "usage.json" || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err == nil && info.Mode().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func readGrokLedger(path string) (grokLedger, bool, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return grokLedger{}, false, nil
	}
	usage, ok := decodeGrokJSON[grokUsageFile](path)
	if !ok {
		return grokLedger{}, false, fmt.Errorf("%s: decode authoritative usage.json", path)
	}
	if usage.SessionID == "" {
		return grokLedger{}, false, fmt.Errorf("%s: authoritative usage.json lacks sessionId", path)
	}
	// summary.json enriches accounting with project and delegation metadata,
	// but is not part of the durable usage contract. Missing or torn summaries
	// must not discard a valid ledger.
	summary, _ := decodeGrokJSON[grokSessionSummary](filepath.Join(filepath.Dir(path), "summary.json"))
	if summary.Info.ID != "" && summary.Info.ID != usage.SessionID {
		return grokLedger{}, false, fmt.Errorf("%s: usage.json sessionId %q does not match summary id %q", path, usage.SessionID, summary.Info.ID)
	}
	return grokLedger{
		sessionID: usage.SessionID,
		project:   summary.Info.CWD,
		kind:      summary.SessionKind,
		parentID:  summary.ParentSessionID,
		path:      path,
		modified:  info.ModTime(),
		usage:     usage,
		summary:   summary,
	}, true, nil
}

type grokJSONDocument interface {
	grokUsageFile | grokSessionSummary
}

func decodeGrokJSON[T grokJSONDocument](path string) (T, bool) {
	var value T
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxLine {
		return value, false
	}
	f, err := os.Open(path)
	if err != nil {
		return value, false
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, maxLine))
	if err := dec.Decode(&value); err != nil {
		return value, false
	}
	// Reject a second JSON value. Atomic rewrites should leave exactly one.
	if dec.Decode(&struct{}{}) != io.EOF {
		return value, false
	}
	return value, true
}

func isGrokChildKind(kind string) bool {
	switch kind {
	case "subagent", "subagent_fork", "subagent_resume":
		return true
	default:
		return false
	}
}

func grokUsageIncomplete(row grokTurnUsage) bool {
	if row.UsageIsIncomplete {
		return true
	}
	for _, usage := range row.ModelUsage {
		if usage.UsageIsIncomplete {
			return true
		}
	}
	return false
}

// grokSessionIndexes keeps one deterministic, newest ledger for each session
// when copies exist after a fork or interrupted restore.
func grokSessionIndexes(ledgers []grokLedger) map[string]int {
	indexes := make(map[string]int, len(ledgers))
	for i, ledger := range ledgers {
		previous, exists := indexes[ledger.sessionID]
		if !exists || ledger.modified.After(ledgers[previous].modified) ||
			(ledger.modified.Equal(ledgers[previous].modified) && ledger.path < ledgers[previous].path) {
			indexes[ledger.sessionID] = i
		}
	}
	return indexes
}

func grokIncompleteTurnCount(ledgers []grokLedger, indexes map[string]int) int {
	count := 0
	for _, index := range indexes {
		for _, row := range ledgers[index].usage.Turns {
			if grokUsageIncomplete(row) {
				count++
			}
		}
	}
	return count
}

func grokLedgerHasUsableTurns(ledger grokLedger) bool {
	for _, row := range ledger.usage.Turns {
		if len(grokTurns(ledger, row, false, ledger.sessionID)) > 0 {
			return true
		}
	}
	return false
}

// grokSuppressedChildren identifies the one safe parent/child precedence case:
// the parent update stream records that exact child finishing before a complete
// turn whose usage matches the durable parent ledger. In every other case the
// child remains the exact source and the ambiguous parent is handled below.
func grokSuppressedChildren(ledgers []grokLedger, indexes map[string]int) map[string]bool {
	suppressed := make(map[string]bool)
	for _, childIndex := range indexes {
		child := ledgers[childIndex]
		if !isGrokChildKind(child.kind) || child.parentID == "" || child.parentID == child.sessionID {
			continue
		}
		parentIndex, ok := indexes[child.parentID]
		if !ok {
			continue
		}
		if grokParentFoldedChild(ledgers[parentIndex], child.sessionID) {
			suppressed[child.sessionID] = true
		}
	}
	return suppressed
}

func grokParentFoldedChild(parent grokLedger, childID string) bool {
	f, err := os.Open(filepath.Join(filepath.Dir(parent.path), "updates.jsonl"))
	if err != nil {
		return false
	}
	defer f.Close()
	finished := false
	for line := range jsonLines(f) {
		var record struct {
			Params struct {
				Update struct {
					Kind    string            `json:"sessionUpdate"`
					ChildID string            `json:"child_session_id"`
					Usage   *grokUsageSummary `json:"usage"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		update := record.Params.Update
		if update.Kind == "subagent_finished" && update.ChildID == childID {
			finished = true
			continue
		}
		if !finished || update.Kind != "turn_completed" || update.Usage == nil || update.Usage.UsageIsIncomplete {
			continue
		}
		for _, turn := range parent.usage.Turns {
			if grokUsageAccountingEqual(turn.grokUsageSummary, *update.Usage) &&
				reflect.DeepEqual(turn.ModelUsage, update.Usage.ModelUsage) {
				return true
			}
		}
	}
	return false
}

func grokUsageAccountingEqual(a, b grokUsageSummary) bool {
	return a.InputTokens == b.InputTokens &&
		a.OutputTokens == b.OutputTokens &&
		a.CachedReadTokens == b.CachedReadTokens &&
		a.CacheCreationTokens == b.CacheCreationTokens &&
		a.ReasoningTokens == b.ReasoningTokens &&
		a.TotalTokens == b.TotalTokens &&
		a.ModelCalls == b.ModelCalls &&
		a.UsageIsIncomplete == b.UsageIsIncomplete
}

func grokTurnOrigin(ledger grokLedger, row grokTurnUsage, ledgers []grokLedger, indexes map[string]int, visited map[string]bool) string {
	if visited[ledger.sessionID] || ledger.parentID == "" || !grokHasForkProvenance(ledger.summary) {
		return ledger.sessionID
	}
	visited[ledger.sessionID] = true
	parentIndex, ok := indexes[ledger.parentID]
	if !ok {
		return ledger.sessionID
	}
	parent := ledgers[parentIndex]
	for _, candidate := range parent.usage.Turns {
		if grokTurnUsageEqual(candidate, row) {
			return grokTurnOrigin(parent, candidate, ledgers, indexes, visited)
		}
	}
	return ledger.sessionID
}

func grokHasForkProvenance(summary grokSessionSummary) bool {
	if summary.ParentSessionID == "" {
		return false
	}
	return summary.ForkedAt != "" || summary.InheritedPrefixLen != nil ||
		summary.SessionKind == "fork" || summary.SessionKind == "worktree" ||
		summary.SessionKind == "subagent_fork"
}

func grokTurnUsageEqual(a, b grokTurnUsage) bool {
	if a.TurnNumber != b.TurnNumber || !grokUsageAccountingEqual(a.grokUsageSummary, b.grokUsageSummary) ||
		!reflect.DeepEqual(a.ModelUsage, b.ModelUsage) {
		return false
	}
	aTime, bTime := parseTime(a.EndedAt), parseTime(b.EndedAt)
	if !aTime.IsZero() && !bTime.IsZero() {
		return aTime.Equal(bTime)
	}
	return a.EndedAt == b.EndedAt
}

// grokOmittedParentSessions gives exact child ledgers precedence over their
// parents' mixed aggregates. The native format records no child identity in a
// folded parent row, so neither a complete flag nor a later parent rewrite can
// prove that a particular child was folded. Emitting both can double-count;
// suppressing the child can lose real usage. Omit the ambiguous parent and
// report it instead. The omission propagates to ancestors because their
// aggregates may include the same child usage.
func grokOmittedParentSessions(ledgers []grokLedger, indexes map[string]int, suppressedChildren map[string]bool) map[string]bool {
	parentByChild := make(map[string]string, len(indexes))
	queue := make([]string, 0, len(indexes))
	for _, childIndex := range indexes {
		child := ledgers[childIndex]
		if suppressedChildren[child.sessionID] {
			continue
		}
		if !isGrokChildKind(child.kind) || child.parentID == "" || child.parentID == child.sessionID {
			continue
		}
		parentIndex, exists := indexes[child.parentID]
		if !exists {
			continue
		}
		parentByChild[child.sessionID] = ledgers[parentIndex].sessionID
		if grokLedgerHasUsableTurns(child) {
			queue = append(queue, child.sessionID)
		}
	}

	omitted := make(map[string]bool)
	visited := make(map[string]bool, len(indexes))
	for len(queue) > 0 {
		childID := queue[0]
		queue = queue[1:]
		if visited[childID] {
			continue
		}
		visited[childID] = true

		parentID, exists := parentByChild[childID]
		if !exists {
			continue
		}
		if !omitted[parentID] {
			omitted[parentID] = true
		}
		// A nested parent can itself be folded into another parent, so walk
		// the relation upward even when it was already omitted on another path.
		queue = append(queue, parentID)
	}
	return omitted
}

func grokTurns(ledger grokLedger, row grokTurnUsage, child bool, origin string) []model.Turn {
	ts := parseTime(row.EndedAt)
	if ts.IsZero() && row.EndedAt != "" {
		return nil
	}
	missingTimeReason := ""
	if ts.IsZero() {
		missingTimeReason = "Grok usage has no native turn timestamp; historical pricing is unavailable"
	}

	if len(row.ModelUsage) == 0 {
		if row.PrimaryModelID == "" {
			turn, ok := grokTurn(ledger, row, "unknown", row.grokUsageSummary, child, ts, origin)
			if !ok {
				return nil
			}
			turn.UnpricedReason = appendGrokReason(turn.UnpricedReason, "Grok usage lacks per-model attribution")
			turn.UnpricedReason = appendGrokReason(turn.UnpricedReason, missingTimeReason)
			return []model.Turn{turn}
		}
		turn, ok := grokTurn(ledger, row, row.PrimaryModelID, row.grokUsageSummary, child, ts, origin)
		if !ok {
			return nil
		}
		turn.UnpricedReason = appendGrokReason(turn.UnpricedReason, missingTimeReason)
		return []model.Turn{turn}
	}
	if !grokModelUsageReconciles(row.grokUsageSummary) {
		turn, ok := grokTurn(ledger, row, "unknown", row.grokUsageSummary, child, ts, origin)
		if !ok {
			return nil
		}
		turn.UnpricedReason = appendGrokReason(turn.UnpricedReason, "Grok modelUsage components do not reconcile to the turn total")
		turn.UnpricedReason = appendGrokReason(turn.UnpricedReason, missingTimeReason)
		return []model.Turn{turn}
	}

	models := make([]string, 0, len(row.ModelUsage))
	for modelID := range row.ModelUsage {
		models = append(models, modelID)
	}
	sort.Strings(models)
	turns := make([]model.Turn, 0, len(models))
	for _, modelID := range models {
		usage := row.ModelUsage[modelID]
		turn, ok := grokTurn(ledger, row, modelID, usage, child, ts, origin)
		if ok {
			turn.UnpricedReason = appendGrokReason(turn.UnpricedReason, missingTimeReason)
			turns = append(turns, turn)
		}
	}
	return turns
}

func grokTurn(ledger grokLedger, row grokTurnUsage, modelID string, source grokUsageSummary, child bool, ts time.Time, origin string) (model.Turn, bool) {
	usage, ok := normalizeGrokUsage(source)
	if !ok || usage.IsZero() || modelID == "" {
		return model.Turn{}, false
	}
	reason := ""
	if source.UsageIsIncomplete || row.UsageIsIncomplete {
		reason = "Grok marks this usage incomplete; token totals may under-count subagents"
	}
	return model.Turn{
		Key:            identityKey("grok-usage-model", origin, strconv.FormatInt(row.TurnNumber, 10), modelID),
		SessionID:      ledger.sessionID,
		Agent:          model.AgentGrok,
		Timestamp:      ts,
		Model:          modelID,
		Provider:       "grok",
		Project:        ledger.project,
		Usage:          usage,
		Subagent:       child,
		Aggregate:      source.ModelCalls != 1,
		UnpricedReason: reason,
	}, true
}

func grokModelUsageReconciles(top grokUsageSummary) bool {
	var sum grokUsageSummary
	for modelID, row := range top.ModelUsage {
		if modelID == "" {
			return false
		}
		if _, ok := normalizeGrokUsage(row); !ok {
			return false
		}
		var ok bool
		sum.InputTokens, ok = addGrokInt(sum.InputTokens, row.InputTokens)
		if !ok {
			return false
		}
		sum.OutputTokens, ok = addGrokInt(sum.OutputTokens, row.OutputTokens)
		if !ok {
			return false
		}
		sum.CachedReadTokens, ok = addGrokInt(sum.CachedReadTokens, row.CachedReadTokens)
		if !ok {
			return false
		}
		sum.CacheCreationTokens, ok = addGrokInt(sum.CacheCreationTokens, row.CacheCreationTokens)
		if !ok {
			return false
		}
		sum.ReasoningTokens, ok = addGrokInt(sum.ReasoningTokens, row.ReasoningTokens)
		if !ok {
			return false
		}
		sum.TotalTokens, ok = addGrokInt(sum.TotalTokens, row.TotalTokens)
		if !ok {
			return false
		}
		sum.ModelCalls, ok = addGrokInt(sum.ModelCalls, row.ModelCalls)
		if !ok {
			return false
		}
	}
	return grokUsageAccountingEqual(sum, top)
}

func addGrokInt(a, b int64) (int64, bool) {
	if b < 0 || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

func scanGrokLegacyFallbacks(ctx context.Context, root string, usagePaths []string, emit func(model.Turn)) error {
	authoritativeDirs := make(map[string]bool, len(usagePaths))
	for _, path := range usagePaths {
		authoritativeDirs[filepath.Dir(path)] = true
	}
	var paths []string
	var scanErr error
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			scanErr = errors.Join(scanErr, walkErr)
			return nil
		}
		if entry.IsDir() || entry.Name() != "updates.jsonl" || entry.Type()&fs.ModeSymlink != 0 || authoritativeDirs[filepath.Dir(path)] {
			return nil
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return errors.Join(scanErr, err)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return errors.Join(scanErr, err)
		}
		summary, _ := decodeGrokJSON[grokSessionSummary](filepath.Join(filepath.Dir(path), "summary.json"))
		sessionID := summary.Info.ID
		if sessionID == "" {
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				rel = filepath.Dir(path)
			}
			sessionID = "directory:" + filepath.ToSlash(rel)
		}
		if err := scanGrokLegacy(path, sessionID, summary, emit); err != nil {
			scanErr = errors.Join(scanErr, err)
		}
	}
	return scanErr
}

func scanGrokLegacy(path, sessionID string, summary grokSessionSummary, emit func(model.Turn)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	seen := make(map[string]model.Turn)
	ordinal := 0
	for line := range jsonLines(f) {
		var record struct {
			Params struct {
				Update struct {
					Kind   string            `json:"sessionUpdate"`
					Prompt string            `json:"prompt_id"`
					Usage  *grokUsageSummary `json:"usage"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &record) != nil || record.Params.Update.Kind != "turn_completed" || record.Params.Update.Usage == nil {
			continue
		}
		source := *record.Params.Update.Usage
		usage, reason, ok := normalizeGrokLegacyUsage(source)
		if !ok || usage.IsZero() {
			continue
		}
		promptID := record.Params.Update.Prompt
		if promptID == "" {
			ordinal++
			promptID = strconv.Itoa(ordinal)
		}
		modelID := "unknown"
		if len(source.ModelUsage) == 1 {
			for candidate := range source.ModelUsage {
				if candidate != "" {
					modelID = candidate
				}
			}
		}
		if modelID == "unknown" {
			reason = appendGrokReason(reason, "Legacy Grok usage does not identify exactly one model")
		}
		if source.UsageIsIncomplete {
			reason = appendGrokReason(reason, "Grok marks this usage incomplete; token totals may under-count subagents")
		}
		// Legacy completion notifications carry no per-turn timestamp. Session
		// metadata describes the whole session and cannot safely date this call.
		stamp := time.Time{}
		reason = appendGrokReason(reason, "Legacy Grok usage has no native completion timestamp; historical pricing is unavailable")
		seen[promptID] = model.Turn{
			Key: identityKey("grok-legacy", sessionID, promptID), SessionID: sessionID,
			Agent: model.AgentGrok, Timestamp: stamp, Model: modelID, Provider: "grok",
			Project: summary.Info.CWD, Usage: usage, Aggregate: true, UnpricedReason: reason,
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		emit(seen[key])
	}
	return nil
}

func normalizeGrokLegacyUsage(source grokUsageSummary) (model.Usage, string, bool) {
	if source.InputTokens < 0 || source.OutputTokens < 0 || source.CachedReadTokens < 0 ||
		source.CacheCreationTokens < 0 || source.ReasoningTokens < 0 {
		return model.Usage{}, "", false
	}
	usage, ok := (model.Usage{
		Input: source.InputTokens, Output: source.OutputTokens,
		CacheRead: source.CachedReadTokens, CacheWrite: source.CacheCreationTokens,
		Reasoning: source.ReasoningTokens,
	}).SanitizeAggregate()
	if !ok {
		return model.Usage{}, "", false
	}
	if source.CachedReadTokens > source.InputTokens ||
		source.CacheCreationTokens > source.InputTokens-source.CachedReadTokens {
		fallback, ok := (model.Usage{
			Unclassified: source.InputTokens,
			Output:       source.OutputTokens,
			Reasoning:    min(source.ReasoningTokens, source.OutputTokens),
		}).SanitizeAggregate()
		return fallback, "Grok cache subsets exceed reported gross input", ok
	}
	usage.Input = source.InputTokens - source.CachedReadTokens - source.CacheCreationTokens
	if usage.Reasoning > usage.Output {
		usage.Reasoning = usage.Output
	}
	return usage, "", true
}

func appendGrokReason(current, next string) string {
	if next == "" {
		return current
	}
	if current == "" {
		return next
	}
	return current + "; " + next
}

func normalizeGrokUsage(source grokUsageSummary) (model.Usage, bool) {
	if source.InputTokens < 0 || source.OutputTokens < 0 || source.CachedReadTokens < 0 ||
		source.CacheCreationTokens < 0 || source.ReasoningTokens < 0 || source.ModelCalls < 0 ||
		source.CachedReadTokens > source.InputTokens ||
		source.CacheCreationTokens > source.InputTokens-source.CachedReadTokens ||
		source.ReasoningTokens > source.OutputTokens {
		return model.Usage{}, false
	}
	if source.InputTokens > math.MaxInt64-source.OutputTokens {
		return model.Usage{}, false
	}
	if source.ModelCalls == 0 && (source.InputTokens != 0 || source.OutputTokens != 0 ||
		source.CachedReadTokens != 0 || source.CacheCreationTokens != 0 || source.ReasoningTokens != 0) {
		// A durable ledger increments modelCalls for every recorded inference.
		// Treat a populated row without it as incomplete schema/corruption, not
		// an unbounded aggregate.
		return model.Usage{}, false
	}
	if source.ModelCalls == 1 && (source.InputTokens > model.MaxPerCallTokens || source.OutputTokens > model.MaxPerCallTokens) {
		return model.Usage{}, false
	}
	if source.ModelCalls != 1 {
		limit := int64(model.MaxPerCallTokens) * int64(model.MaxPerCallTokens)
		if source.ModelCalls > model.MaxPerCallTokens {
			return model.Usage{}, false
		}
		if source.ModelCalls > 1 {
			limit = source.ModelCalls * model.MaxPerCallTokens
		}
		for _, value := range [...]int64{
			source.InputTokens,
			source.OutputTokens,
			source.CachedReadTokens,
			source.CacheCreationTokens,
			source.ReasoningTokens,
		} {
			if value > limit {
				return model.Usage{}, false
			}
		}
	}
	if source.TotalTokens != source.InputTokens+source.OutputTokens {
		return model.Usage{}, false
	}
	usage := model.Usage{
		Input:      source.InputTokens - source.CachedReadTokens - source.CacheCreationTokens,
		Output:     source.OutputTokens,
		CacheRead:  source.CachedReadTokens,
		CacheWrite: source.CacheCreationTokens,
		Reasoning:  source.ReasoningTokens,
	}
	if source.ModelCalls == 1 {
		usage.ContextTokens = source.InputTokens
		return usage.Sanitize()
	}
	return usage, true
}
