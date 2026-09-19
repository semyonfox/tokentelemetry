package ingest

import (
	"context"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/semyonfox/tokentelemetry/engine/internal/cost"
	"github.com/semyonfox/tokentelemetry/engine/internal/model"
	"github.com/semyonfox/tokentelemetry/engine/internal/pricing"
)

func TestAntigravityMapsExactUsageAndNanosecondTimestamp(t *testing.T) {
	root := t.TempDir()
	writeAntigravityDB(t, root, "session-one", antigravityTestBlob(antigravityTestRecord{
		model: "Gemini 3.6 Flash (High)", responseID: "response-123",
		seconds: 1_800_000_000, nanos: 123_456_789,
		input: 2_487, output: 130, cacheWrite: 100, cacheRead: 16_278,
		field9: 58, field10: 72,
	}))

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 {
		t.Fatalf("turn count = %d, want 1", len(turns))
	}
	want := model.Turn{
		Key:       identityKey("antigravity-response", "response-123"),
		SessionID: "session-one",
		Agent:     model.AgentAntigravity,
		Timestamp: time.Unix(1_800_000_000, 123_456_789),
		Model:     "gemini-3.6-flash",
		Provider:  "google",
		Usage: model.Usage{
			Input: 2_487, Output: 130, CacheWrite: 100, CacheRead: 16_278,
			ContextTokens: 18_865,
		},
	}
	if !reflect.DeepEqual(turns[0], want) {
		t.Fatalf("turn = %#v, want %#v", turns[0], want)
	}

	table := &pricing.Table{
		Models: map[string]*pricing.Model{
			"gemini-3.6-flash": {ID: "gemini-3.6-flash", Rates: []pricing.Rate{{
				From: pricing.MustParseDate("2026-01-01"), In: 1, Out: 2, CacheRead: 0.1, CacheWrite: 1,
			}}},
		},
		ByProvider: map[string]*pricing.Model{},
		Aliases:    map[string]string{},
	}
	if priced := cost.Of(turns[0], table); !priced.Priced() || priced.Model != "gemini-3.6-flash" {
		t.Fatalf("display model was not priceable: %#v", priced)
	}
}

func TestAntigravityReadsCommittedLiveWAL(t *testing.T) {
	root := t.TempDir()
	baseline := antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "baseline",
		seconds: 1_800_000_005, input: 1, output: 1,
	}
	writeAntigravityDB(t, root, "session-wal", antigravityTestBlob(baseline))

	path := filepath.Join(root, "session-wal.db")
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var journalMode string
	if err := writer.QueryRow("PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal mode = %q, want wal", journalMode)
	}
	live := antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "live-wal",
		seconds: 1_800_000_006, input: 2, output: 1,
	}
	liveBlob := antigravityTestBlob(live)
	if _, err := writer.Exec("INSERT INTO gen_metadata(idx, data, size) VALUES (?, ?, ?)", 1, liveBlob, len(liveBlob)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("committed WAL is unavailable: %v", err)
	}

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 2 {
		t.Fatalf("turns = %#v, want baseline and committed WAL row", turns)
	}
	keys := make(map[string]bool, len(turns))
	for _, turn := range turns {
		keys[turn.Key] = true
	}
	if !keys[identityKey("antigravity-response", "live-wal")] {
		t.Fatalf("live WAL row was not scanned: %#v", turns)
	}
}

func TestAntigravityReadsOnlyDirectGenerationUsage(t *testing.T) {
	root := t.TempDir()
	contextRecord := antigravityTestRecord{
		model: "Gemini 3.6 Flash (High)", modelField: 19, contextModelID: 812, seconds: 1_800_000_010,
	}
	primary := antigravityTestRecord{modelID: 812, responseID: "primary", input: 10, output: 5}
	ignoredWrapper := antigravityTestRecord{modelID: 812, responseID: "ignored-wrapper", input: 11, output: 6}
	writeAntigravityDB(t, root, "session-all", antigravityTestBlobWithUsages(
		contextRecord, antigravityTestUsage(primary), antigravityTestUsage(ignoredWrapper),
	))

	// A step has a timestamp and a nested ChatModel response ID, but no token
	// counter from it may become a turn.
	writeAntigravitySteps(t, root, "session-all", antigravityTestStepBlob(antigravityTestRecord{
		responseID: "primary", stepTimestampField: 1, seconds: 1_800_000_011,
	}))

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 {
		t.Fatalf("turn count = %d, want 1: %#v", len(turns), turns)
	}
	turn := turns[0]
	if turn.Key != identityKey("antigravity-response", "primary") || turn.Usage.Input != 10 || turn.Model != "gemini-3.6-flash" || turn.Provider != "google" {
		t.Fatalf("direct generation turn = %#v", turn)
	}
}

