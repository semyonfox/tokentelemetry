package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// KimiCode reads the request/usage event pairs written by the Kimi Code CLI.
type KimiCode struct{ root string }

func NewKimiCode() *KimiCode {
	return &KimiCode{root: envDir("KIMI_CODE_HOME", ".kimi-code")}
}

func newKimiCodeAt(root string) *KimiCode { return &KimiCode{root: root} }
func (k *KimiCode) Agent() model.Agent    { return model.Agent("kimicode") }
func (k *KimiCode) Roots() []string {
	if k.root == "" {
		return nil
	}
	sessions := existingDir(filepath.Join(k.root, "sessions"))
	if sessions == "" {
		return nil
	}
	return []string{sessions}
}

func (k *KimiCode) Scan(ctx context.Context, emit func(model.Turn)) error {
	if len(k.Roots()) == 0 {
		return nil
	}
	var scanErr error
	err := filepath.WalkDir(filepath.Join(k.root, "sessions"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			scanErr = errors.Join(scanErr, walkErr)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "wire.jsonl" || filepath.Base(filepath.Dir(filepath.Dir(path))) != "agents" {
			return nil
		}
		if err := scanKimiCodeWire(path, emit); err != nil {
			scanErr = errors.Join(scanErr, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return scanErr
}

type kimiCodeRequest struct {
	model, alias, turn string
	timestamp          json.RawMessage
	line               int
}

func scanKimiCodeWire(path string, emit func(model.Turn)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("kimicode: open %s: %w", path, err)
	}
	defer f.Close()
	agentID := filepath.Base(filepath.Dir(path))
	sessionDir := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	sessionID := strings.TrimPrefix(filepath.Base(sessionDir), "session_")
	project := kimiCodeProject(sessionDir)
	aliases := map[string]string{}
	var request *kimiCodeRequest
	lineNumber := 0
	var scanErr error
	for line := range jsonLines(f) {
		lineNumber++
		var record struct {
			Type       string          `json:"type"`
			AgentID    string          `json:"agentId"`
			Model      string          `json:"model"`
			ModelAlias string          `json:"modelAlias"`
			TurnStep   string          `json:"turnStep"`
			UsageScope string          `json:"usageScope"`
			Time       json.RawMessage `json:"time"`
			Usage      *struct {
				InputOther       int64 `json:"inputOther"`
				Output           int64 `json:"output"`
				InputCacheRead   int64 `json:"inputCacheRead"`
				InputCacheCreate int64 `json:"inputCacheCreation"`
			} `json:"usage"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		// A main-agent journal can mirror child-agent events. Only events owned
		// by this journal's agent may affect its outstanding request. Older
		// migrated records can lack agentId and remain attributable to their file.
		if record.AgentID != "" && record.AgentID != agentID {
			continue
		}
		switch record.Type {
		case "llm.request":
			if record.Model != "" && record.ModelAlias != "" {
				aliases[record.ModelAlias] = record.Model
			}
			turn := strings.SplitN(record.TurnStep, ".", 2)[0]
			request = &kimiCodeRequest{model: record.Model, alias: record.ModelAlias, turn: turn, timestamp: record.Time, line: lineNumber}
		case "usage.record":
			if record.UsageScope != "" && record.UsageScope != "turn" && record.UsageScope != "session" {
				continue
			}
			if request == nil || record.Usage == nil {
				continue
			}
			// usage.record names the request's model alias. Do not let an
			// unrelated record consume a still-outstanding request.
			if usageModel := strings.TrimSpace(record.Model); usageModel != "" &&
				usageModel != strings.TrimSpace(request.alias) &&
				usageModel != strings.TrimSpace(request.model) &&
				aliases[usageModel] != strings.TrimSpace(request.model) {
				continue
			}
			raw := record.Usage
			usage := model.Usage{Input: raw.InputOther, Output: raw.Output, CacheRead: raw.InputCacheRead, CacheWrite: raw.InputCacheCreate}
			usage, ok := usage.Sanitize()
			if !ok {
				request = nil
				continue
			}
			usage.ContextTokens = usage.Input + usage.CacheRead + usage.CacheWrite
			if usage.ContextTokens > model.MaxPerCallTokens {
				request = nil
				continue
			}
			modelID := strings.TrimSpace(request.model)
			if modelID == "" {
				modelID = aliases[strings.TrimSpace(record.Model)]
			}
			if modelID == "" {
				modelID = aliases[request.alias]
			}
			unpriced := ""
			if modelID == "" {
				modelID = "unknown"
				unpriced = "Kimi Code usage could not be correlated to a recorded request model"
			}
			timestamp := kimiWireTime(record.Time)
			if timestamp.IsZero() {
				timestamp = kimiWireTime(request.timestamp)
			}
			if timestamp.IsZero() {
				scanErr = errors.Join(scanErr, fmt.Errorf("kimicode: %s line %d has measured usage without a request timestamp", path, lineNumber))
				request = nil
				continue
			}
			if !usage.IsZero() {
				emit(model.Turn{
					Key:       identityKey("kimicode", sessionID, agentID, strconv.Itoa(request.line)),
					SessionID: sessionID, Agent: model.Agent("kimicode"), Timestamp: timestamp,
					Model: modelID, Provider: "kimicode", Project: project, Usage: usage,
					Subagent: agentID != "main", UnpricedReason: unpriced,
				})
			}
			request = nil
		}
	}
	return scanErr
}

func kimiCodeProject(sessionDir string) string {
	b, err := os.ReadFile(filepath.Join(sessionDir, "state.json"))
	if err == nil {
		var state struct {
			WorkDir string `json:"workDir"`
			CWD     string `json:"cwd"`
		}
		if json.Unmarshal(b, &state) == nil {
			if state.WorkDir != "" {
				return state.WorkDir
			}
			if state.CWD != "" {
				return state.CWD
			}
		}
	}
	return filepath.Base(filepath.Dir(sessionDir))
}
