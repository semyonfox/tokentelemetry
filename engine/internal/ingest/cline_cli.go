package ingest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// ClineCLI scans Cline's version 1 SDK/CLI session store.
//
// Layout:
//
//	~/.cline/data/sessions/<session-id>/<session-id>.json
//	~/.cline/data/sessions/<session-id>/<session-id>.messages.json
//
// The manifest identifies the session and its default route. Assistant
// messages carry per-call metrics and may override the model and provider.
// SDK inputTokens is the whole prompt, including cache reads and writes, so it
// is reduced to TokenTelemetry's disjoint uncached-input bucket here.
type ClineCLI struct {
	root string
}

func NewClineCLI() *ClineCLI {
	return &ClineCLI{root: clineSessionsDir()}
}

func newClineCLIAt(root string) *ClineCLI { return &ClineCLI{root: root} }

func clineDataDir() string {
	if dir := strings.TrimSpace(os.Getenv("CLINE_DATA_DIR")); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv("CLINE_DIR")); dir != "" {
		return filepath.Join(dir, "data")
	}
	if home := homeDir(); home != "" {
		return filepath.Join(home, ".cline", "data")
	}
	return ""
}

func clineSessionsDir() string {
	if dir := strings.TrimSpace(os.Getenv("CLINE_SESSION_DATA_DIR")); dir != "" {
		return existingDir(dir)
	}
	if data := clineDataDir(); data != "" {
		return existingDir(filepath.Join(data, "sessions"))
	}
	return ""
}

func (c *ClineCLI) Agent() model.Agent { return model.Agent("cline-cli") }

func (c *ClineCLI) Roots() []string {
	if c.root == "" || existingDir(c.root) == "" {
		return nil
	}
	return []string{c.root}
}

type clineManifest struct {
	Version       int                        `json:"version"`
	SessionID     string                     `json:"session_id"`
	StartedAt     string                     `json:"started_at"`
	EndedAt       string                     `json:"ended_at"`
	Provider      string                     `json:"provider"`
	Model         string                     `json:"model"`
	CWD           string                     `json:"cwd"`
	WorkspaceRoot string                     `json:"workspace_root"`
	Metadata      map[string]json.RawMessage `json:"metadata"`
}

type clineMessagesDocument struct {
	Version   int            `json:"version"`
	Agent     string         `json:"agent"`
	SessionID string         `json:"sessionId"`
	Messages  []clineMessage `json:"messages"`
}

type clineMessage struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	SessionID string `json:"sessionId"`
	TS        int64  `json:"ts"`
	ModelInfo struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
	} `json:"modelInfo"`
	Metrics *clineMetrics `json:"metrics"`
}

type clineMetrics struct {
	Input      int64 `json:"inputTokens"`
	Output     int64 `json:"outputTokens"`
	CacheRead  int64 `json:"cacheReadTokens"`
	CacheWrite int64 `json:"cacheWriteTokens"`
	Reasoning  int64 `json:"reasoningTokenCount"`
}

func (c *ClineCLI) Scan(ctx context.Context, emit func(model.Turn)) error {
	roots := c.Roots()
	if len(roots) == 0 {
		return nil
	}
	entries, err := os.ReadDir(roots[0])
	if err != nil {
		return err
	}
	var paths []string
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		paths = append(paths, filepath.Join(roots[0], id, id+".json"))
	}
	for _, turn := range mapFiles(ctx, paths, c.scanManifest) {
		emit(turn)
	}
	return nil
}

