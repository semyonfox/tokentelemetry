package ingest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// scanCopilotVSCode reads the local ChatSessionStore written by VS Code. roots
// are VS Code User directories, although accepting workspaceStorage,
// globalStorage, and a session-store directory directly keeps platform-specific
// discovery in the caller.
//
// This reader deliberately never falls back to promptTokens or
// completionTokens. Those counters describe a recent model call, while
// modelTotals is the only persisted per-model, whole-turn accounting record.
// ChatSessionStore is shared by every VS Code chat participant, so a persisted
// GitHub Copilot agent ID is also required before a record is attributed here.
func scanCopilotVSCode(ctx context.Context, roots []string, emit func(model.Turn)) (found bool, err error) {
	paths, err := copilotVSCodeSessionPaths(ctx, roots)
	if err != nil {
		return false, err
	}
	if len(paths) == 0 {
		return false, nil
	}

	var candidates []model.Turn
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return true, err
		}
		state, ok, err := copilotVSCodeSessionState(ctx, path)
		if err != nil {
			return true, err
		}
		if !ok {
			continue
		}
		candidates = append(candidates, copilotVSCodeTurns(state)...)
	}
	for _, turn := range copilotVSCodeMostCompleteTurns(candidates) {
		emit(turn)
	}
	return true, nil
}

// VS Code intentionally retains copies during session migration. Keep the
// first copy's attribution, but replace its usage when a later copy carries a
// more complete cumulative total, matching the central scanner reconciliation.
func copilotVSCodeMostCompleteTurns(candidates []model.Turn) []model.Turn {
	seen := make(map[string]int, len(candidates))
	var turns []model.Turn
	for _, turn := range candidates {
		if turn.Key == "" {
			turns = append(turns, turn)
			continue
		}
		if index, duplicate := seen[turn.Key]; duplicate {
			if moreCompleteUsage(turn.Usage, turns[index].Usage) {
				turns[index].Usage = turn.Usage
			}
			continue
		}
		seen[turn.Key] = len(turns)
		turns = append(turns, turn)
	}
	return turns
}

// copilotVSCodeSessionPaths restricts discovery to ChatSessionStore locations.
// In particular, it does not walk the adjacent Copilot transcripts, debug logs,
// edit state, or extension storage, all of which can contain user content but
// not this exact accounting record.
func copilotVSCodeSessionPaths(ctx context.Context, roots []string) ([]string, error) {
	byLocation := make(map[string]copilotVSCodeSessionPath)
	seenRoots := make(map[string]struct{})
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if root == "" {
			continue
		}
		root = filepath.Clean(root)
		if _, seen := seenRoots[root]; seen {
			continue
		}
		seenRoots[root] = struct{}{}

		info, err := os.Stat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read VS Code Copilot root %q: %w", root, err)
		}
		useLog := copilotVSCodeUseLogSessionStorage(root)
		if !info.IsDir() {
			copilotVSCodeAddSessionFile(byLocation, root, useLog)
			continue
		}

		switch filepath.Base(root) {
		case "workspaceStorage":
			if err := copilotVSCodeWorkspacePaths(ctx, root, byLocation, useLog); err != nil {
				return nil, err
			}
		case "globalStorage":
			if err := copilotVSCodeSessionDirPaths(ctx, filepath.Join(root, "emptyWindowChatSessions"), byLocation, useLog); err != nil {
				return nil, err
			}
			if err := copilotVSCodeSessionDirPaths(ctx, filepath.Join(root, "transferredChatSessions"), byLocation, false); err != nil {
				return nil, err
			}
		case "no-workspace":
			if err := copilotVSCodeSessionDirPaths(ctx, filepath.Join(root, "chatSessions"), byLocation, useLog); err != nil {
				return nil, err
			}
		case "chatSessions", "emptyWindowChatSessions":
			if err := copilotVSCodeSessionDirPaths(ctx, root, byLocation, useLog); err != nil {
				return nil, err
			}
		case "transferredChatSessions":
			if err := copilotVSCodeSessionDirPaths(ctx, root, byLocation, false); err != nil {
				return nil, err
			}
		default:
			if err := copilotVSCodeWorkspacePaths(ctx, filepath.Join(root, "workspaceStorage"), byLocation, useLog); err != nil {
				return nil, err
			}
			if err := copilotVSCodeSessionDirPaths(ctx, filepath.Join(root, "globalStorage", "emptyWindowChatSessions"), byLocation, useLog); err != nil {
				return nil, err
			}
			if err := copilotVSCodeSessionDirPaths(ctx, filepath.Join(root, "globalStorage", "transferredChatSessions"), byLocation, false); err != nil {
				return nil, err
			}
		}
	}

	paths := make([]string, 0, len(byLocation))
	for _, candidate := range byLocation {
		paths = append(paths, candidate.path)
	}
	sort.Strings(paths)
	return paths, nil
}

