package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Copilot reads the stores Copilot already writes locally. It never enables
// telemetry or opens a network connection, and it never retains, reports, or
// uses prompt/response fields for accounting.
//
// Newer Copilot CLI and desktop builds record one request per row in
// session-store.db. Older builds have no such table, so completed sessions use
// the durable shutdown rollup in session-state instead. VS Code stores are
// scanned separately in copilot_vscode.go.
type Copilot struct {
	root        string
	vscodeRoots []string
	otelPath    string
}

func NewCopilot() *Copilot {
	return &Copilot{
		root:        envDir("COPILOT_HOME", ".copilot"),
		vscodeRoots: copilotVSCodeUserRoots(),
		otelPath:    copilotOTelExporterPath(),
	}
}

func newCopilotAt(root string) *Copilot { return &Copilot{root: root} }

func newCopilotWithVSCode(root string, vscodeRoots []string) *Copilot {
	return &Copilot{root: root, vscodeRoots: vscodeRoots}
}

func (c *Copilot) Agent() model.Agent { return model.AgentCopilot }

func (c *Copilot) Roots() []string {
	seen := make(map[string]bool)
	var roots []string
	add := func(root string) {
		root = filepath.Clean(root)
		if root != "." && !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	if c.root != "" && (fileExists(filepath.Join(c.root, "session-store.db")) || existingDir(filepath.Join(c.root, "session-state")) != "") {
		add(c.root)
	}
	for _, root := range c.vscodeRoots {
		if existingDir(filepath.Join(root, "workspaceStorage")) != "" ||
			existingDir(filepath.Join(root, "globalStorage", "emptyWindowChatSessions")) != "" ||
			existingDir(filepath.Join(root, "globalStorage", "transferredChatSessions")) != "" {
			add(root)
		}
	}
	if c.otelPath != "" && fileExists(c.otelPath) {
		add(c.otelPath)
	}
	return roots
}

// copilotVSCodeUserRoots returns product data roots, not extension logs. The
// user config directory is platform-aware and honours XDG_CONFIG_HOME.
func copilotVSCodeUserRoots() []string {
	config, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	var roots []string
	for _, product := range []string{"Code", "Code - Insiders", "Code - OSS", "VSCodium"} {
		if root := existingDir(filepath.Join(config, product, "User")); root != "" {
			roots = append(roots, root)
		}
	}
	return roots
}

type copilotLedger struct {
	turns        []model.Turn
	exact        map[string]bool
	invalid      map[string]copilotInvalidPair
	invalidOrder []string
}

type copilotInvalidPair struct {
	session string
	model   string
	reason  string
}

func (c *Copilot) Scan(ctx context.Context, emit func(model.Turn)) error {
	var scanErr error
	nativeSessions := make(map[string]bool)
	emitNative := func(turn model.Turn) {
		nativeSessions[turn.SessionID] = true
		emit(turn)
	}
	if c.root != "" {
		ledger, ledgerErr := scanCopilotSessionStore(ctx, filepath.Join(c.root, "session-store.db"))
		shutdown, shutdownErr := scanCopilotShutdownLogs(ctx, filepath.Join(c.root, "session-state"))
		turns, reconcileErr := reconcileCopilotUsage(ledger, shutdown)
		for _, turn := range turns {
			emitNative(turn)
		}
		if ledgerErr != nil {
			scanErr = errors.Join(scanErr, ledgerErr)
		}
		if shutdownErr != nil {
			scanErr = errors.Join(scanErr, shutdownErr)
		}
		if reconcileErr != nil {
			scanErr = errors.Join(scanErr, reconcileErr)
		}
	}
	if len(c.vscodeRoots) > 0 {
		_, err := scanCopilotVSCode(ctx, c.vscodeRoots, emitNative)
		if err != nil {
			scanErr = errors.Join(scanErr, err)
		}
	}
	if c.otelPath != "" {
		exported, err := scanCopilotOTel(ctx, c.otelPath)
		if err != nil {
			scanErr = errors.Join(scanErr, err)
		}
		for _, turn := range exported {
			// The exporter and native ledger have no common per-request ID.
			// Choose one source for the whole conversation, including when
			// request and response model names differ between the two stores.
			if !nativeSessions[turn.SessionID] {
				emit(turn)
			}
		}
	}
	return scanErr
}

// scanCopilotSessionStore reads only the native usage table. A missing or
// pre-ledger database is not an error: its session journal can still supply a
// completed aggregate for anything the per-request table did not cover.
func scanCopilotSessionStore(ctx context.Context, dbPath string) (copilotLedger, error) {
	empty := copilotLedger{exact: make(map[string]bool), invalid: make(map[string]copilotInvalidPair)}
	if !fileExists(dbPath) {
		return empty, nil
	}
	db, err := sql.Open("sqlite", copilotSQLiteDSN(dbPath))
	if err != nil {
		return empty, fmt.Errorf("open Copilot session store: %w", err)
	}
	defer db.Close()

	columns, exists, err := copilotUsageColumns(ctx, db)
	if err != nil || !exists {
		return empty, err
	}
	for _, column := range []string{
		"id", "session_id", "model", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_write_tokens", "created_at",
	} {
		if !columns[column] {
			return empty, fmt.Errorf("unrecognised Copilot assistant_usage_events layout: missing %s", column)
		}
	}

	initiator := "''"
	if columns["initiator"] {
		initiator = "COALESCE(initiator, '')"
	}
	// reasoning_tokens was added after the core per-request counters. It is a
	// useful subset of output when present, but an older otherwise-complete
	// ledger must not fail just because it cannot break that subset out.
	reasoning := "0"
	if columns["reasoning_tokens"] {
		reasoning = "CAST(COALESCE(reasoning_tokens, 0) AS INTEGER)"
	}
	query := `SELECT
		id,
		COALESCE(session_id, ''),
		COALESCE(model, ''),
		CAST(COALESCE(input_tokens, 0) AS INTEGER),
		CAST(COALESCE(output_tokens, 0) AS INTEGER),
		CAST(COALESCE(cache_read_tokens, 0) AS INTEGER),
		CAST(COALESCE(cache_write_tokens, 0) AS INTEGER),
		` + reasoning + `,
		COALESCE(CAST(created_at AS TEXT), ''), ` + initiator + `
		FROM assistant_usage_events
		ORDER BY id`
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return empty, fmt.Errorf("query Copilot usage events: %w", err)
	}
	defer rows.Close()

	sourceID := copilotSourceID(dbPath)
	groups := make(map[string][]model.Turn)
	groupOrder := make([]string, 0)
	invalid := make(map[string]copilotInvalidPair)
	invalidOrder := make([]string, 0)
	compactionWarning := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		var row copilotUsageRow
		if err := rows.Scan(
			&row.id, &row.sessionID, &row.model,
			&row.input, &row.output, &row.cacheRead, &row.cacheWrite,
			&row.reasoning, &row.createdAt, &row.initiator,
		); err != nil {
			return empty, fmt.Errorf("read Copilot usage event: %w", err)
		}
		if row.id <= 0 || row.sessionID == "" || strings.TrimSpace(row.model) == "" {
			continue
		}
		row.model = strings.TrimSpace(row.model)
		pair := copilotSessionModelKey(row.sessionID, row.model)
		if _, exists := groups[pair]; !exists {
			groupOrder = append(groupOrder, pair)
			groups[pair] = nil
		}
		timestamp := parseCopilotStoreTime(row.createdAt)
		if timestamp.IsZero() {
			if _, exists := invalid[pair]; !exists {
				invalidOrder = append(invalidOrder, pair)
			}
			invalid[pair] = copilotInvalidPair{session: row.sessionID, model: row.model, reason: "request timestamp is missing or invalid"}
			continue
		}
		usage, suspectCacheSplit, ok := normalizeCopilotUsage(row.input, row.output, row.cacheRead, row.cacheWrite, row.reasoning, false)
		if !ok {
			if _, exists := invalid[pair]; !exists {
				invalidOrder = append(invalidOrder, pair)
			}
			invalid[pair] = copilotInvalidPair{session: row.sessionID, model: row.model, reason: "token buckets do not reconcile"}
			continue
		}
		if usage.IsZero() {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(row.initiator), "compaction") && suspectCacheSplit {
			compactionWarning++
		}
		groups[pair] = append(groups[pair], model.Turn{
			Key:       copilotUsageKey(sourceID, row),
			SessionID: row.sessionID,
			Agent:     model.AgentCopilot,
			Timestamp: timestamp,
			Model:     strings.TrimSpace(row.model),
			Provider:  "github-copilot",
			Usage:     usage,
		})
	}
	if err := rows.Err(); err != nil {
		return empty, fmt.Errorf("iterate Copilot usage events: %w", err)
	}
	ledger := copilotLedger{exact: make(map[string]bool), invalid: invalid, invalidOrder: invalidOrder}
	for _, pair := range groupOrder {
		if _, bad := invalid[pair]; bad {
			continue
		}
		ledger.exact[pair] = true
		ledger.turns = append(ledger.turns, groups[pair]...)
	}
	if compactionWarning > 0 {
		return ledger, fmt.Errorf("Copilot session store: %d compaction row(s) have no cache-write breakdown; their total tokens are retained but the fresh/cache split may be incomplete", compactionWarning)
	}
	return ledger, nil
}