func TestAntigravityDoesNotCarryModelAcrossGenerations(t *testing.T) {
	root := t.TempDir()
	writeAntigravityDB(t, root, "session-continuation",
		antigravityTestBlobWithUsages(antigravityTestRecord{
			model: "Gemini 3.6 Flash", contextModelID: 812, seconds: 1_800_000_015,
		}, nil),
		antigravityTestBlob(antigravityTestRecord{
			modelID: 812, responseID: "continued", seconds: 1_800_000_016, input: 6, output: 2,
		}),
	)

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 || turns[0].Model != "antigravity-model-812" || turns[0].Provider != "" {
		t.Fatalf("unpriced continuation turns = %#v", turns)
	}
}

func TestAntigravityUsesSameGenerationTextModel(t *testing.T) {
	root := t.TempDir()
	contextRecord := antigravityTestRecord{
		model: "Gemini 2.5 Flash", contextModelID: 312, seconds: 1_800_000_018,
	}
	direct := antigravityTestRecord{modelID: 312, responseID: "flash", input: 1, output: 1}
	writeAntigravityDB(t, root, "session-models", antigravityTestBlobWithUsages(
		contextRecord, antigravityTestUsage(direct),
	))

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 {
		t.Fatalf("turn count = %d, want 1: %#v", len(turns), turns)
	}
	if got := turns[0].Model; got != "gemini-2.5-flash" {
		t.Fatalf("paired native model ID = %q, want gemini-2.5-flash", got)
	}
}

func TestAntigravityIgnoresUnverifiedModelField(t *testing.T) {
	root := t.TempDir()
	writeAntigravityDB(t, root, "session-unverified-model", antigravityTestBlob(antigravityTestRecord{
		model: "Gemini 3.6 Flash", modelField: 21, contextModelID: 901, modelID: 901,
		responseID: "unverified-model", seconds: 1_800_000_018, input: 1, output: 1,
	}))

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 || turns[0].Model != "antigravity-model-901" || turns[0].Provider != "" {
		t.Fatalf("unverified model field was promoted: %#v", turns)
	}
}

func TestAntigravityKeepsNumericOnlyAndNoIdentityUsage(t *testing.T) {
	root := t.TempDir()
	writeAntigravityDB(t, root, "session-native",
		antigravityTestBlob(antigravityTestRecord{contextModelID: 777, modelID: 777, seconds: 1_800_000_019, input: 6, output: 2}),
		antigravityTestBlob(antigravityTestRecord{contextModelID: 777, modelID: 777, seconds: 1_800_000_020, input: 7, output: 3}),
	)

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 2 {
		t.Fatalf("turns = %#v", turns)
	}
	for index, turn := range turns {
		if turn.Model != "antigravity-model-777" || turn.Key != identityKey("antigravity-row", antigravitySourceID(filepath.Join(root, "session-native.db")), "gen_metadata", itoa(int64(index)), "0") {
			t.Fatalf("native-only turn = %#v", turn)
		}
	}
}

func TestAntigravityDoesNotUseUnverifiedUsageAliases(t *testing.T) {
	root := t.TempDir()
	first := antigravityTestRecord{
		model: "Gemini 3.6 Flash", messageID: "shared-message", providerID: "shared-provider",
		seconds: 1_800_000_025, input: 3, output: 1,
	}
	second := first
	second.seconds++
	second.input = 4
	writeAntigravityDB(t, root, "session-unverified-aliases", antigravityTestBlob(first), antigravityTestBlob(second))

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 2 || turns[0].Key == turns[1].Key {
		t.Fatalf("unverified aliases merged records: %#v", turns)
	}
}