type copilotVSCodeSessionPath struct {
	path string
	log  bool
}

func copilotVSCodeWorkspacePaths(ctx context.Context, workspaceStorage string, paths map[string]copilotVSCodeSessionPath, useLog bool) error {
	entries, err := os.ReadDir(workspaceStorage)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read VS Code workspace storage %q: %w", workspaceStorage, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		if err := copilotVSCodeSessionDirPaths(ctx, filepath.Join(workspaceStorage, entry.Name(), "chatSessions"), paths, useLog); err != nil {
			return err
		}
	}
	return nil
}

func copilotVSCodeSessionDirPaths(ctx context.Context, dir string, paths map[string]copilotVSCodeSessionPath, useLog bool) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read VS Code chat sessions %q: %w", dir, err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			continue
		}
		copilotVSCodeAddSessionFile(paths, filepath.Join(dir, entry.Name()), useLog)
	}
	return nil
}

func copilotVSCodeAddSessionFile(paths map[string]copilotVSCodeSessionPath, path string, useLog bool) {
	extension := filepath.Ext(path)
	if extension != ".json" && extension != ".jsonl" {
		return
	}
	location := filepath.Join(filepath.Dir(path), strings.TrimSuffix(filepath.Base(path), extension))
	candidate := copilotVSCodeSessionPath{path: path, log: extension == ".jsonl"}
	current, exists := paths[location]
	// VS Code uses the operation log by default, but its local
	// chat.useLogSessionStorage setting can explicitly select the flat snapshot.
	// Transferred sessions are always flat files.
	if !exists || (candidate.log == useLog && current.log != useLog) {
		paths[location] = candidate
	}
}

const copilotVSCodeMaxSettingsBytes = 1 << 20