type copilotUsageRow struct {
	id                                     int64
	sessionID, model, createdAt, initiator string
	input, output, cacheRead, cacheWrite   int64
	reasoning                              int64
}

func copilotUsageColumns(ctx context.Context, db *sql.DB) (map[string]bool, bool, error) {
	var present int
	err := db.QueryRowContext(ctx, "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'assistant_usage_events' LIMIT 1").Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(assistant_usage_events)")
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, false, err
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return columns, true, nil
}

func copilotSQLiteDSN(dbPath string) string {
	uriPath := filepath.ToSlash(dbPath)
	if filepath.VolumeName(dbPath) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{
		Scheme:   "file",
		Path:     uriPath,
		RawQuery: "mode=ro&_pragma=busy_timeout(3000)&_pragma=query_only(ON)&_pragma=trusted_schema(OFF)",
	}).String()
}

// normalizeCopilotUsage turns Copilot's cache-inclusive input count into
// disjoint buckets. The native records call reasoning a subset of output.
func normalizeCopilotUsage(grossInput, output, cacheRead, cacheWrite, reasoning int64, aggregate bool) (model.Usage, bool, bool) {
	for _, value := range []*int64{&grossInput, &output, &cacheRead, &cacheWrite, &reasoning} {
		if *value < 0 {
			*value = 0
		}
	}
	if cacheRead > grossInput || cacheWrite > grossInput-cacheRead || reasoning > output {
		return model.Usage{}, false, false
	}
	usage := model.Usage{
		Input:         grossInput - cacheRead - cacheWrite,
		Output:        output,
		CacheRead:     cacheRead,
		CacheWrite:    cacheWrite,
		Reasoning:     reasoning,
		ContextTokens: grossInput,
	}
	var ok bool
	if aggregate {
		usage, ok = sanitizeAggregateUsage(usage)
		usage.ContextTokens = 0
	} else {
		usage, ok = usage.Sanitize()
	}
	if !ok {
		return model.Usage{}, false, false
	}
	// The current scalar cache-write counter is occasionally absent for a
	// compaction request even when the raw input was cache-inclusive. Do not
	// invent a private token-details parser: retain the total and disclose it.
	return usage, cacheWrite == 0 && grossInput > cacheRead, true
}

func parseCopilotStoreTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if timestamp := parseTime(raw); !timestamp.IsZero() {
		return timestamp
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999",
	} {
		if timestamp, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return timestamp
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return copilotEpoch(value)
}

func copilotEpoch(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	var seconds, nanos int64
	switch {
	case value >= 1_000_000_000_000_000_000:
		seconds, nanos = value/1_000_000_000, value%1_000_000_000
	case value >= 1_000_000_000_000_000:
		seconds, nanos = value/1_000_000, (value%1_000_000)*1_000
	case value >= 1_000_000_000_000:
		seconds, nanos = value/1_000, (value%1_000)*1_000_000
	default:
		seconds = value
	}
	if seconds > 253_402_300_799 {
		return time.Time{}
	}
	return time.Unix(seconds, nanos).UTC()
}

func copilotUsageKey(sourceID string, row copilotUsageRow) string {
	material := strings.Join([]string{
		row.sessionID, row.model, row.createdAt,
		strconv.FormatInt(row.input, 10), strconv.FormatInt(row.output, 10),
		strconv.FormatInt(row.cacheRead, 10), strconv.FormatInt(row.cacheWrite, 10),
		strconv.FormatInt(row.reasoning, 10),
	}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return identityKey("copilot-store", sourceID, strconv.FormatInt(row.id, 10), fmt.Sprintf("%x", sum[:]))
}

func copilotSourceID(dbPath string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(dbPath)))
	return fmt.Sprintf("%x", sum[:])
}