func TestAntigravityDoesNotMergeUnidentifiedRowsAcrossDatabases(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	for root, input := range map[string]uint64{first: 5, second: 7} {
		writeAntigravityDB(t, root, "session", antigravityTestBlob(antigravityTestRecord{
			model: "Gemini 3.6 Flash", seconds: 1_800_000_019, input: input, output: 2,
		}))
	}

	result, err := Run(context.Background(), []Scanner{&Antigravity{roots: []string{first, second}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Turns) != 2 || result.Duplicates != 0 {
		t.Fatalf("turns = %#v, duplicates = %d; want two distinct local rows", result.Turns, result.Duplicates)
	}
	keys := make(map[string]bool, len(result.Turns))
	inputs := make(map[int64]bool, len(result.Turns))
	for _, turn := range result.Turns {
		keys[turn.Key] = true
		inputs[turn.Usage.Input] = true
	}
	if len(keys) != 2 || !inputs[5] || !inputs[7] {
		t.Fatalf("unidentified rows were not kept separately: %#v", result.Turns)
	}
}

func TestAntigravityUsesTimestampFromMatchingMetadataCopy(t *testing.T) {
	root := t.TempDir()
	generation := antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "shared", executionID: "execution-shared", input: 5, output: 1,
	}
	writeAntigravityDB(t, root, "session-timestamp", antigravityTestBlob(generation))
	step := antigravityTestRecord{responseID: "shared", executionID: "execution-shared", seconds: 1_800_000_019}
	writeAntigravitySteps(t, root, "session-timestamp", antigravityTestStepBlob(step))

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 || turns[0].Timestamp.IsZero() || turns[0].Usage.Input != 5 || turns[0].Usage.Output != 1 {
		t.Fatalf("reconciled turns = %#v", turns)
	}
}

func TestAntigravityUsesStepTimestampByUnambiguousExecution(t *testing.T) {
	root := t.TempDir()
	generation := antigravityTestRecord{
		model: "Gemini 3.6 Flash", executionID: "execution-only", input: 5, output: 1,
	}
	writeAntigravityDB(t, root, "session-execution", antigravityTestBlob(generation))
	writeAntigravitySteps(t, root, "session-execution", antigravityTestStepBlob(antigravityTestRecord{
		executionID: "execution-only", seconds: 1_800_000_021,
	}))

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 || !turns[0].Timestamp.Equal(time.Unix(1_800_000_021, 0)) {
		t.Fatalf("execution-dated turns = %#v", turns)
	}
}

func TestAntigravityDoesNotGuessStepTimestampForAmbiguousExecution(t *testing.T) {
	root := t.TempDir()
	generation := antigravityTestRecord{
		model: "Gemini 3.6 Flash", executionID: "execution-ambiguous", input: 5, output: 1,
	}
	writeAntigravityDB(t, root, "session-ambiguous", antigravityTestBlob(generation))
	writeAntigravitySteps(t, root, "session-ambiguous",
		antigravityTestStepBlob(antigravityTestRecord{executionID: "execution-ambiguous", seconds: 1_800_000_022}),
		antigravityTestStepBlob(antigravityTestRecord{executionID: "execution-ambiguous", seconds: 1_800_000_023}),
	)

	if turns := scan(t, &Antigravity{roots: []string{root}}); len(turns) != 0 {
		t.Fatalf("ambiguous execution turns = %#v, want none", turns)
	}
}

func TestAntigravityIgnoresUnknownUsageFieldsWithoutInflatingOutput(t *testing.T) {
	root := t.TempDir()
	writeAntigravityDB(t, root, "session-output", antigravityTestBlob(antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "response-output", seconds: 1_800_000_020,
		input: 4, output: 2, field9: 5, field10: 7,
	}))

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 || turns[0].Usage.Output != 2 || turns[0].Usage.Reasoning != 0 {
		t.Fatalf("usage = %#v, want only verified total output", turns)
	}
}

