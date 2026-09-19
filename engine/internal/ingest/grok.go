package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
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
	return nil
}

type grokUsageFile struct {
	SessionID string          `json:"sessionId"`
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
		CWD string `json:"cwd"`
	} `json:"info"`
	SessionKind     string `json:"session_kind"`
	ParentSessionID string `json:"parent_session_id"`
}

type grokLedger struct {
	sessionID string
	project   string
	kind      string
	parentID  string
	path      string
	modified  time.Time
	usage     grokUsageFile
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
	ledgers := make([]grokLedger, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		ledger, ok := readGrokLedger(path)
		if !ok {
			continue
		}
		ledgers = append(ledgers, ledger)
	}

	bySession := grokSessionIndexes(ledgers)
	omittedParents := grokOmittedParentSessions(ledgers, bySession)
	incomplete := grokIncompleteTurnCount(ledgers, bySession)
	for i, ledger := range ledgers {
		// Atomic rewrites and interrupted restores can leave several copies of
		// one session ledger on disk. The newest durable copy is canonical.
		if bySession[ledger.sessionID] != i {
			continue
		}
		if omittedParents[ledger.sessionID] {
			continue
		}
		child := isGrokChildKind(ledger.kind)
		for _, row := range ledger.usage.Turns {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, turn := range grokTurns(ledger, row, child) {
				emit(turn)
			}
		}
	}
	if incomplete > 0 && len(omittedParents) > 0 {
		return fmt.Errorf("grok: %d persisted turn(s) report incomplete usage; omitted %d parent ledger(s) whose aggregates may overlap retained child usage", incomplete, len(omittedParents))
	}
	if incomplete > 0 {
		return fmt.Errorf("grok: %d persisted turn(s) report incomplete usage", incomplete)
	}
	if len(omittedParents) > 0 {
		return fmt.Errorf("grok: omitted %d parent ledger(s) whose aggregates may overlap retained child usage", len(omittedParents))
	}
	return nil
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

func readGrokLedger(path string) (grokLedger, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return grokLedger{}, false
	}
	usage, ok := decodeGrokJSON[grokUsageFile](path)
	if !ok || usage.SessionID == "" {
		return grokLedger{}, false
	}
	// summary.json enriches accounting with project and delegation metadata,
	// but is not part of the durable usage contract. Missing or torn summaries
	// must not discard a valid ledger.
	summary, _ := decodeGrokJSON[grokSessionSummary](filepath.Join(filepath.Dir(path), "summary.json"))
	return grokLedger{
		sessionID: usage.SessionID,
		project:   summary.Info.CWD,
		kind:      summary.SessionKind,
		parentID:  summary.ParentSessionID,
		path:      path,
		modified:  info.ModTime(),
		usage:     usage,
	}, true
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
		if len(grokTurns(ledger, row, false)) > 0 {
			return true
		}
	}
	return false
}

// grokOmittedParentSessions gives exact child ledgers precedence over their
// parents' mixed aggregates. The native format records no child identity in a
// folded parent row, so neither a complete flag nor a later parent rewrite can
// prove that a particular child was folded. Emitting both can double-count;
// suppressing the child can lose real usage. Omit the ambiguous parent and
// report it instead. The omission propagates to ancestors because their
// aggregates may include the same child usage.
func grokOmittedParentSessions(ledgers []grokLedger, indexes map[string]int) map[string]bool {
	parentByChild := make(map[string]string, len(indexes))
	queue := make([]string, 0, len(indexes))
	for _, childIndex := range indexes {
		child := ledgers[childIndex]
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

func grokTurns(ledger grokLedger, row grokTurnUsage, child bool) []model.Turn {
	ts := parseTime(row.EndedAt)
	if ts.IsZero() {
		return nil
	}

	if len(row.ModelUsage) == 0 {
		if row.PrimaryModelID == "" {
			return nil
		}
		turn, ok := grokTurn(ledger, row, row.PrimaryModelID, row.grokUsageSummary, child, ts)
		if !ok {
			return nil
		}
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
		turn, ok := grokTurn(ledger, row, modelID, usage, child, ts)
		if ok {
			turns = append(turns, turn)
		}
	}
	return turns
}

func grokTurn(ledger grokLedger, row grokTurnUsage, modelID string, source grokUsageSummary, child bool, ts time.Time) (model.Turn, bool) {
	usage, ok := normalizeGrokUsage(source)
	if !ok || usage.IsZero() || modelID == "" {
		return model.Turn{}, false
	}
	return model.Turn{
		Key:       grokTurnKey(row, modelID, source, ts),
		SessionID: ledger.sessionID,
		Agent:     model.AgentGrok,
		Timestamp: ts,
		Model:     modelID,
		Project:   ledger.project,
		Usage:     usage,
		Subagent:  child,
		Aggregate: source.ModelCalls != 1,
	}, true
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
	if source.TotalTokens != 0 && source.TotalTokens != source.InputTokens+source.OutputTokens {
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

// Grok copies usage.json into forks. The fingerprint deliberately excludes
// session and project identity so inherited rows collapse back to one record.
func grokTurnKey(row grokTurnUsage, modelID string, source grokUsageSummary, timestamp time.Time) string {
	material := identityKey("grok-turn-fingerprint",
		strconv.FormatInt(row.TurnNumber, 10), timestamp.UTC().Format(time.RFC3339Nano), modelID,
		strconv.FormatInt(source.InputTokens, 10),
		strconv.FormatInt(source.OutputTokens, 10),
		strconv.FormatInt(source.CachedReadTokens, 10),
		strconv.FormatInt(source.CacheCreationTokens, 10),
		strconv.FormatInt(source.ReasoningTokens, 10),
		strconv.FormatInt(source.ModelCalls, 10),
	)
	sum := sha256.Sum256([]byte(material))
	return fmt.Sprintf("grok|%x", sum)
}