// reconcileCopilotUsage keeps valid per-request rows authoritative and uses
// shutdown snapshots only for history those rows provably do not cover. The
// subtraction is componentwise: a mixed-direction mismatch is disclosed and
// excluded instead of being turned into a positive-only residual.
func reconcileCopilotUsage(ledger copilotLedger, shutdown []model.Turn) ([]model.Turn, error) {
	ledgerByPair := make(map[string][]model.Turn)
	ledgerBySession := make(map[string][]model.Turn)
	for _, turn := range ledger.turns {
		pair := copilotSessionModelKey(turn.SessionID, turn.Model)
		ledgerByPair[pair] = append(ledgerByPair[pair], turn)
		ledgerBySession[turn.SessionID] = append(ledgerBySession[turn.SessionID], turn)
	}

	shutdownByPair := make(map[string][]model.Turn)
	shutdownBySession := make(map[string][]model.Turn)
	shutdownOrder := make([]string, 0, len(shutdown))
	for _, turn := range shutdown {
		pair := copilotSessionModelKey(turn.SessionID, turn.Model)
		if _, exists := shutdownByPair[pair]; !exists {
			shutdownOrder = append(shutdownOrder, pair)
		}
		shutdownByPair[pair] = append(shutdownByPair[pair], turn)
		shutdownBySession[turn.SessionID] = append(shutdownBySession[turn.SessionID], turn)
	}

	invalidSessions := make(map[string]bool)
	for _, invalid := range ledger.invalid {
		invalidSessions[invalid.session] = true
	}
	ambiguousSessions := make(map[string]bool)
	for pair, rows := range ledgerByPair {
		if len(shutdownByPair[pair]) > 0 || invalidSessions[rows[0].SessionID] {
			continue
		}
		shutdownRows := shutdownBySession[rows[0].SessionID]
		if len(shutdownRows) == 0 {
			continue
		}
		exact, ok := copilotUsageThrough(rows, latestCopilotTurnTime(shutdownRows))
		if !ok {
			ambiguousSessions[rows[0].SessionID] = true
			continue
		}
		if !exact.IsZero() {
			ambiguousSessions[rows[0].SessionID] = true
		}
	}

	kept := append([]model.Turn(nil), ledger.turns...)
	var reconcileErr error
	handledAmbiguous := make(map[string]bool)
	usedInvalidFallback := make(map[string]bool)
	for _, pair := range shutdownOrder {
		rows := shutdownByPair[pair]
		session := rows[0].SessionID
		if ambiguousSessions[session] {
			if handledAmbiguous[session] {
				continue
			}
			handledAmbiguous[session] = true
			exact, ok := copilotUsageThrough(ledgerBySession[session], latestCopilotTurnTime(shutdownBySession[session]))
			if !ok {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Copilot session %q exact rows overflow aggregate accounting; exact rows kept", session))
				continue
			}
			residual, err := copilotShutdownResidual(session, "unknown", exact, shutdownBySession[session])
			if err != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Copilot session %q store and shutdown model identifiers differ: %w", session, err))
				continue
			}
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Copilot session %q store and shutdown model identifiers differ; reconciled the uncovered total without model attribution", session))
			if residual != nil {
				residual.Key = identityKey("copilot-shutdown-residual", session, "model-mismatch")
				residual.Model = "unknown"
				residual.UnpricedReason = "Copilot store and shutdown model identifiers differ; residual model attribution is unavailable"
				kept = append(kept, *residual)
			}
			continue
		}

		if invalid, bad := ledger.invalid[pair]; bad {
			kept = append(kept, rows...)
			usedInvalidFallback[pair] = true
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Copilot session %q model %q has an invalid request row (%s); used its shutdown aggregate", invalid.session, invalid.model, invalid.reason))
			continue
		}
		if !ledger.exact[pair] {
			kept = append(kept, rows...)
			continue
		}

		exact, ok := copilotUsageThrough(ledgerByPair[pair], latestCopilotTurnTime(rows))
		if !ok {
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Copilot session %q model %q exact rows overflow aggregate accounting; exact rows kept", session, rows[0].Model))
			continue
		}
		residual, err := copilotShutdownResidual(session, rows[0].Model, exact, rows)
		if err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
			continue
		}
		if residual != nil {
			kept = append(kept, *residual)
		}
	}
	for _, pair := range ledger.invalidOrder {
		if usedInvalidFallback[pair] {
			continue
		}
		invalid := ledger.invalid[pair]
		reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Copilot session %q model %q has an invalid request row (%s) and no shutdown aggregate; request group excluded", invalid.session, invalid.model, invalid.reason))
	}
	return kept, reconcileErr
}

