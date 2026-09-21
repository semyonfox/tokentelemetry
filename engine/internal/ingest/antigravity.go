package ingest

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
	"unicode/utf8"

	_ "modernc.org/sqlite" // pure-Go driver: keeps CGO_ENABLED=0 cross-compiles working

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Antigravity reads per-invocation counters from local conversation databases.
// An explicit streamRoot switches it to saved public headless results instead;
// the two ledgers can describe the same calls and must not be added together.
type Antigravity struct {
	roots      []string
	streamRoot string
}

const (
	antigravityDatabaseVersion = 1
	// Generator metadata embeds prompts, messages and tool definitions alongside
	// its small accounting fields. Current native records can legitimately exceed
	// 4 MiB, so bound the complete protobuf at a size that covers those records
	// while retaining only the field paths used for accounting below.
	antigravityMaxMetadataBytes = 64 << 20
	antigravityMaxProtoFields   = 8_192
	antigravityMaxModelBytes    = 256
	antigravityMaxIDBytes       = 1_024
)

func NewAntigravity() *Antigravity {
	if configured := os.Getenv("TT_ANTIGRAVITY_DIR"); configured != "" {
		return &Antigravity{streamRoot: existingDir(configured)}
	}
	home := homeDir()
	if home == "" {
		return &Antigravity{}
	}

	var roots []string
	for _, root := range []string{
		filepath.Join(home, ".gemini", "antigravity"),
		filepath.Join(home, ".gemini", "antigravity-cli"),
		filepath.Join(home, ".gemini", "antigravity-ide"),
		filepath.Join(home, ".gemini", "antigravity-backup"),
		filepath.Join(home, ".config", "antigravity"),
	} {
		if conversations := existingDir(filepath.Join(root, "conversations")); conversations != "" {
			roots = append(roots, conversations)
		} else if direct := existingDir(root); direct != "" {
			roots = append(roots, direct)
		}
	}
	return &Antigravity{roots: roots}
}

func (a *Antigravity) Agent() model.Agent { return model.AgentAntigravity }

func (a *Antigravity) Roots() []string {
	if a.streamRoot != "" {
		return []string{a.streamRoot}
	}
	return append([]string(nil), a.roots...)
}

func (a *Antigravity) Scan(ctx context.Context, emit func(model.Turn)) error {
	if a.streamRoot != "" {
		return scanAntigravityStreams(ctx, a.streamRoot, emit)
	}
	var candidates []antigravityCandidate
	var scanErr error
	paths, err := antigravityDBPaths(ctx, a.roots)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		scanErr = errors.Join(scanErr, err)
	}
	for _, path := range paths {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		found, skipped, err := scanAntigravityDB(ctx, path)
		candidates = append(candidates, found...)
		if skipped > 0 {
			scanErr = errors.Join(scanErr, fmt.Errorf("%s: skipped %d Antigravity metadata blob(s) larger than %d MiB", path, skipped, antigravityMaxMetadataBytes>>20))
		}
		if err != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("%s: %w", path, err))
		}
	}
	for _, turn := range mergeAntigravityCandidates(candidates) {
		emit(turn)
	}
	return scanErr
}

func antigravityDBPaths(ctx context.Context, roots []string) ([]string, error) {
	seen := make(map[string]bool)
	var paths []string
	var walkErr error
	for _, root := range roots {
		// WalkDir deliberately does not follow directory links. Antigravity's
		// state root is commonly relocated, so resolve just the configured root
		// while keeping nested links out of scope.
		walkRoot := root
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			walkRoot = resolved
		}
		err := filepath.WalkDir(walkRoot, func(path string, entry fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				// A removed or unreadable conversation must not hide the rest.
				return nil
			}
			if entry.IsDir() || entry.Type()&fs.ModeSymlink != 0 || filepath.Ext(entry.Name()) != ".db" {
				return nil
			}
			path = filepath.Clean(path)
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			walkErr = errors.Join(walkErr, fmt.Errorf("%s: %w", root, err))
		}
	}
	sort.Strings(paths)
	return paths, walkErr
}