func TestAntigravityUsesStepOnlyToDateGenerationUsage(t *testing.T) {
	root := t.TempDir()
	contextRecord := antigravityTestRecord{model: "Gemini 3.6 Flash", executionID: "execution-bridge"}
	direct := antigravityTestRecord{
		responseID: "response-bridge", messageID: "message-bridge", input: 10, output: 3,
	}
	writeAntigravityDB(t, root, "session-bridge", antigravityTestBlobWithUsage(contextRecord, antigravityTestUsage(direct)))

	step := antigravityTestRecord{responseID: "response-bridge", executionID: "execution-bridge", seconds: 1_800_000_030}
	stepChatModel := antigravityProtoTestMessage(
		antigravityProtoTestBytes(4, antigravityTestUsage(antigravityTestRecord{input: 12, output: 4})),
		antigravityProtoTestBytes(11, []byte(step.responseID)),
	)
	writeAntigravitySteps(t, root, "session-bridge", antigravityTestStepBlobWithChatModel(step, stepChatModel))

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 {
		t.Fatalf("turn count = %d, want one merged invocation: %#v", len(turns), turns)
	}
	turn := turns[0]
	if turn.Key != identityKey("antigravity-response", "response-bridge") || turn.Usage.Input != 10 || turn.Usage.Output != 3 || turn.Timestamp.IsZero() {
		t.Fatalf("generation usage = %#v", turn)
	}
}

func TestAntigravityDoesNotInferProviderFromNumericEnum(t *testing.T) {
	root := t.TempDir()
	record := antigravityTestRecord{
		model: "Unrecognised Experimental Model", responseID: "response-other",
		seconds: 1_800_000_040, input: 5, output: 2,
	}
	// The observed Google provider enum is deliberately present. Provider
	// attribution still comes only from a trustworthy text model name.
	usage := antigravityProtoTestMessage(
		antigravityProtoTestVarint(2, 5),
		antigravityProtoTestVarint(3, 2),
		antigravityProtoTestVarint(6, 24),
		antigravityProtoTestBytes(11, []byte(record.responseID)),
	)
	writeAntigravityDB(t, root, "session-other", antigravityTestBlobWithUsage(record, usage))

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 {
		t.Fatalf("turn count = %d, want 1", len(turns))
	}
	if turns[0].Provider != "" {
		t.Fatalf("provider = %q, want empty", turns[0].Provider)
	}
}

func TestAntigravitySkipsMalformedAndMissingRequiredFields(t *testing.T) {
	root := t.TempDir()
	valid := antigravityTestRecord{
		model: "Gemini 3.6 Pro", responseID: "valid-response",
		seconds: 1_800_000_050, input: 7, output: 3,
	}
	missingModel := valid
	missingModel.model = ""
	missingModel.responseID = "missing-model"
	missingTimestamp := valid
	missingTimestamp.seconds = 0
	absurdUsage := valid
	absurdUsage.responseID = "absurd"
	absurdUsage.output = uint64(model.MaxPerCallTokens + 1)

	writeAntigravityDB(t, root, "session-invalid",
		[]byte{0x0a, 0x80},
		antigravityTestBlob(missingModel),
		antigravityTestBlob(missingTimestamp),
		antigravityTestBlob(absurdUsage),
		antigravityTestBlob(valid),
	)

	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 || turns[0].Key != identityKey("antigravity-response", "valid-response") {
		t.Fatalf("turns = %#v, want only valid response", turns)
	}
}