func copilotSessionModelKey(sessionID, modelID string) string {
	return identityKey("copilot-session-model", sessionID, strings.ToLower(strings.TrimSpace(modelID)))
}

func latestCopilotTurnTime(turns []model.Turn) time.Time {
	var latest time.Time
	for _, turn := range turns {
		if turn.Timestamp.After(latest) {
			latest = turn.Timestamp
		}
	}
	return latest
}

func copilotUsageThrough(rows []model.Turn, cutoff time.Time) (model.Usage, bool) {
	var total model.Usage
	for _, row := range rows {
		if row.Timestamp.After(cutoff) {
			continue
		}
		if !copilotAddUsage(&total, row.Usage) {
			return model.Usage{}, false
		}
	}
	return total, true
}

func copilotShutdownResidual(sessionID, modelID string, exact model.Usage, shutdown []model.Turn) (*model.Turn, error) {
	var cumulative model.Usage
	latest := shutdown[0]
	for _, turn := range shutdown {
		if !copilotAddUsage(&cumulative, turn.Usage) {
			return nil, fmt.Errorf("Copilot session %q model %q shutdown totals overflow aggregate accounting; exact rows kept", sessionID, modelID)
		}
		if turn.Timestamp.After(latest.Timestamp) {
			latest = turn
		}
	}
	if exact.Input > cumulative.Input || exact.Output > cumulative.Output ||
		exact.CacheRead > cumulative.CacheRead || exact.CacheWrite > cumulative.CacheWrite ||
		exact.Reasoning > cumulative.Reasoning {
		return nil, fmt.Errorf("Copilot session %q model %q request rows do not reconcile componentwise with shutdown totals; exact rows kept", sessionID, modelID)
	}
	usage := model.Usage{
		Input:      cumulative.Input - exact.Input,
		Output:     cumulative.Output - exact.Output,
		CacheRead:  cumulative.CacheRead - exact.CacheRead,
		CacheWrite: cumulative.CacheWrite - exact.CacheWrite,
		Reasoning:  cumulative.Reasoning - exact.Reasoning,
	}
	if usage.Reasoning > usage.Output {
		return nil, fmt.Errorf("Copilot session %q model %q residual reasoning exceeds residual output; exact rows kept", sessionID, modelID)
	}
	if usage.IsZero() {
		return nil, nil
	}
	latest.Key = identityKey("copilot-shutdown-residual", sessionID, strings.ToLower(strings.TrimSpace(modelID)))
	latest.Usage = usage
	latest.Aggregate = true
	return &latest, nil
}

func copilotAddUsage(total *model.Usage, next model.Usage) bool {
	next, ok := next.SanitizeAggregate()
	if !ok {
		return false
	}
	candidate := *total
	candidate.Add(next)
	candidate, ok = candidate.SanitizeAggregate()
	if !ok {
		return false
	}
	*total = candidate
	return true
}

// session.shutdown is written to the native journal after a clean session
// close. Its modelMetrics counters are exact per-model aggregates, but lack a
// per-request context size, so they deliberately use base pricing.
func scanCopilotShutdownLogs(ctx context.Context, stateRoot string) ([]model.Turn, error) {
	if existingDir(stateRoot) == "" {
		return nil, nil
	}
	var paths []string
	err := filepath.WalkDir(stateRoot, func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil || entry.IsDir() || entry.Name() != "events.jsonl" {
			return nil
		}
		paths = append(paths, entryPath)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var turns []model.Turn
	var scanErr error
	for _, eventPath := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fileTurns, err := scanCopilotShutdownFile(ctx, eventPath)
		if err != nil {
			scanErr = errors.Join(scanErr, err)
		}
		turns = append(turns, fileTurns...)
	}
	return turns, scanErr
}