// copilotVSCodeUseLogSessionStorage observes VS Code's own local preference;
// it is not a TokenTelemetry setting. The default is the append-only log.
func copilotVSCodeUseLogSessionStorage(root string) bool {
	dir := filepath.Clean(root)
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	// A direct session file can sit four levels below User. Do not wander above
	// the VS Code data tree looking for unrelated settings files.
	for depth := 0; depth < 5; depth++ {
		path := filepath.Join(dir, "settings.json")
		if fileExists(path) {
			return copilotVSCodeLogStorageSetting(path)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return true
}

func copilotVSCodeLogStorageSetting(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return true
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, int64(copilotVSCodeMaxSettingsBytes)+1))
	if err != nil || len(raw) > copilotVSCodeMaxSettingsBytes {
		return true
	}
	jsonBytes, ok := copilotVSCodeJSONC(raw)
	if !ok {
		return true
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(jsonBytes, &settings) != nil {
		return true
	}
	value, exists := settings["chat.useLogSessionStorage"]
	if !exists {
		return true
	}
	var enabled bool
	if json.Unmarshal(value, &enabled) != nil {
		return true
	}
	return enabled
}

// copilotVSCodeJSONC accepts the comment and trailing-comma syntax VS Code
// permits in settings.json while leaving quoted URLs and prompt-like strings
// untouched. Unknown settings stay as RawMessage values and are never used.
func copilotVSCodeJSONC(raw []byte) ([]byte, bool) {
	withoutComments := make([]byte, 0, len(raw))
	inString, escaped := false, false
	for index := 0; index < len(raw); {
		current := raw[index]
		if inString {
			withoutComments = append(withoutComments, current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			index++
			continue
		}
		if current == '"' {
			inString = true
			withoutComments = append(withoutComments, current)
			index++
			continue
		}
		if current != '/' || index+1 == len(raw) {
			withoutComments = append(withoutComments, current)
			index++
			continue
		}
		switch raw[index+1] {
		case '/':
			index += 2
			for index < len(raw) && raw[index] != '\n' {
				index++
			}
			if index < len(raw) {
				withoutComments = append(withoutComments, '\n')
				index++
			}
		case '*':
			index += 2
			closed := false
			for index+1 < len(raw) {
				if raw[index] == '\n' {
					withoutComments = append(withoutComments, '\n')
				}
				if raw[index] == '*' && raw[index+1] == '/' {
					index += 2
					closed = true
					break
				}
				index++
			}
			if !closed {
				return nil, false
			}
			withoutComments = append(withoutComments, ' ')
		default:
			withoutComments = append(withoutComments, current)
			index++
		}
	}
	if inString {
		return nil, false
	}

	withoutTrailingCommas := make([]byte, 0, len(withoutComments))
	inString, escaped = false, false
	for index, current := range withoutComments {
		if inString {
			withoutTrailingCommas = append(withoutTrailingCommas, current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			withoutTrailingCommas = append(withoutTrailingCommas, current)
			continue
		}
		if current == ',' {
			next := index + 1
			for next < len(withoutComments) && (withoutComments[next] == ' ' || withoutComments[next] == '\t' || withoutComments[next] == '\r' || withoutComments[next] == '\n') {
				next++
			}
			if next < len(withoutComments) && (withoutComments[next] == '}' || withoutComments[next] == ']') {
				continue
			}
		}
		withoutTrailingCommas = append(withoutTrailingCommas, current)
	}
	return withoutTrailingCommas, !inString
}

func copilotVSCodeSessionState(ctx context.Context, path string) (map[string]any, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if filepath.Ext(path) == ".jsonl" {
		return copilotVSCodeReplayJSONL(ctx, path)
	}

	file, err := os.Open(path)
	if err != nil {
		// A session can disappear between directory discovery and this open.
		return nil, false, nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > maxLine {
		return nil, false, nil
	}
	value, ok := copilotVSCodeDecode(file)
	if !ok {
		return nil, false, nil
	}
	state, ok := value.(map[string]any)
	return state, ok, nil
}

// copilotVSCodeReplayJSONL mirrors VS Code's ObjectMutationLog format. A
// malformed final partial line is common while VS Code is appending, so it is
// ignored after a complete baseline. A malformed complete entry makes the
// state ambiguous and is rejected.
func copilotVSCodeReplayJSONL(ctx context.Context, path string) (map[string]any, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false, nil
	}
	defer file.Close()

	reader := bufio.NewReaderSize(file, 256<<10)
	var overflow []byte
	var state any
	hasBaseline := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		line, readErr := reader.ReadSlice('\n')
		if readErr == bufio.ErrBufferFull {
			if len(overflow)+len(line) > maxLine {
				return nil, false, nil
			}
			overflow = append(overflow, line...)
			continue
		}
		if len(overflow) > 0 {
			if len(overflow)+len(line) > maxLine {
				return nil, false, nil
			}
			overflow = append(overflow, line...)
			line = overflow
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			entry, ok := copilotVSCodeDecodeBytes(trimmed)
			if !ok {
				if readErr == io.EOF && hasBaseline {
					break
				}
				return nil, false, nil
			}
			if !copilotVSCodeApplyMutation(&state, entry, &hasBaseline) {
				return nil, false, nil
			}
		}
		overflow = overflow[:0]
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, false, nil
		}
	}
	if !hasBaseline {
		return nil, false, nil
	}
	result, ok := state.(map[string]any)
	return result, ok, nil
}

// The operation log can address any field in the serialized session tree. This
// is the one intentionally dynamic boundary: every map, slice, and scalar is
// validated again before it affects accounting.
func copilotVSCodeDecode(reader io.Reader) (any, bool) {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	var extra any
	return value, decoder.Decode(&extra) == io.EOF
}

func copilotVSCodeDecodeBytes(raw []byte) (any, bool) {
	return copilotVSCodeDecode(bytes.NewReader(raw))
}

type copilotVSCodePathPart struct {
	key     string
	index   int
	isIndex bool
}

func copilotVSCodeApplyMutation(state *any, raw any, hasBaseline *bool) bool {
	entry, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	kind, ok := copilotVSCodeInteger(entry["kind"])
	if !ok {
		return false
	}
	switch kind {
	case 0:
		value, exists := entry["v"]
		if !exists {
			return false
		}
		*state = value
		*hasBaseline = true
		return true
	case 1:
		path, ok := copilotVSCodePath(entry["k"])
		value, exists := entry["v"]
		if !ok || !exists || !*hasBaseline {
			return false
		}
		return copilotVSCodeSet(*state, path, value, false)
	case 2:
		path, ok := copilotVSCodePath(entry["k"])
		if !ok || !*hasBaseline {
			return false
		}
		var values []any
		if rawValues, exists := entry["v"]; exists && rawValues != nil {
			var arrayOK bool
			values, arrayOK = rawValues.([]any)
			if !arrayOK {
				return false
			}
		}
		var start *int
		if rawStart, exists := entry["i"]; exists {
			value, ok := copilotVSCodeInteger(rawStart)
			if !ok || value < 0 || value > int64(^uint(0)>>1) {
				return false
			}
			index := int(value)
			start = &index
		}
		return copilotVSCodePush(*state, path, values, start)
	case 3:
		path, ok := copilotVSCodePath(entry["k"])
		return ok && *hasBaseline && copilotVSCodeSet(*state, path, nil, true)
	default:
		return false
	}
}

func copilotVSCodePath(value any) ([]copilotVSCodePathPart, bool) {
	parts, ok := value.([]any)
	if !ok {
		return nil, false
	}
	path := make([]copilotVSCodePathPart, 0, len(parts))
	for _, part := range parts {
		if key, ok := part.(string); ok {
			path = append(path, copilotVSCodePathPart{key: key})
			continue
		}
		index, ok := copilotVSCodeInteger(part)
		if !ok || index < 0 || index > int64(^uint(0)>>1) {
			return nil, false
		}
		path = append(path, copilotVSCodePathPart{index: int(index), isIndex: true})
	}
	return path, true
}

func (part copilotVSCodePathPart) mapKey() string {
	if part.isIndex {
		return strconv.Itoa(part.index)
	}
	return part.key
}

func copilotVSCodeSet(state any, path []copilotVSCodePathPart, value any, deleting bool) bool {
	if len(path) == 0 {
		// VS Code's ObjectMutationLog treats an empty set/delete path as a no-op.
		return true
	}
	parent, last, ok := copilotVSCodeParent(state, path)
	if !ok {
		return false
	}
	switch current := parent.(type) {
	case map[string]any:
		key := last.mapKey()
		if deleting {
			delete(current, key)
		} else {
			current[key] = value
		}
		return true
	case []any:
		if !last.isIndex || last.index >= len(current) {
			return false
		}
		if deleting {
			// JavaScript deletion in an array leaves a hole. nil is the closest
			// JSON-safe representation and cannot be mistaken for response data.
			current[last.index] = nil
		} else {
			current[last.index] = value
		}
		return true
	default:
		return false
	}
}

func copilotVSCodePush(state any, path []copilotVSCodePathPart, values []any, start *int) bool {
	if len(path) == 0 {
		return false
	}
	parent, last, ok := copilotVSCodeParent(state, path)
	if !ok {
		return false
	}
	var (
		array []any
		store func([]any)
	)
	switch current := parent.(type) {
	case map[string]any:
		key := last.mapKey()
		if existing, exists := current[key]; exists && existing != nil {
			var arrayOK bool
			array, arrayOK = existing.([]any)
			if !arrayOK {
				return false
			}
		}
		store = func(value []any) { current[key] = value }
	case []any:
		if !last.isIndex || last.index >= len(current) {
			return false
		}
		if existing := current[last.index]; existing != nil {
			var arrayOK bool
			array, arrayOK = existing.([]any)
			if !arrayOK {
				return false
			}
		}
		store = func(value []any) { current[last.index] = value }
	default:
		return false
	}
	if start != nil {
		if *start > len(array) {
			return false
		}
		array = array[:*start]
	}
	array = append(array, values...)
	store(array)
	return true
}

func copilotVSCodeParent(state any, path []copilotVSCodePathPart) (any, copilotVSCodePathPart, bool) {
	current := state
	for _, part := range path[:len(path)-1] {
		switch container := current.(type) {
		case map[string]any:
			value, exists := container[part.mapKey()]
			if !exists || value == nil {
				return nil, copilotVSCodePathPart{}, false
			}
			current = value
		case []any:
			if !part.isIndex || part.index >= len(container) || container[part.index] == nil {
				return nil, copilotVSCodePathPart{}, false
			}
			current = container[part.index]
		default:
			return nil, copilotVSCodePathPart{}, false
		}
	}
	return current, path[len(path)-1], true
}

func copilotVSCodeTurns(state map[string]any) []model.Turn {
	sessionID, _ := state["sessionId"].(string)
	if sessionID == "" {
		return nil
	}
	requests, ok := state["requests"].([]any)
	if !ok {
		return nil
	}

	var turns []model.Turn
	for _, rawRequest := range requests {
		request, ok := rawRequest.(map[string]any)
		if !ok || !copilotVSCodeIsCopilotRequest(request) || !copilotVSCodeTerminal(request) {
			continue
		}
		requestID, _ := request["requestId"].(string)
		responseID, _ := request["responseId"].(string)
		if requestID == "" || responseID == "" {
			continue
		}
		timestamp := copilotVSCodeTimestamp(request)
		if timestamp.IsZero() {
			continue
		}
		totals, ok := copilotVSCodeModelTotals(request["modelTotals"])
		if !ok {
			continue
		}
		for _, total := range totals {
			if total.input < total.cached {
				continue
			}
			usage := model.Usage{
				Input:     total.input - total.cached,
				CacheRead: total.cached,
				Output:    total.output,
			}
			if usage.IsZero() {
				continue
			}
			turns = append(turns, model.Turn{
				Key:       identityKey("copilot-vscode-response", requestID, responseID, total.model),
				SessionID: sessionID,
				Agent:     model.AgentCopilot,
				Timestamp: timestamp,
				Model:     total.model,
				Provider:  "github-copilot",
				Usage:     usage,
				// modelTotals combines every call in the logical request, including
				// subagents. It cannot safely select a single-call context tier.
				Aggregate: true,
			})
		}
	}
	return turns
}

const (
	copilotVSCodeAgentPrefix      = "github.copilot."
	copilotVSCodeAgentHostCopilot = "agent-host-copilot"
)

// copilotVSCodeIsCopilotRequest gates VS Code's shared session store using
// the serialized ChatAgent identifier. Unknown and absent IDs stay out rather
// than being attributed to Copilot based only on a model name or token shape.
// agent-host-copilotcli deliberately stays out too: it shares Copilot CLI's
// session-state journal, which scanCopilotShutdownLogs already reads.
func copilotVSCodeIsCopilotRequest(request map[string]any) bool {
	agent, ok := request["agent"].(map[string]any)
	if !ok {
		return false
	}
	id, _ := agent["id"].(string)
	return strings.HasPrefix(id, copilotVSCodeAgentPrefix) ||
		id == copilotVSCodeAgentHostCopilot
}

// copilotVSCodeTerminal follows the current numeric ResponseModelState enum:
// Complete=1, Cancelled=2 and Failed=3. A cancelled or failed model request
// can have already consumed tokens, while Pending and NeedsInput remain mutable
// and are deliberately skipped. Older flat snapshots can lack modelState; VS
// Code revives a persisted response as complete unless isCanceled is set, so
// retain that narrow compatibility path without inspecting response content.
func copilotVSCodeTerminal(request map[string]any) bool {
	if rawState, exists := request["modelState"]; exists {
		state, ok := rawState.(map[string]any)
		if !ok {
			return false
		}
		value, ok := copilotVSCodeInteger(state["value"])
		return ok && (value == 1 || value == 2 || value == 3)
	}
	if canceled, _ := request["isCanceled"].(bool); canceled {
		return false
	}
	response, hasResponse := request["response"]
	return hasResponse && response != nil
}

type copilotVSCodeModelTotal struct {
	model  string
	input  int64
	cached int64
	output int64
}

// modelTotals is a whole-turn sum, not an individual request usage block. The
// store does not say whether cache writes occurred, so none are inferred.
// inputTokens and cachedTokens are treated as gross input and cache reads,
// respectively. If a row contradicts that relation, the file gives no safe
// alternative interpretation and that row is skipped.
func copilotVSCodeModelTotals(value any) ([]copilotVSCodeModelTotal, bool) {
	rows, ok := value.([]any)
	if !ok {
		return nil, false
	}
	totals := make([]copilotVSCodeModelTotal, 0, len(rows))
	byModel := make(map[string]int, len(rows))
	for _, rawRow := range rows {
		row, ok := rawRow.(map[string]any)
		if !ok {
			continue
		}
		modelID, _ := row["model"].(string)
		input, inputOK := copilotVSCodeTokenCount(row["inputTokens"])
		cached, cachedOK := copilotVSCodeTokenCount(row["cachedTokens"])
		output, outputOK := copilotVSCodeTokenCount(row["outputTokens"])
		if strings.TrimSpace(modelID) == "" || !inputOK || !cachedOK || !outputOK || cached > input {
			continue
		}
		if index, exists := byModel[modelID]; exists {
			merged, ok := copilotVSCodeMergeModelTotal(totals[index], input, cached, output)
			if !ok {
				continue
			}
			totals[index] = merged
			continue
		}
		byModel[modelID] = len(totals)
		totals = append(totals, copilotVSCodeModelTotal{model: modelID, input: input, cached: cached, output: output})
	}
	return totals, len(totals) > 0
}

func copilotVSCodeMergeModelTotal(previous copilotVSCodeModelTotal, input, cached, output int64) (copilotVSCodeModelTotal, bool) {
	var ok bool
	if previous.input, ok = copilotVSCodeAddTokens(previous.input, input); !ok {
		return copilotVSCodeModelTotal{}, false
	}
	if previous.cached, ok = copilotVSCodeAddTokens(previous.cached, cached); !ok {
		return copilotVSCodeModelTotal{}, false
	}
	if previous.output, ok = copilotVSCodeAddTokens(previous.output, output); !ok {
		return copilotVSCodeModelTotal{}, false
	}
	return previous, true
}

const copilotVSCodeMaxSafeInteger int64 = 9_007_199_254_740_991

func copilotVSCodeTokenCount(value any) (int64, bool) {
	count, ok := copilotVSCodeInteger(value)
	return count, ok && count >= 0
}

func copilotVSCodeAddTokens(left, right int64) (int64, bool) {
	if left > copilotVSCodeMaxSafeInteger-right {
		return 0, false
	}
	return left + right, true
}

func copilotVSCodeInteger(value any) (int64, bool) {
	var text string
	switch number := value.(type) {
	case json.Number:
		text = number.String()
	case int64:
		return number, number >= -copilotVSCodeMaxSafeInteger && number <= copilotVSCodeMaxSafeInteger
	case int:
		return int64(number), true
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < -float64(copilotVSCodeMaxSafeInteger) || number > float64(copilotVSCodeMaxSafeInteger) {
			return 0, false
		}
		return int64(number), true
	default:
		return 0, false
	}
	if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
		return integer, integer >= -copilotVSCodeMaxSafeInteger && integer <= copilotVSCodeMaxSafeInteger
	}
	decimal, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(decimal) || math.IsInf(decimal, 0) || math.Trunc(decimal) != decimal || decimal < -float64(copilotVSCodeMaxSafeInteger) || decimal > float64(copilotVSCodeMaxSafeInteger) {
		return 0, false
	}
	return int64(decimal), true
}

func copilotVSCodeTimestamp(request map[string]any) time.Time {
	if timestamp := copilotVSCodeTime(request["responseTimestamp"]); !timestamp.IsZero() {
		return timestamp
	}
	if state, ok := request["modelState"].(map[string]any); ok {
		if timestamp := copilotVSCodeTime(state["completedAt"]); !timestamp.IsZero() {
			return timestamp
		}
	}
	return copilotVSCodeTime(request["timestamp"])
}

func copilotVSCodeTime(value any) time.Time {
	if text, ok := value.(string); ok {
		if timestamp := parseTime(text); !timestamp.IsZero() {
			return timestamp
		}
	}
	milliseconds, ok := copilotVSCodeInteger(value)
	if !ok || milliseconds < 946_684_800_000 || milliseconds > 7_258_118_400_000 {
		// ChatSessionStore serializes Date.now() values in milliseconds. Do not
		// guess seconds or nanoseconds from an unknown schema.
		return time.Time{}
	}
	return time.UnixMilli(milliseconds).UTC()
}