type antigravityTable struct {
	name   string
	column string
}

var (
	antigravityGenerationTable = antigravityTable{name: "gen_metadata", column: "data"}
	antigravityStepTable       = antigravityTable{name: "steps", column: "metadata"}
)

func scanAntigravityDB(ctx context.Context, path string) ([]antigravityCandidate, int64, error) {
	// Keep the database strictly read-only while still observing a live WAL.
	// The timeout lets a concurrent Antigravity write finish before the query.
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{
		Scheme:   "file",
		Path:     uriPath,
		RawQuery: "mode=ro&_pragma=busy_timeout(3000)&_pragma=query_only(ON)&_pragma=trusted_schema(OFF)",
	}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()

	valid, err := antigravityExpectedTable(ctx, db, antigravityGenerationTable, "idx", "data")
	if err != nil || !valid {
		return nil, 0, err
	}
	var version int64
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return nil, 0, err
	}
	if version != antigravityDatabaseVersion {
		return nil, 0, fmt.Errorf("unsupported Antigravity conversation database version %d", version)
	}

	sessionID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	sourceID := antigravitySourceID(path)
	generationEvents, skipped, err := scanAntigravityGenerations(ctx, db, sessionID, sourceID)
	if err != nil {
		return antigravityCandidates(generationEvents), skipped, err
	}
	stepTimestamps, stepSkipped, err := scanAntigravityStepTimestamps(ctx, db)
	skipped += stepSkipped
	if err != nil {
		return antigravityCandidates(generationEvents), skipped, err
	}
	dateAntigravityGenerationEvents(generationEvents, stepTimestamps)
	return antigravityCandidates(generationEvents), skipped, nil
}

// antigravitySourceID makes a no-message fallback identity unique to its
// database without placing a local filesystem path in reportable turn data.
func antigravitySourceID(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return fmt.Sprintf("%x", sum)
}

// antigravityExpectedTable excludes views, virtual tables and unrelated SQLite
// files before a query names a private table. The expected columns plus
// user_version form the compatibility gate for the undocumented store.
func antigravityExpectedTable(ctx context.Context, db *sql.DB, table antigravityTable, required ...string) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ? LIMIT 1", table.name).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	// table.name is selected exclusively from the two package constants above.
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table.name+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		columns[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, name := range required {
		if !columns[name] {
			return false, fmt.Errorf("unrecognised Antigravity %s table layout", table.name)
		}
	}
	return true, nil
}

func scanAntigravityGenerations(ctx context.Context, db *sql.DB, sessionID, sourceID string) ([]antigravityUsageEvent, int64, error) {
	var events []antigravityUsageEvent
	skipped, err := scanAntigravityMetadataRows(ctx, db, antigravityGenerationTable, func(idx int64, data []byte) {
		explicitModel, explicitModelID, timestamp, executionID, usages, ok := parseAntigravityGeneration(data)
		if !ok {
			return
		}
		for ordinal, usage := range usages {
			events = append(events, antigravityUsageEvent{
				sessionID: sessionID, sourceID: sourceID, timestamp: timestamp, executionID: executionID, model: explicitModel, contextModelID: explicitModelID,
				sourceTable: antigravityGenerationTable.name, sourceIndex: idx, usageOrdinal: ordinal, usage: usage,
			})
		}
	})
	return events, skipped, err
}

type antigravityStepTimeMetadata struct {
	timestamp   time.Time
	responseID  string
	executionID string
}

type antigravityStepTimestamps struct {
	byResponse          map[string]time.Time
	ambiguousResponses  map[string]bool
	byExecution         map[string]time.Time
	ambiguousExecutions map[string]bool
}