type copilotJournalEvent struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	AgentID   string          `json:"agentId"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type copilotShutdownData struct {
	SessionID        string                        `json:"sessionId"`
	SessionStartTime json.RawMessage               `json:"sessionStartTime"`
	ModelMetrics     map[string]copilotModelMetric `json:"modelMetrics"`
}

type copilotModelMetric struct {
	Model string                   `json:"model"`
	Usage copilotJournalTokenUsage `json:"usage"`
}

type copilotJournalTokenUsage struct {
	InputTokens         int64 `json:"inputTokens"`
	OutputTokens        int64 `json:"outputTokens"`
	CacheReadTokens     int64 `json:"cacheReadTokens"`
	CacheWriteTokens    int64 `json:"cacheWriteTokens"`
	CacheCreationTokens int64 `json:"cacheCreationTokens"`
	ReasoningTokens     int64 `json:"reasoningTokens"`
}

type copilotCumulativeUsage struct {
	input, output, cacheRead, cacheWrite, reasoning int64
}

func scanCopilotShutdownFile(ctx context.Context, eventPath string) ([]model.Turn, error) {
	f, err := os.Open(eventPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	pathSessionID := filepath.Base(filepath.Dir(eventPath))
	previous := make(map[string]copilotCumulativeUsage)
	sequence := make(map[string]int)
	lastTimestamp := time.Time{}
	resetPending := false
	var turns []model.Turn
	var scanErr error
	for line := range jsonLines(f) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var event copilotJournalEvent
		if json.Unmarshal(line, &event) != nil {
			continue
		}
		if timestamp := parseCopilotStoreTime(event.Timestamp); !timestamp.IsZero() {
			lastTimestamp = timestamp
		}
		if event.Type == "session.compaction_complete" {
			var data struct {
				Success bool `json:"success"`
			}
			if json.Unmarshal(event.Data, &data) == nil && data.Success {
				previous = make(map[string]copilotCumulativeUsage)
				resetPending = true
			}
			continue
		}
		if event.Type != "session.shutdown" {
			continue
		}
		var data copilotShutdownData
		if json.Unmarshal(event.Data, &data) != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("Copilot session %q has an unreadable shutdown snapshot", pathSessionID))
			continue
		}
		if len(data.ModelMetrics) == 0 {
			continue
		}
		sessionID := firstNonEmpty(data.SessionID, pathSessionID)
		timestamp := parseCopilotStoreTime(event.Timestamp)
		if timestamp.IsZero() {
			timestamp = lastTimestamp
		}
		if timestamp.IsZero() {
			timestamp = copilotJSONEpoch(data.SessionStartTime)
		}
		if sessionID == "" {
			continue
		}
		models := make([]string, 0, len(data.ModelMetrics))
		for modelID := range data.ModelMetrics {
			models = append(models, modelID)
		}
		sort.Strings(models)
		for _, mapModel := range models {
			metric := data.ModelMetrics[mapModel]
			modelID := firstNonEmpty(strings.TrimSpace(metric.Model), strings.TrimSpace(mapModel))
			if modelID == "" {
				modelID = "unknown"
			}
			cacheWrite := metric.Usage.CacheWriteTokens
			if cacheWrite == 0 {
				cacheWrite = metric.Usage.CacheCreationTokens
			}
			current := copilotCumulativeUsage{
				input: metric.Usage.InputTokens, output: metric.Usage.OutputTokens,
				cacheRead: metric.Usage.CacheReadTokens, cacheWrite: cacheWrite,
				reasoning: metric.Usage.ReasoningTokens,
			}
			modelKey := strings.ToLower(modelID)
			prior, hasPrior := previous[modelKey]
			delta := current
			if hasPrior {
				decreased, increased := copilotCounterDirections(current, prior)
				switch {
				case decreased && increased:
					scanErr = errors.Join(scanErr, fmt.Errorf("Copilot session %q model %q has mixed-direction cumulative shutdown counters; ambiguous snapshot excluded", sessionID, modelID))
					continue
				case decreased:
					scanErr = errors.Join(scanErr, fmt.Errorf("Copilot session %q model %q reset cumulative shutdown counters; aggregate history may be incomplete", sessionID, modelID))
				default:
					delta = copilotCumulativeDelta(current, prior)
				}
			}
			if resetPending {
				scanErr = errors.Join(scanErr, fmt.Errorf("Copilot session %q compacted before a shutdown aggregate; pre-compaction usage may be missing", sessionID))
			}
			if timestamp.IsZero() {
				scanErr = errors.Join(scanErr, fmt.Errorf("Copilot session %q model %q has no usable shutdown timestamp", sessionID, modelID))
				continue
			}
			usage, reason := copilotAggregateUsage(delta)
			if reason != "" {
				scanErr = errors.Join(scanErr, fmt.Errorf("Copilot session %q model %q has invalid shutdown usage (%s)", sessionID, modelID, reason))
				continue
			}
			// Only an accepted cumulative snapshot may become the next delta
			// baseline. Otherwise a torn or corrected row can make later valid
			// usage overlap with history that was already emitted.
			previous[modelKey] = current
			if usage.IsZero() {
				continue
			}
			sequence[modelKey]++
			key := identityKey("copilot-shutdown", sessionID, modelID)
			if sequence[modelKey] > 1 {
				key = identityKey("copilot-shutdown", sessionID, modelID, strconv.Itoa(sequence[modelKey]))
			}
			turns = append(turns, model.Turn{
				Key:       key,
				SessionID: sessionID,
				Agent:     model.AgentCopilot,
				Timestamp: timestamp,
				Model:     modelID,
				Provider:  "github-copilot",
				Usage:     usage,
				Subagent:  event.AgentID != "",
				Aggregate: true,
			})
		}
		resetPending = false
	}
	return turns, scanErr
}

func copilotCounterDirections(current, previous copilotCumulativeUsage) (decreased, increased bool) {
	currentBuckets := [...]int64{current.input, current.output, current.cacheRead, current.cacheWrite, current.reasoning}
	previousBuckets := [...]int64{previous.input, previous.output, previous.cacheRead, previous.cacheWrite, previous.reasoning}
	for i := range currentBuckets {
		decreased = decreased || currentBuckets[i] < previousBuckets[i]
		increased = increased || currentBuckets[i] > previousBuckets[i]
	}
	return decreased, increased
}

func copilotCumulativeDelta(current, previous copilotCumulativeUsage) copilotCumulativeUsage {
	return copilotCumulativeUsage{
		input: current.input - previous.input, output: current.output - previous.output,
		cacheRead:  current.cacheRead - previous.cacheRead,
		cacheWrite: current.cacheWrite - previous.cacheWrite,
		reasoning:  current.reasoning - previous.reasoning,
	}
}

func copilotAggregateUsage(raw copilotCumulativeUsage) (model.Usage, string) {
	for _, count := range []int64{raw.input, raw.output, raw.cacheRead, raw.cacheWrite, raw.reasoning} {
		if count < 0 {
			return model.Usage{}, "negative cumulative delta"
		}
	}
	if raw.cacheRead > raw.input || raw.cacheWrite > raw.input-raw.cacheRead {
		return model.Usage{}, "cache tokens exceed cache-inclusive input"
	}
	if raw.reasoning > raw.output {
		return model.Usage{}, "reasoning tokens exceed output tokens"
	}
	usage, ok := (model.Usage{
		Input: raw.input - raw.cacheRead - raw.cacheWrite, Output: raw.output,
		CacheRead: raw.cacheRead, CacheWrite: raw.cacheWrite, Reasoning: raw.reasoning,
	}).SanitizeAggregate()
	if !ok {
		return model.Usage{}, "usage overflows aggregate accounting"
	}
	return usage, ""
}

func copilotJSONEpoch(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return parseCopilotStoreTime(text)
	}
	var value int64
	if json.Unmarshal(raw, &value) == nil {
		return copilotEpoch(value)
	}
	return time.Time{}
}
