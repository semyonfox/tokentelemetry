package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
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
		turns, disagreements := reconcileCopilotUsage(ledger, shutdown)
		for _, turn := range turns {
			emitNative(turn)
		}
		if ledgerErr != nil {
			scanErr = errors.Join(scanErr, ledgerErr)
		}
		if shutdownErr != nil {
			scanErr = errors.Join(scanErr, shutdownErr)
		}
		if disagreements > 0 {
			scanErr = errors.Join(scanErr, fmt.Errorf("Copilot: %d session/model aggregate(s) disagreed with the per-request ledger and replaced it; no usage was inferred", disagreements))
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
func scanCopilotSessionStore(ctx context.Context, dbPath string) ([]model.Turn, error) {
	if !fileExists(dbPath) {
		return nil, nil
	}
	db, err := sql.Open("sqlite", copilotSQLiteDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open Copilot session store: %w", err)
	}
	defer db.Close()

	columns, exists, err := copilotUsageColumns(ctx, db)
	if err != nil || !exists {
		return nil, err
	}
	for _, column := range []string{
		"id", "session_id", "model", "input_tokens", "output_tokens",
		"cache_read_tokens", "cache_write_tokens", "created_at",
	} {
		if !columns[column] {
			return nil, fmt.Errorf("unrecognised Copilot assistant_usage_events layout: missing %s", column)
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
		return nil, fmt.Errorf("query Copilot usage events: %w", err)
	}
	defer rows.Close()

	sourceID := copilotSourceID(dbPath)
	var turns []model.Turn
	compactionWarning := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var row copilotUsageRow
		if err := rows.Scan(
			&row.id, &row.sessionID, &row.model,
			&row.input, &row.output, &row.cacheRead, &row.cacheWrite,
			&row.reasoning, &row.createdAt, &row.initiator,
		); err != nil {
			return nil, fmt.Errorf("read Copilot usage event: %w", err)
		}
		if row.id <= 0 || row.sessionID == "" || strings.TrimSpace(row.model) == "" {
			continue
		}
		timestamp := parseCopilotStoreTime(row.createdAt)
		if timestamp.IsZero() {
			continue
		}
		usage, suspectCacheSplit, ok := normalizeCopilotUsage(row.input, row.output, row.cacheRead, row.cacheWrite, row.reasoning, false)
		if !ok || usage.IsZero() {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(row.initiator), "compaction") && suspectCacheSplit {
			compactionWarning++
		}
		turns = append(turns, model.Turn{
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
		return nil, fmt.Errorf("iterate Copilot usage events: %w", err)
	}
	if compactionWarning > 0 {
		return turns, fmt.Errorf("Copilot session store: %d compaction row(s) have no cache-write breakdown; their total tokens are retained but the fresh/cache split may be incomplete", compactionWarning)
	}
	return turns, nil
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

// reconcileCopilotUsage uses the per-request ledger only when it adds up to
// the native clean-shutdown aggregate for that session and model. A partial or
// changed ledger cannot safely be added to the aggregate, so the exact
// aggregate replaces that group and the caller reports the discrepancy.
func reconcileCopilotUsage(ledger, shutdown []model.Turn) ([]model.Turn, int) {
	ledgerByModel := make(map[string][]model.Turn)
	ledgerOrder := make([]string, 0, len(ledger))
	for _, turn := range ledger {
		key := copilotSessionModelKey(turn.SessionID, turn.Model)
		if _, exists := ledgerByModel[key]; !exists {
			ledgerOrder = append(ledgerOrder, key)
		}
		ledgerByModel[key] = append(ledgerByModel[key], turn)
	}

	shutdownByModel := make(map[string]model.Turn)
	shutdownOrder := make([]string, 0, len(shutdown))
	for _, turn := range shutdown {
		key := copilotSessionModelKey(turn.SessionID, turn.Model)
		previous, exists := shutdownByModel[key]
		if !exists {
			shutdownOrder = append(shutdownOrder, key)
			shutdownByModel[key] = turn
		} else if moreCompleteUsage(turn.Usage, previous.Usage) {
			// A journal can retain repeated cumulative shutdown snapshots. They
			// identify the same session/model, so retain the most complete one.
			shutdownByModel[key] = turn
		}
	}

	var kept []model.Turn
	usedShutdown := make(map[string]bool, len(shutdownByModel))
	disagreements := 0
	for _, key := range ledgerOrder {
		rows := ledgerByModel[key]
		shutdownTurn, hasShutdown := shutdownByModel[key]
		if !hasShutdown || copilotUsageMatchesAggregate(rows, shutdownTurn.Usage) {
			kept = append(kept, rows...)
			continue
		}
		kept = append(kept, shutdownTurn)
		usedShutdown[key] = true
		disagreements++
	}
	for _, key := range shutdownOrder {
		if !usedShutdown[key] {
			if _, hasLedger := ledgerByModel[key]; !hasLedger {
				kept = append(kept, shutdownByModel[key])
			}
		}
	}
	return kept, disagreements
}

func copilotSessionModelKey(sessionID, modelID string) string {
	return identityKey("copilot-session-model", sessionID, strings.ToLower(strings.TrimSpace(modelID)))
}

func copilotUsageMatchesAggregate(rows []model.Turn, aggregate model.Usage) bool {
	var total model.Usage
	for _, row := range rows {
		if !copilotAddUsage(&total, row.Usage) {
			return false
		}
	}
	return total.Input == aggregate.Input &&
		total.Output == aggregate.Output &&
		total.CacheRead == aggregate.CacheRead &&
		total.CacheWrite == aggregate.CacheWrite &&
		total.Reasoning == aggregate.Reasoning
}

func copilotAddUsage(total *model.Usage, next model.Usage) bool {
	buckets := [...]struct {
		total *int64
		next  int64
	}{
		{&total.Input, next.Input},
		{&total.Output, next.Output},
		{&total.CacheRead, next.CacheRead},
		{&total.CacheWrite, next.CacheWrite},
		{&total.Reasoning, next.Reasoning},
	}
	for _, bucket := range buckets {
		if bucket.next < 0 || bucket.next > math.MaxInt64-*bucket.total {
			return false
		}
		*bucket.total += bucket.next
	}
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
	for _, eventPath := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fileTurns, err := scanCopilotShutdownFile(ctx, eventPath)
		if err != nil {
			return nil, err
		}
		turns = append(turns, fileTurns...)
	}
	return turns, nil
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

func scanCopilotShutdownFile(ctx context.Context, eventPath string) ([]model.Turn, error) {
	f, err := os.Open(eventPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	pathSessionID := filepath.Base(filepath.Dir(eventPath))
	var turns []model.Turn
	for line := range jsonLines(f) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var event copilotJournalEvent
		if json.Unmarshal(line, &event) != nil || event.Type != "session.shutdown" {
			continue
		}
		var data copilotShutdownData
		if json.Unmarshal(event.Data, &data) != nil || len(data.ModelMetrics) == 0 {
			continue
		}
		sessionID := firstNonEmpty(data.SessionID, pathSessionID)
		timestamp := parseCopilotStoreTime(event.Timestamp)
		if timestamp.IsZero() {
			timestamp = copilotJSONEpoch(data.SessionStartTime)
		}
		if sessionID == "" || timestamp.IsZero() {
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
			cacheWrite := metric.Usage.CacheWriteTokens
			if cacheWrite == 0 {
				cacheWrite = metric.Usage.CacheCreationTokens
			}
			usage, _, ok := normalizeCopilotUsage(
				metric.Usage.InputTokens, metric.Usage.OutputTokens,
				metric.Usage.CacheReadTokens, cacheWrite, metric.Usage.ReasoningTokens, true,
			)
			if !ok || usage.IsZero() || modelID == "" {
				continue
			}
			turns = append(turns, model.Turn{
				Key:       identityKey("copilot-shutdown", sessionID, modelID),
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
	}
	return turns, nil
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