func TestAntigravityMergesCopiedResponseAcrossDatabases(t *testing.T) {
	root := t.TempDir()
	record := antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "copied-response",
		seconds: 1_800_000_060, input: 20, output: 10,
	}
	writeAntigravityDB(t, root, "original", antigravityTestBlob(record))
	record.output = 12
	writeAntigravityDB(t, root, "fork", antigravityTestBlob(record))

	result, err := Run(context.Background(), []Scanner{&Antigravity{roots: []string{root}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Turns) != 1 || result.Duplicates != 0 {
		t.Fatalf("turns = %d, duplicates = %d; want 1, 0", len(result.Turns), result.Duplicates)
	}
	if result.Turns[0].Key != identityKey("antigravity-response", "copied-response") || result.Turns[0].Usage.Output != 12 {
		t.Fatalf("turn = %#v", result.Turns[0])
	}
}

func TestAntigravityDoesNotCombineConflictingCopiedUsage(t *testing.T) {
	root := t.TempDir()
	first := antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "conflicting-response",
		seconds: 1_800_000_065, input: 100, output: 1,
	}
	second := first
	second.input = 1
	second.output = 100
	writeAntigravityDB(t, root, "a-first", antigravityTestBlob(first))
	writeAntigravityDB(t, root, "b-second", antigravityTestBlob(second))

	result, err := Run(context.Background(), []Scanner{&Antigravity{roots: []string{root}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Turns) != 1 || result.Duplicates != 0 {
		t.Fatalf("turns = %#v, duplicates = %d; want one native record", result.Turns, result.Duplicates)
	}
	if got := result.Turns[0].Usage; got.Input != 100 || got.Output != 1 || got.ContextTokens != 100 {
		t.Fatalf("conflicting copies produced usage %#v, want first native vector", got)
	}
}

func TestAntigravitySkipsFailedGenerationWithoutUsage(t *testing.T) {
	root := t.TempDir()
	record := antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "failed-response",
		seconds: 1_800_000_070,
	}
	writeAntigravityDB(t, root, "failed", antigravityTestBlobWithUsages(record, nil))

	if turns := scan(t, &Antigravity{roots: []string{root}}); len(turns) != 0 {
		t.Fatalf("turn count = %d, want 0", len(turns))
	}
}

func TestAntigravitySkipsOversizedMetadataWithoutDroppingValidRows(t *testing.T) {
	root := t.TempDir()
	writeAntigravityDB(t, root, "bounded", antigravityTestBlob(antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "small", seconds: 1_800_000_080, input: 1, output: 1,
	}))
	appendAntigravityLargeGenerationBlob(t, root, "bounded", antigravityMaxMetadataBytes+1)

	result, err := Run(context.Background(), []Scanner{&Antigravity{roots: []string{root}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Turns) != 1 || result.Turns[0].Key != identityKey("antigravity-response", "small") {
		t.Fatalf("turns = %#v", result.Turns)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error(), "skipped 1 Antigravity metadata blob") {
		t.Fatalf("scan errors = %#v", result.Errors)
	}
}

func TestAntigravityRejectsUnknownDatabaseVersion(t *testing.T) {
	root := t.TempDir()
	writeAntigravityDB(t, root, "unknown-version", antigravityTestBlob(antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "should-not-read", seconds: 1_800_000_090, input: 1, output: 1,
	}))
	setAntigravityVersion(t, root, "unknown-version", antigravityDatabaseVersion+1)

	result, err := Run(context.Background(), []Scanner{&Antigravity{roots: []string{root}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Turns) != 0 || len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error(), "unsupported Antigravity conversation database version") {
		t.Fatalf("result = %#v", result)
	}
}

func TestAntigravityDiscoversAllConversationRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	roots := []string{
		filepath.Join(home, ".gemini", "antigravity", "conversations"),
		filepath.Join(home, ".gemini", "antigravity-cli", "conversations"),
		filepath.Join(home, ".gemini", "antigravity-ide", "conversations"),
		filepath.Join(home, ".gemini", "antigravity-backup", "conversations"),
		filepath.Join(home, ".config", "antigravity"), // direct database root is valid too.
	}
	for _, root := range roots {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := NewAntigravity().Roots(); !reflect.DeepEqual(got, roots) {
		t.Fatalf("roots = %#v, want %#v", got, roots)
	}
}

func TestAntigravityFindsNestedConversationDatabase(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "archived", "conversations")
	writeAntigravityDB(t, nested, "nested", antigravityTestBlob(antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "nested", seconds: 1_800_000_095, input: 1, output: 1,
	}))
	turns := scan(t, &Antigravity{roots: []string{root}})
	if len(turns) != 1 || turns[0].Key != identityKey("antigravity-response", "nested") {
		t.Fatalf("nested turns = %#v", turns)
	}
}