func scanAntigravityStepTimestamps(ctx context.Context, db *sql.DB) (antigravityStepTimestamps, int64, error) {
	var timestamps antigravityStepTimestamps
	valid, err := antigravityExpectedTable(ctx, db, antigravityStepTable, "idx", "metadata")
	if err != nil || !valid {
		return timestamps, 0, err
	}

	skipped, err := scanAntigravityMetadataRows(ctx, db, antigravityStepTable, func(_ int64, data []byte) {
		timestamp, ok := parseAntigravityStepTimestamp(data)
		if !ok {
			return
		}
		timestamps.observe(timestamp)
	})
	return timestamps, skipped, err
}

func (t *antigravityStepTimestamps) observe(step antigravityStepTimeMetadata) {
	if t.byResponse == nil {
		t.byResponse = make(map[string]time.Time)
		t.ambiguousResponses = make(map[string]bool)
		t.byExecution = make(map[string]time.Time)
		t.ambiguousExecutions = make(map[string]bool)
	}
	if step.responseID != "" && !t.ambiguousResponses[step.responseID] {
		if existing, found := t.byResponse[step.responseID]; found && !existing.Equal(step.timestamp) {
			delete(t.byResponse, step.responseID)
			t.ambiguousResponses[step.responseID] = true
		} else {
			t.byResponse[step.responseID] = step.timestamp
		}
	}
	if step.executionID != "" && !t.ambiguousExecutions[step.executionID] {
		if existing, found := t.byExecution[step.executionID]; found && !existing.Equal(step.timestamp) {
			delete(t.byExecution, step.executionID)
			t.ambiguousExecutions[step.executionID] = true
		} else {
			t.byExecution[step.executionID] = step.timestamp
		}
	}
}

// dateAntigravityGenerationEvents fills an absent generation timestamp from a
// step only when its native response or execution ID maps to one timestamp.
// Conflicting local metadata remains undated rather than being assigned to a
// different invocation.
func dateAntigravityGenerationEvents(events []antigravityUsageEvent, timestamps antigravityStepTimestamps) {
	for index := range events {
		event := &events[index]
		if !event.timestamp.IsZero() {
			continue
		}
		if event.usage.responseID != "" && !timestamps.ambiguousResponses[event.usage.responseID] {
			if timestamp, found := timestamps.byResponse[event.usage.responseID]; found {
				event.timestamp = timestamp
			}
		}
		if event.timestamp.IsZero() && event.executionID != "" && !timestamps.ambiguousExecutions[event.executionID] {
			if timestamp, found := timestamps.byExecution[event.executionID]; found {
				event.timestamp = timestamp
			}
		}
	}
}

func scanAntigravityMetadataRows(ctx context.Context, db *sql.DB, table antigravityTable, handle func(int64, []byte)) (int64, error) {
	query := "SELECT idx, " + table.column + " FROM " + table.name + " WHERE typeof(" + table.column + ") = 'blob' AND length(" + table.column + ") BETWEEN 1 AND ? ORDER BY idx"
	rows, err := db.QueryContext(ctx, query, antigravityMaxMetadataBytes)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var idx int64
		var data []byte
		if err := rows.Scan(&idx, &data); err != nil {
			return 0, err
		}
		handle(idx, data)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	var skipped int64
	countQuery := "SELECT count(*) FROM " + table.name + " WHERE typeof(" + table.column + ") = 'blob' AND length(" + table.column + ") > ?"
	if err := db.QueryRowContext(ctx, countQuery, antigravityMaxMetadataBytes).Scan(&skipped); err != nil {
		return 0, err
	}
	return skipped, nil
}

type antigravityModel struct {
	id       string
	provider string
	rank     uint8
}

func antigravityNumericModel(id uint64) antigravityModel {
	return antigravityModel{id: "antigravity-model-" + strconv.FormatUint(id, 10)}
}

type antigravityUsage struct {
	usage      model.Usage
	modelID    uint64
	responseID string
}

type antigravityUsageEvent struct {
	sessionID      string
	sourceID       string
	timestamp      time.Time
	executionID    string
	model          antigravityModel
	contextModelID uint64
	sourceTable    string
	sourceIndex    int64
	usageOrdinal   int
	usage          antigravityUsage
}

