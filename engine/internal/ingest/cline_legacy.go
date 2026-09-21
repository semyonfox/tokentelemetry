package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

const clineExtensionID = "saoudrizwan.claude-dev"

// Cline scans the extension's legacy task store. New SDK/CLI sessions use the
// unrelated version 1 store handled by ClineCLI.
type Cline struct {
	roots []string
}

func NewCline() *Cline {
	roots := vscodeGlobalStorageRoots(clineExtensionID)
	if data := clineDataDir(); data != "" {
		roots = append(roots, data)
	}
	return &Cline{roots: existingTaskRoots(roots)}
}

func newClineAt(roots ...string) *Cline { return &Cline{roots: roots} }

func (c *Cline) Agent() model.Agent { return model.AgentCline }

func (c *Cline) Roots() []string { return append([]string(nil), c.roots...) }

func (c *Cline) Scan(ctx context.Context, emit func(model.Turn)) error {
	return scanLegacyClineTasks(ctx, c.roots, model.AgentCline, false, emit)
}

func vscodeGlobalStorageRoots(extensionID string) []string {
	home := homeDir()
	if home == "" {
		return nil
	}
	var parents []string
	switch runtime.GOOS {
	case "darwin":
		parents = []string{
			filepath.Join(home, "Library", "Application Support", "Code", "User", "globalStorage"),
			filepath.Join(home, "Library", "Application Support", "Code - Insiders", "User", "globalStorage"),
			filepath.Join(home, "Library", "Application Support", "VSCodium", "User", "globalStorage"),
		}
	case "windows":
		roaming := os.Getenv("APPDATA")
		if roaming == "" {
			roaming = filepath.Join(home, "AppData", "Roaming")
		}
		parents = []string{
			filepath.Join(roaming, "Code", "User", "globalStorage"),
			filepath.Join(roaming, "Code - Insiders", "User", "globalStorage"),
			filepath.Join(roaming, "VSCodium", "User", "globalStorage"),
		}
	default:
		parents = []string{
			filepath.Join(home, ".config", "Code", "User", "globalStorage"),
			filepath.Join(home, ".config", "Code - Insiders", "User", "globalStorage"),
			filepath.Join(home, ".config", "VSCodium", "User", "globalStorage"),
		}
	}
	roots := make([]string, 0, len(parents))
	for _, parent := range parents {
		roots = append(roots, filepath.Join(parent, extensionID))
	}
	return roots
}

func existingTaskRoots(candidates []string) []string {
	seen := make(map[string]struct{}, len(candidates))
	var roots []string
	for _, candidate := range candidates {
		if existingDir(filepath.Join(candidate, "tasks")) == "" {
			continue
		}
		clean := filepath.Clean(candidate)
		if _, duplicate := seen[clean]; duplicate {
			continue
		}
		seen[clean] = struct{}{}
		roots = append(roots, clean)
	}
	return roots
}

type legacyClineMessage struct {
	Type string          `json:"type"`
	Say  string          `json:"say"`
	Text string          `json:"text"`
	TS   json.RawMessage `json:"ts"`
}

type legacyClineMetrics struct {
	TokensIn    int64  `json:"tokensIn"`
	TokensOut   int64  `json:"tokensOut"`
	CacheReads  int64  `json:"cacheReads"`
	CacheWrites int64  `json:"cacheWrites"`
	APIProtocol string `json:"apiProtocol"`
}

func scanLegacyClineTasks(ctx context.Context, roots []string, agent model.Agent, rooSemantics bool, emit func(model.Turn)) error {
	var firstErr error
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := os.ReadDir(filepath.Join(root, "tasks"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		var paths []string
		for _, entry := range entries {
			if entry.IsDir() {
				paths = append(paths, filepath.Join(root, "tasks", entry.Name()))
			}
		}
		for _, turn := range mapFiles(ctx, paths, func(path string) []model.Turn {
			return scanLegacyClineTask(path, agent, rooSemantics)
		}) {
			emit(turn)
		}
	}
	return firstErr
}

var (
	clineModelTag  = regexp.MustCompile(`<model>([^<]+)</model>`)
	clineWorkspace = regexp.MustCompile(`Current Workspace Directory \(([^)]+)\)`)
)

func scanLegacyClineTask(taskDir string, agent model.Agent, rooSemantics bool) []model.Turn {
	raw, err := os.ReadFile(filepath.Join(taskDir, "ui_messages.json"))
	if err != nil {
		return nil
	}
	var messages []legacyClineMessage
	if json.Unmarshal(raw, &messages) != nil {
		return nil
	}
	modelID, project := legacyClineHistoryMeta(taskDir)
	if modelID == "" {
		modelID = "unknown"
	}
	taskID := filepath.Base(taskDir)
	var turns []model.Turn
	requestIndex := 0
	for _, message := range messages {
		if message.Type != "say" || message.Say != "api_req_started" {
			continue
		}
		index := requestIndex
		requestIndex++
		var metrics legacyClineMetrics
		if message.Text == "" || json.Unmarshal([]byte(message.Text), &metrics) != nil {
			continue
		}
		// Classic Cline persists tokensIn as uncached input. Roo changed its
		// contract to gross input when it added apiProtocol to each request;
		// older Roo rows without that marker retain the classic disjoint shape.
		tokensIn := nonnegativeClineCount(metrics.TokensIn)
		cacheReads := nonnegativeClineCount(metrics.CacheReads)
		cacheWrites := nonnegativeClineCount(metrics.CacheWrites)
		netInput := tokensIn
		contextTokens := tokensIn + cacheReads + cacheWrites
		if rooSemantics && metrics.APIProtocol != "" {
			netInput = tokensIn - cacheReads - cacheWrites
			if netInput < 0 {
				netInput = 0
			}
			contextTokens = tokensIn
		}
		usage := model.Usage{
			Input: netInput, Output: metrics.TokensOut,
			CacheRead: cacheReads, CacheWrite: cacheWrites,
			ContextTokens: contextTokens,
		}
		usage, ok := usage.Sanitize()
		if !ok || usage.IsZero() {
			continue
		}
		turns = append(turns, model.Turn{
			Key:            string(agent) + "|" + taskID + "|" + strconv.Itoa(index),
			SessionID:      taskID,
			Agent:          agent,
			Timestamp:      parseLegacyClineTime(message.TS),
			Model:          modelID,
			Project:        project,
			Usage:          usage,
			UnpricedReason: "Legacy Cline-family task history does not preserve per-call model attribution",
		})
	}
	return turns
}

func parseLegacyClineTime(raw json.RawMessage) time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}
	}
	var millis int64
	if json.Unmarshal(raw, &millis) == nil {
		return unixMillis(millis)
	}
	var stamp string
	if json.Unmarshal(raw, &stamp) == nil {
		return parseTime(stamp)
	}
	return time.Time{}
}

func legacyClineHistoryMeta(taskDir string) (modelID, project string) {
	raw, err := os.ReadFile(filepath.Join(taskDir, "api_conversation_history.json"))
	if err != nil {
		return "", ""
	}
	var messages []struct {
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &messages) != nil {
		return "", ""
	}
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		for _, block := range message.Content {
			if modelID == "" {
				if match := clineModelTag.FindStringSubmatch(block.Text); len(match) == 2 {
					modelID = strings.TrimSpace(match[1])
				}
			}
			if project == "" {
				if match := clineWorkspace.FindStringSubmatch(block.Text); len(match) == 2 {
					project = strings.TrimSpace(match[1])
				}
			}
		}
		if modelID != "" && project != "" {
			break
		}
	}
	return modelID, project
}
