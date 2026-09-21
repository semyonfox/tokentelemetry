package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Qwen reads Qwen Code's append-only chat records.
type Qwen struct{ root string }

func NewQwen() *Qwen {
	if root := os.Getenv("QWEN_DATA_DIR"); root != "" {
		return &Qwen{root: existingDir(root)}
	}
	if home := os.Getenv("QWEN_HOME"); home != "" {
		return &Qwen{root: existingDir(filepath.Join(home, "projects"))}
	}
	home := homeDir()
	if home == "" {
		return &Qwen{}
	}
	return &Qwen{root: existingDir(filepath.Join(home, ".qwen", "projects"))}
}

func newQwenAt(root string) *Qwen  { return &Qwen{root: root} }
func (q *Qwen) Agent() model.Agent { return model.Agent("qwen") }
func (q *Qwen) Roots() []string {
	if q.root == "" {
		return nil
	}
	return []string{q.root}
}

type qwenRecord struct {
	UUID        string `json:"uuid"`
	SessionID   string `json:"sessionId"`
	Timestamp   string `json:"timestamp"`
	Type        string `json:"type"`
	CWD         string `json:"cwd"`
	Model       string `json:"model"`
	IsSidechain bool   `json:"isSidechain"`
	ForkedFrom  *struct {
		SessionID   string `json:"sessionId"`
		MessageUUID string `json:"messageUuid"`
	} `json:"forkedFrom"`
	Usage *struct {
		Prompt     int64 `json:"promptTokenCount"`
		Candidates int64 `json:"candidatesTokenCount"`
		Thoughts   int64 `json:"thoughtsTokenCount"`
		Cached     int64 `json:"cachedContentTokenCount"`
	} `json:"usageMetadata"`
}

func (q *Qwen) Scan(ctx context.Context, emit func(model.Turn)) error {
	if q.root == "" {
		return nil
	}
	var scanErr error
	err := filepath.WalkDir(q.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			scanErr = errors.Join(scanErr, walkErr)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") || !qwenChatPath(path) {
			return nil
		}
		if err := q.scanFile(path, emit); err != nil {
			scanErr = errors.Join(scanErr, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return scanErr
}

func qwenChatPath(path string) bool {
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "chats" {
			return true
		}
	}
	return false
}

func (q *Qwen) scanFile(path string, emit func(model.Turn)) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("qwen: open %s: %w", path, err)
	}
	defer f.Close()
	project := qwenProject(path)
	var scanErr error
	for line := range jsonLines(f) {
		var record qwenRecord
		if json.Unmarshal(line, &record) != nil || record.Type != "assistant" || record.Usage == nil {
			continue
		}
		if record.SessionID == "" || record.UUID == "" {
			continue
		}
		raw := record.Usage
		if raw.Prompt < 0 || raw.Candidates < 0 || raw.Thoughts < 0 || raw.Cached < 0 || raw.Cached > raw.Prompt ||
			raw.Prompt > model.MaxPerCallTokens || raw.Candidates > model.MaxPerCallTokens-raw.Thoughts {
			continue
		}
		usage := model.Usage{
			Input: raw.Prompt - raw.Cached, Output: raw.Candidates + raw.Thoughts,
			CacheRead: raw.Cached, Reasoning: raw.Thoughts, ContextTokens: raw.Prompt,
		}
		usage, ok := usage.Sanitize()
		if !ok || usage.IsZero() {
			continue
		}
		timestamp := parseTime(record.Timestamp)
		if timestamp.IsZero() {
			scanErr = errors.Join(scanErr, fmt.Errorf("qwen: %s has measured usage without a timestamp", path))
			continue
		}
		if record.CWD != "" {
			project = record.CWD
		}
		modelID := record.Model
		unpriced := ""
		if modelID == "" {
			modelID = "unknown"
			unpriced = "Qwen chat record does not identify the request model"
		}
		ownerSession, ownerMessage := record.SessionID, record.UUID
		if record.ForkedFrom != nil && record.ForkedFrom.SessionID != "" && record.ForkedFrom.MessageUUID != "" {
			ownerSession, ownerMessage = record.ForkedFrom.SessionID, record.ForkedFrom.MessageUUID
		}
		emit(model.Turn{
			Key: identityKey("qwen", ownerSession, ownerMessage), SessionID: record.SessionID,
			Agent: model.Agent("qwen"), Timestamp: timestamp, Model: modelID,
			Provider: "qwen", Project: project, Usage: usage, Subagent: record.IsSidechain, UnpricedReason: unpriced,
		})
	}
	return scanErr
}

func qwenProject(path string) string {
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "chats" {
			return filepath.Base(filepath.Dir(dir))
		}
	}
	return ""
}