type antigravityCandidate struct {
	turn      model.Turn
	aliases   []string
	modelRank uint8
	keyRank   uint8
}

func parseAntigravityGeneration(data []byte) (antigravityModel, uint64, time.Time, string, []antigravityUsage, bool) {
	outer, ok := parseAntigravityProtoOnly(data, 1, 4)
	if !ok {
		return antigravityModel{}, 0, time.Time{}, "", nil, false
	}
	chatModelData, ok := antigravityProtoBytes(outer, 1)
	if !ok {
		return antigravityModel{}, 0, time.Time{}, "", nil, false
	}
	chatModel, ok := parseAntigravityProtoOnly(chatModelData, 3, 4, 9, 19)
	if !ok {
		return antigravityModel{}, 0, time.Time{}, "", nil, false
	}

	// response_model is the only descriptor-backed text model field. Do not
	// promote unknown protobuf fields into a priceable model name.
	modelName := antigravityProtoText(chatModel, 19, antigravityMaxModelBytes)
	model := antigravityModelFromText(modelName)
	modelID := antigravityModelID(chatModel, 3)
	timestamp, _ := antigravityGenerationTimestamp(chatModel)
	executionID := antigravityProtoText(outer, 4, antigravityMaxIDBytes)
	var usages []antigravityUsage
	if usageData, found := antigravityProtoBytes(chatModel, 4); found {
		if usage, valid := parseAntigravityUsage(usageData); valid {
			usages = append(usages, usage)
		}
	}
	return model, modelID, timestamp, executionID, usages, true
}

func parseAntigravityStepTimestamp(data []byte) (antigravityStepTimeMetadata, bool) {
	fields, ok := parseAntigravityProtoOnly(data, 1, 8, 9, 12)
	if !ok {
		return antigravityStepTimeMetadata{}, false
	}
	timestamp, ok := antigravityStepTimestamp(fields)
	if !ok {
		return antigravityStepTimeMetadata{}, false
	}

	var responseID string
	if chatModelData, found := antigravityProtoBytes(fields, 9); found {
		if chatModel, valid := parseAntigravityProtoOnly(chatModelData, 11); valid {
			responseID = antigravityProtoText(chatModel, 11, antigravityMaxIDBytes)
		}
	}
	executionID := antigravityProtoText(fields, 12, antigravityMaxIDBytes)
	if responseID == "" && executionID == "" {
		return antigravityStepTimeMetadata{}, false
	}
	return antigravityStepTimeMetadata{
		timestamp: timestamp, responseID: responseID, executionID: executionID,
	}, true
}

func parseAntigravityUsage(data []byte) (antigravityUsage, bool) {
	fields, ok := parseAntigravityProtoOnly(data, 1, 2, 3, 4, 5, 11)
	if !ok {
		return antigravityUsage{}, false
	}
	input, ok := antigravityTokenCount(fields, 2)
	if !ok {
		return antigravityUsage{}, false
	}
	totalOutput, ok := antigravityTokenCount(fields, 3)
	if !ok {
		return antigravityUsage{}, false
	}
	cacheWrite, ok := antigravityTokenCount(fields, 4)
	if !ok {
		return antigravityUsage{}, false
	}
	cacheRead, ok := antigravityTokenCount(fields, 5)
	if !ok {
		return antigravityUsage{}, false
	}

	// The private usage message reliably exposes the total output at field 3.
	// Fields 9 and 10 vary between builds and are not a documented
	// reasoning/visible-output split. Treating them as one can inflate output,
	// so keep them out of accounting until their semantics are verified.
	usage, valid := (model.Usage{
		Input: input, Output: totalOutput, CacheWrite: cacheWrite, CacheRead: cacheRead,
	}).Sanitize()
	if !valid || usage.IsZero() {
		return antigravityUsage{}, false
	}
	usage.ContextTokens = usage.Input + usage.CacheRead + usage.CacheWrite

	modelID, _, ok := antigravityProtoVarint(fields, 1)
	if !ok {
		return antigravityUsage{}, false
	}
	return antigravityUsage{
		usage: usage, modelID: modelID,
		responseID: antigravityProtoText(fields, 11, antigravityMaxIDBytes),
	}, true
}