func TestAntigravityScansSymlinkedRoot(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "relocated")
	link := filepath.Join(parent, "antigravity")
	writeAntigravityDB(t, target, "session", antigravityTestBlob(antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "symlinked", seconds: 1_800_000_096, input: 1, output: 1,
	}))
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	turns := scan(t, &Antigravity{roots: []string{link}})
	if len(turns) != 1 || turns[0].Key != identityKey("antigravity-response", "symlinked") {
		t.Fatalf("symlinked-root turns = %#v", turns)
	}
}

func TestAntigravitySkipsNestedSymlinkedDatabase(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	writeAntigravityDB(t, target, "outside", antigravityTestBlob(antigravityTestRecord{
		model: "Gemini 3.6 Flash", responseID: "outside", seconds: 1_800_000_097, input: 1, output: 1,
	}))
	if err := os.Symlink(filepath.Join(target, "outside.db"), filepath.Join(root, "linked.db")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if turns := scan(t, &Antigravity{roots: []string{root}}); len(turns) != 0 {
		t.Fatalf("nested symlink turns = %#v, want none", turns)
	}
}

type antigravityTestRecord struct {
	model              string
	modelField         int
	contextModelID     uint64
	stepTimestampField int
	modelID            uint64
	messageID          string
	responseID         string
	providerID         string
	executionID        string
	seconds            uint64
	nanos              uint64
	input              uint64
	output             uint64
	cacheWrite         uint64
	cacheRead          uint64
	field9             uint64
	field10            uint64
}

func antigravityTestUsage(record antigravityTestRecord) []byte {
	var fields [][]byte
	if record.modelID != 0 {
		fields = append(fields, antigravityProtoTestVarint(1, record.modelID))
	}
	fields = append(fields,
		antigravityProtoTestVarint(2, record.input),
		antigravityProtoTestVarint(3, record.output),
		antigravityProtoTestVarint(4, record.cacheWrite),
		antigravityProtoTestVarint(5, record.cacheRead),
		antigravityProtoTestVarint(9, record.field9),
		antigravityProtoTestVarint(10, record.field10),
	)
	if record.messageID != "" {
		fields = append(fields, antigravityProtoTestBytes(7, []byte(record.messageID)))
	}
	if record.responseID != "" {
		fields = append(fields, antigravityProtoTestBytes(11, []byte(record.responseID)))
	}
	if record.providerID != "" {
		fields = append(fields, antigravityProtoTestBytes(12, []byte(record.providerID)))
	}
	return antigravityProtoTestMessage(fields...)
}

func antigravityTestBlob(record antigravityTestRecord) []byte {
	return antigravityTestBlobWithUsage(record, antigravityTestUsage(record))
}

func antigravityTestBlobWithUsage(record antigravityTestRecord, usage []byte) []byte {
	return antigravityTestBlobWithUsages(record, usage)
}

func antigravityTestBlobWithUsages(record antigravityTestRecord, direct []byte, ignoredWrappers ...[]byte) []byte {
	var chatModelFields [][]byte
	if record.contextModelID != 0 {
		chatModelFields = append(chatModelFields, antigravityProtoTestVarint(3, record.contextModelID))
	}
	if direct != nil {
		chatModelFields = append(chatModelFields, antigravityProtoTestBytes(4, direct))
	}
	for _, ignored := range ignoredWrappers {
		// Field 17 has been observed in old blobs but its schema is not
		// descriptor-backed. Keep it in fixtures to prove it cannot count usage.
		chatModelFields = append(chatModelFields, antigravityProtoTestBytes(17, antigravityProtoTestBytes(2, ignored)))
	}
	chatModelFields = append(chatModelFields, antigravityProtoTestBytes(9, antigravityProtoTestBytes(4, antigravityTestTimestamp(record))))
	if record.model != "" {
		modelField := record.modelField
		if modelField == 0 {
			modelField = 19
		}
		chatModelFields = append(chatModelFields, antigravityProtoTestBytes(modelField, []byte(record.model)))
	}
	outerFields := [][]byte{antigravityProtoTestBytes(1, antigravityProtoTestMessage(chatModelFields...))}
	if record.executionID != "" {
		outerFields = append(outerFields, antigravityProtoTestBytes(4, []byte(record.executionID)))
	}
	return antigravityProtoTestMessage(outerFields...)
}

func antigravityTestStepBlob(record antigravityTestRecord) []byte {
	var chatModelFields [][]byte
	if record.responseID != "" {
		chatModelFields = append(chatModelFields, antigravityProtoTestBytes(11, []byte(record.responseID)))
	}
	return antigravityTestStepBlobWithChatModel(record, antigravityProtoTestMessage(chatModelFields...))
}

func antigravityTestStepBlobWithChatModel(record antigravityTestRecord, chatModel []byte) []byte {
	var fields [][]byte
	timestampField := record.stepTimestampField
	if timestampField == 0 {
		timestampField = 8
	}
	fields = append(fields, antigravityProtoTestBytes(timestampField, antigravityTestTimestamp(record)))
	if chatModel != nil {
		fields = append(fields, antigravityProtoTestBytes(9, chatModel))
	}
	if record.executionID != "" {
		fields = append(fields, antigravityProtoTestBytes(12, []byte(record.executionID)))
	}
	return antigravityProtoTestMessage(fields...)
}

func antigravityTestTimestamp(record antigravityTestRecord) []byte {
	return antigravityProtoTestMessage(
		antigravityProtoTestVarint(1, record.seconds),
		antigravityProtoTestVarint(2, record.nanos),
	)
}

func antigravityProtoTestMessage(fields ...[]byte) []byte {
	var message []byte
	for _, field := range fields {
		message = append(message, field...)
	}
	return message
}

func antigravityProtoTestVarint(number int, value uint64) []byte {
	var encoded [binary.MaxVarintLen64]byte
	keyLen := binary.PutUvarint(encoded[:], uint64(number<<3))
	field := append([]byte(nil), encoded[:keyLen]...)
	valueLen := binary.PutUvarint(encoded[:], value)
	return append(field, encoded[:valueLen]...)
}

func antigravityProtoTestBytes(number int, value []byte) []byte {
	var encoded [binary.MaxVarintLen64]byte
	keyLen := binary.PutUvarint(encoded[:], uint64(number<<3|2))
	field := append([]byte(nil), encoded[:keyLen]...)
	lengthLen := binary.PutUvarint(encoded[:], uint64(len(value)))
	field = append(field, encoded[:lengthLen]...)
	return append(field, value...)
}

func writeAntigravityDB(t *testing.T, root, session string, blobs ...[]byte) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, session+".db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE gen_metadata (
		idx INTEGER PRIMARY KEY,
		data BLOB,
		size INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	for idx, blob := range blobs {
		if _, err := db.Exec("INSERT INTO gen_metadata(idx, data, size) VALUES (?, ?, ?)", idx, blob, len(blob)); err != nil {
			t.Fatal(err)
		}
	}
}

func writeAntigravitySteps(t *testing.T, root, session string, blobs ...[]byte) {
	t.Helper()
	path := filepath.Join(root, session+".db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE steps (
		idx INTEGER PRIMARY KEY,
		metadata BLOB
	)`); err != nil {
		t.Fatal(err)
	}
	for idx, blob := range blobs {
		if _, err := db.Exec("INSERT INTO steps(idx, metadata) VALUES (?, ?)", idx, blob); err != nil {
			t.Fatal(err)
		}
	}
}

func appendAntigravityLargeGenerationBlob(t *testing.T, root, session string, bytes int) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, session+".db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO gen_metadata(idx, data, size) VALUES (?, zeroblob(?), ?)", 99, bytes, bytes); err != nil {
		t.Fatal(err)
	}
}

func setAntigravityVersion(t *testing.T, root, session string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, session+".db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA user_version = " + itoa(int64(version))); err != nil {
		t.Fatal(err)
	}
}