func (c *ClineCLI) scanManifest(path string) []model.Turn {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var manifest clineManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Version != 1 {
		return nil
	}

	dir := filepath.Dir(path)
	fileSessionID := strings.TrimSuffix(filepath.Base(path), ".json")
	sessionID := manifest.SessionID
	if sessionID == "" {
		sessionID = fileSessionID
	}
	messagesPath := filepath.Join(dir, fileSessionID+".messages.json")
	messagesRaw, err := os.ReadFile(messagesPath)
	if err != nil {
		return c.clineRollup(manifest, sessionID)
	}
	var doc clineMessagesDocument
	if json.Unmarshal(messagesRaw, &doc) != nil {
		// The file is rewritten while a live session runs. A torn read should
		// still use the manifest rollup when one is available.
		return c.clineRollup(manifest, sessionID)
	}
	if doc.Version != 1 {
		return nil
	}
	if doc.SessionID != "" {
		sessionID = doc.SessionID
	}

	project := manifest.WorkspaceRoot
	if project == "" {
		project = manifest.CWD
	}
	fallbackTime := parseTime(manifest.StartedAt)
	subagent := doc.Agent != "" && doc.Agent != "lead"
	turns := make([]model.Turn, 0, len(doc.Messages))
	hadMetrics := false
	for i, message := range doc.Messages {
		if message.Role != "assistant" || message.Metrics == nil {
			continue
		}
		m := message.Metrics
		grossInput := nonnegativeClineCount(m.Input)
		cacheRead := nonnegativeClineCount(m.CacheRead)
		cacheWrite := nonnegativeClineCount(m.CacheWrite)
		netInput := grossInput - cacheRead - cacheWrite
		if netInput < 0 {
			netInput = 0
		}
		usage := model.Usage{
			Input:         netInput,
			Output:        m.Output,
			CacheRead:     cacheRead,
			CacheWrite:    cacheWrite,
			Reasoning:     m.Reasoning,
			ContextTokens: grossInput,
		}
		usage, ok := usage.Sanitize()
		if !ok || usage.IsZero() {
			continue
		}
		hadMetrics = true
		modelID := message.ModelInfo.ID
		if modelID == "" {
			modelID = manifest.Model
		}
		if modelID == "" {
			modelID = "unknown"
		}
		provider := message.ModelInfo.Provider
		if provider == "" {
			provider = manifest.Provider
		}
		messageID := message.ID
		if messageID == "" {
			messageID = strconv.Itoa(i)
		}
		owner := sessionID
		if message.SessionID != "" {
			owner = message.SessionID
		}
		timestamp := unixMillis(message.TS)
		if timestamp.IsZero() {
			timestamp = fallbackTime
		}
		turns = append(turns, model.Turn{
			Key:       "cline-cli|" + owner + "|" + messageID,
			SessionID: owner,
			Agent:     model.Agent("cline-cli"),
			Timestamp: timestamp,
			Model:     modelID,
			Provider:  provider,
			Project:   project,
			Usage:     usage,
			Subagent:  subagent,
		})
	}
	if hadMetrics {
		return turns
	}
	return c.clineRollup(manifest, sessionID)
}

type clineRollupUsage struct {
	Input      int64 `json:"inputTokens"`
	Output     int64 `json:"outputTokens"`
	CacheRead  int64 `json:"cacheReadTokens"`
	CacheWrite int64 `json:"cacheWriteTokens"`
	Reasoning  int64 `json:"reasoningTokenCount"`
}

func (c *ClineCLI) clineRollup(manifest clineManifest, sessionID string) []model.Turn {
	raw, ok := manifest.Metadata["usage"]
	if !ok {
		return nil
	}
	var rollup clineRollupUsage
	if json.Unmarshal(raw, &rollup) != nil {
		return nil
	}
	grossInput := nonnegativeClineCount(rollup.Input)
	cacheRead := nonnegativeClineCount(rollup.CacheRead)
	cacheWrite := nonnegativeClineCount(rollup.CacheWrite)
	netInput := grossInput - cacheRead - cacheWrite
	if netInput < 0 {
		netInput = 0
	}
	usage := model.Usage{
		Input: netInput, Output: rollup.Output,
		CacheRead: cacheRead, CacheWrite: cacheWrite,
		Reasoning: rollup.Reasoning, ContextTokens: grossInput,
	}
	usage, valid := usage.Sanitize()
	if !valid || usage.IsZero() {
		return nil
	}
	project := manifest.WorkspaceRoot
	if project == "" {
		project = manifest.CWD
	}
	timestamp := parseTime(manifest.EndedAt)
	if timestamp.IsZero() {
		timestamp = parseTime(manifest.StartedAt)
	}
	modelID := manifest.Model
	if modelID == "" {
		modelID = "unknown"
	}
	return []model.Turn{{
		Key: "cline-cli|" + sessionID + "|rollup", SessionID: sessionID,
		Agent: model.Agent("cline-cli"), Timestamp: timestamp,
		Model: modelID, Provider: manifest.Provider, Project: project,
		Usage: usage, Aggregate: true,
	}}
}

func nonnegativeClineCount(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