func antigravityGenerationTimestamp(fields []antigravityProtoField) (time.Time, bool) {
	wrapperData, ok := antigravityProtoBytes(fields, 9)
	if !ok {
		return time.Time{}, false
	}
	wrapper, ok := parseAntigravityProtoOnly(wrapperData, 4)
	if !ok {
		return time.Time{}, false
	}
	timestampData, ok := antigravityProtoBytes(wrapper, 4)
	if !ok {
		return time.Time{}, false
	}
	return antigravityTimestamp(timestampData)
}

func antigravityStepTimestamp(fields []antigravityProtoField) (time.Time, bool) {
	for _, number := range [...]int{8, 1} {
		if data, ok := antigravityProtoBytes(fields, number); ok {
			if timestamp, ok := antigravityTimestamp(data); ok {
				return timestamp, true
			}
		}
	}
	return time.Time{}, false
}

func antigravityTimestamp(data []byte) (time.Time, bool) {
	fields, ok := parseAntigravityProtoOnly(data, 1, 2)
	if !ok {
		return time.Time{}, false
	}
	seconds, found, ok := antigravityProtoVarint(fields, 1)
	// google.protobuf.Timestamp is defined through 9999-12-31T23:59:59Z.
	if !ok || !found || seconds == 0 || seconds > 253_402_300_799 {
		return time.Time{}, false
	}
	nanos, found, ok := antigravityProtoVarint(fields, 2)
	if !ok || (found && nanos >= uint64(time.Second)) {
		return time.Time{}, false
	}
	return time.Unix(int64(seconds), int64(nanos)), true
}

func antigravityModelFromText(text string) antigravityModel {
	text = strings.TrimSpace(text)
	if text == "" {
		return antigravityModel{}
	}
	lower := strings.ToLower(text)
	if canonical, ok := antigravityGeminiModel(lower); ok {
		return antigravityModel{id: canonical, provider: "google", rank: 2}
	}

	// Antigravity normally stores friendly labels (for example "Claude Sonnet
	// 4.6") rather than API IDs. Hyphenating only familiar vendor labels is a
	// reversible spelling conversion; unknown labels remain untouched rather
	// than being guessed at.
	if base := strings.TrimSpace(strings.SplitN(lower, "(", 2)[0]); base != "" {
		converted := strings.ReplaceAll(base, " ", "-")
		if strings.HasPrefix(converted, "claude-") || strings.HasPrefix(converted, "gpt-") {
			return antigravityModel{id: converted, rank: 2}
		}
	}
	return antigravityModel{id: text, rank: 1}
}

func antigravityGeminiModel(lower string) (string, bool) {
	if strings.HasPrefix(lower, "gemini-") {
		for _, suffix := range [...]string{"-high", "-medium", "-low"} {
			lower = strings.TrimSuffix(lower, suffix)
		}
		return lower, true
	}

	base := strings.TrimSpace(strings.SplitN(lower, "(", 2)[0])
	parts := strings.Fields(base)
	if len(parts) < 3 || parts[0] != "gemini" || !antigravityVersion(parts[1]) {
		return "", false
	}
	family := parts[2]
	consumed := 3
	if family == "flash" && len(parts) > consumed && parts[consumed] == "lite" {
		family += "-lite"
		consumed++
	}
	if family != "flash" && family != "flash-lite" && family != "pro" {
		return "", false
	}
	for _, suffix := range parts[consumed:] {
		switch suffix {
		case "thinking", "high", "medium", "low":
		default:
			return "", false
		}
	}
	return "gemini-" + parts[1] + "-" + family, true
}

func antigravityVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func antigravityCandidates(events []antigravityUsageEvent) []antigravityCandidate {
	var candidates []antigravityCandidate
	for _, event := range events {
		modelName := antigravityEventModel(event)
		if modelName.id == "" {
			continue
		}
		fallback := identityKey("antigravity-row", event.sourceID, event.sourceTable, strconv.FormatInt(event.sourceIndex, 10), strconv.Itoa(event.usageOrdinal))
		aliases, key, rank := event.usage.identities(fallback)
		candidates = append(candidates, antigravityCandidate{
			turn: model.Turn{
				Key: key, SessionID: event.sessionID, Agent: model.AgentAntigravity,
				Timestamp: event.timestamp, Model: modelName.id, Provider: modelName.provider, Usage: event.usage.usage,
			},
			aliases: aliases, modelRank: modelName.rank, keyRank: rank,
		})
	}
	return candidates
}

// antigravityEventModel uses a text model only from the same generation
// record. Numeric private IDs without that field stay deliberately unpriced;
// learning them from another row could charge a call to the wrong model.
func antigravityEventModel(event antigravityUsageEvent) antigravityModel {
	if event.model.id != "" && (event.usage.modelID == 0 || event.contextModelID == 0 || event.usage.modelID == event.contextModelID) {
		return event.model
	}
	if event.usage.modelID != 0 {
		return antigravityNumericModel(event.usage.modelID)
	}
	if event.contextModelID != 0 {
		return antigravityNumericModel(event.contextModelID)
	}
	return antigravityModel{}
}

func (u antigravityUsage) identities(fallback string) ([]string, string, uint8) {
	if u.responseID != "" {
		key := identityKey("antigravity-response", u.responseID)
		return []string{key}, key, 3
	}
	return []string{fallback}, fallback, 0
}

type antigravityGroup struct {
	turn      model.Turn
	aliases   map[string]bool
	modelRank uint8
	keyRank   uint8
	dead      bool
}

func mergeAntigravityCandidates(candidates []antigravityCandidate) []model.Turn {
	byAlias := make(map[string]*antigravityGroup)
	var groups []*antigravityGroup
	for _, candidate := range candidates {
		var target *antigravityGroup
		for _, alias := range candidate.aliases {
			if existing := byAlias[alias]; existing != nil && !existing.dead {
				if target == nil {
					target = existing
				} else if target != existing {
					target = mergeAntigravityGroups(target, existing, byAlias)
				}
			}
		}
		if target == nil {
			target = &antigravityGroup{aliases: make(map[string]bool)}
			groups = append(groups, target)
		}
		mergeAntigravityTurn(target, candidate.turn, candidate.modelRank, candidate.keyRank)
		for _, alias := range candidate.aliases {
			target.aliases[alias] = true
			byAlias[alias] = target
		}
	}

	turns := make([]model.Turn, 0, len(groups))
	for _, group := range groups {
		if !group.dead && !group.turn.Timestamp.IsZero() {
			turns = append(turns, group.turn)
		}
	}
	return turns
}

func mergeAntigravityGroups(target, source *antigravityGroup, byAlias map[string]*antigravityGroup) *antigravityGroup {
	mergeAntigravityTurn(target, source.turn, source.modelRank, source.keyRank)
	for alias := range source.aliases {
		target.aliases[alias] = true
		byAlias[alias] = target
	}
	source.dead = true
	return target
}

func mergeAntigravityTurn(target *antigravityGroup, candidate model.Turn, modelRank, keyRank uint8) {
	if target.turn.Key == "" {
		target.turn = candidate
		target.modelRank = modelRank
		target.keyRank = keyRank
		return
	}
	// Copied conversations can contain competing snapshots for one response.
	// Keep one complete native vector rather than taking a per-bucket maximum:
	// mixing fields would manufacture usage that no invocation reported.
	if moreCompleteUsage(candidate.Usage, target.turn.Usage) {
		target.turn.Usage = candidate.Usage
	}
	if modelRank > target.modelRank {
		target.turn.Model = candidate.Model
		target.turn.Provider = candidate.Provider
		target.modelRank = modelRank
	}
	if keyRank > target.keyRank {
		target.turn.Key = candidate.Key
		target.keyRank = keyRank
	}
	if !candidate.Timestamp.IsZero() && (target.turn.Timestamp.IsZero() || candidate.Timestamp.Before(target.turn.Timestamp)) {
		target.turn.Timestamp = candidate.Timestamp
	}
}

func antigravityTokenCount(fields []antigravityProtoField, number int) (int64, bool) {
	value, found, ok := antigravityProtoVarint(fields, number)
	if !ok {
		return 0, false
	}
	if !found {
		return 0, true
	}
	if value > math.MaxInt64 {
		return 0, false
	}
	return int64(value), true
}

func antigravityModelID(fields []antigravityProtoField, number int) uint64 {
	value, found, ok := antigravityProtoVarint(fields, number)
	if !ok || !found {
		return 0
	}
	return value
}

type antigravityProtoField struct {
	number int
	wire   byte
	varint uint64
	bytes  []byte
}

// parseAntigravityProtoOnly validates the complete message but retains only
// explicitly requested fields. Large prompts, messages, tools and outputs can
// therefore make a native metadata record large without multiplying parser
// memory or becoming accidental accounting inputs.
func parseAntigravityProtoOnly(data []byte, keep ...int) ([]antigravityProtoField, bool) {
	fields := make([]antigravityProtoField, 0, 8)
	fieldCount := 0
	for pos := 0; pos < len(data); {
		if fieldCount == antigravityMaxProtoFields {
			return nil, false
		}
		fieldCount++
		key, ok := readAntigravityVarint(data, &pos)
		if !ok || key>>3 == 0 || key>>3 > math.MaxInt32 {
			return nil, false
		}
		field := antigravityProtoField{number: int(key >> 3), wire: byte(key & 7)}
		switch field.wire {
		case 0:
			field.varint, ok = readAntigravityVarint(data, &pos)
			if !ok {
				return nil, false
			}
		case 1:
			if len(data)-pos < 8 {
				return nil, false
			}
			pos += 8
		case 2:
			length, ok := readAntigravityVarint(data, &pos)
			if !ok || length > uint64(len(data)-pos) {
				return nil, false
			}
			end := pos + int(length)
			field.bytes = data[pos:end]
			pos = end
		case 5:
			if len(data)-pos < 4 {
				return nil, false
			}
			pos += 4
		default:
			return nil, false
		}
		for _, number := range keep {
			if field.number == number {
				fields = append(fields, field)
				break
			}
		}
	}
	return fields, true
}

func readAntigravityVarint(data []byte, pos *int) (uint64, bool) {
	var value uint64
	for shift := uint(0); shift < 64 && *pos < len(data); shift += 7 {
		b := data[*pos]
		*pos++
		if shift == 63 && b > 1 {
			return 0, false
		}
		value |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return value, true
		}
	}
	return 0, false
}

func antigravityProtoBytes(fields []antigravityProtoField, number int) ([]byte, bool) {
	var value []byte
	found := false
	for _, field := range fields {
		if field.number != number {
			continue
		}
		if field.wire != 2 {
			return nil, false
		}
		value = field.bytes
		found = true
	}
	return value, found
}

func antigravityProtoVarint(fields []antigravityProtoField, number int) (uint64, bool, bool) {
	var value uint64
	found := false
	for _, field := range fields {
		if field.number != number {
			continue
		}
		if field.wire != 0 {
			return 0, false, false
		}
		value = field.varint
		found = true
	}
	return value, found, true
}

func antigravityProtoText(fields []antigravityProtoField, number, limit int) string {
	value, ok := antigravityProtoBytes(fields, number)
	if !ok || len(value) == 0 || len(value) > limit || !utf8.Valid(value) {
		return ""
	}
	return strings.TrimSpace(string(value))
}
