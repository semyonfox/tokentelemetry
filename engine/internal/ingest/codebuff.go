package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Codebuff reads completed UI messages. Credits are Codebuff's native meter;
// they remain credits and are never converted to dollars.
type Codebuff struct{ roots []string }

func NewCodebuff() *Codebuff {
	if root := strings.TrimSpace(os.Getenv("CODEBUFF_DATA_DIR")); root != "" {
		return &Codebuff{roots: existingCodebuffRoots(root)}
	}
	h := homeDir()
	if h == "" {
		return &Codebuff{}
	}
	base := filepath.Join(h, ".config")
	return &Codebuff{roots: existingCodebuffRoots(filepath.Join(base, "manicode"), filepath.Join(base, "manicode-dev"), filepath.Join(base, "manicode-staging"))}
}
func newCodebuffAt(roots ...string) *Codebuff { return &Codebuff{roots: roots} }
func (c *Codebuff) Agent() model.Agent        { return model.Agent("codebuff") }
func (c *Codebuff) Roots() []string           { return append([]string(nil), c.roots...) }

func existingCodebuffRoots(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if p = existingDir(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type codebuffUsage struct {
	InputTokens           int64 `json:"inputTokens"`
	InputTokensSnake      int64 `json:"input_tokens"`
	PromptTokens          int64 `json:"promptTokens"`
	PromptTokensSnake     int64 `json:"prompt_tokens"`
	OutputTokens          int64 `json:"outputTokens"`
	OutputTokensSnake     int64 `json:"output_tokens"`
	CompletionTokens      int64 `json:"completionTokens"`
	CompletionTokensSnake int64 `json:"completion_tokens"`
	CacheWrite            int64 `json:"cacheCreationInputTokens"`
	CacheWriteSnake       int64 `json:"cache_creation_input_tokens"`
	CacheRead             int64 `json:"cacheReadInputTokens"`
	CacheReadSnake        int64 `json:"cache_read_input_tokens"`
	CachedInput           int64 `json:"cachedInputTokens"`
	Reasoning             int64 `json:"reasoningOutputTokens"`
}
type codebuffMessage struct {
	ID, Variant, Role, Timestamp string
	Credits                      *float64 `json:"credits"`
	Metadata                     struct {
		Model, ModelID string
		Timestamp      json.RawMessage `json:"timestamp"`
		Usage          *codebuffUsage  `json:"usage"`
		Codebuff       struct {
			Model string         `json:"model"`
			Usage *codebuffUsage `json:"usage"`
		} `json:"codebuff"`
	} `json:"metadata"`
}
type codebuffRunState struct {
	CWD          string `json:"cwd"`
	SessionState struct {
		CWD            string `json:"cwd"`
		ProjectContext struct {
			CWD string `json:"cwd"`
		} `json:"projectContext"`
		FileContext struct {
			CWD string `json:"cwd"`
		} `json:"fileContext"`
	} `json:"sessionState"`
}

func (c *Codebuff) Scan(ctx context.Context, emit func(model.Turn)) error {
	var scanErr error
	for _, root := range c.roots {
		projects, err := os.ReadDir(filepath.Join(root, "projects"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				scanErr = errors.Join(scanErr, err)
			}
			continue
		}
		for _, project := range projects {
			if !project.IsDir() {
				continue
			}
			chats := filepath.Join(root, "projects", project.Name(), "chats")
			entries, err := os.ReadDir(chats)
			if err != nil {
				continue
			}
			for _, chat := range entries {
				if !chat.IsDir() {
					continue
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				dir := filepath.Join(chats, chat.Name())
				for _, turn := range scanCodebuffChat(root, project.Name(), dir) {
					emit(turn)
				}
			}
		}
	}
	return scanErr
}

func scanCodebuffChat(root, fallbackProject, dir string) []model.Turn {
	raw, err := os.ReadFile(filepath.Join(dir, "chat-messages.json"))
	if err != nil {
		return nil
	}
	var messages []codebuffMessage
	if json.Unmarshal(raw, &messages) != nil {
		return nil
	}
	project := fallbackProject
	if stateRaw, err := os.ReadFile(filepath.Join(dir, "run-state.json")); err == nil {
		var state codebuffRunState
		if json.Unmarshal(stateRaw, &state) == nil {
			project = firstNonEmpty(state.SessionState.ProjectContext.CWD, state.SessionState.FileContext.CWD, state.SessionState.CWD, state.CWD, project)
		}
	}
	chatID := filepath.Base(dir)
	session := filepath.Base(root) + "/" + chatID
	fallbackTime := codebuffChatTime(chatID)
	var turns []model.Turn
	for i, msg := range messages {
		variant := firstNonEmpty(msg.Variant, msg.Role)
		if variant != "ai" && variant != "agent" && variant != "assistant" {
			continue
		}
		credits := validCredits(msg.Credits)
		usage := codebuffMessageUsage(msg)
		if usage.IsZero() && credits == nil {
			continue
		}
		modelID := firstNonEmpty(msg.Metadata.Model, msg.Metadata.ModelID, msg.Metadata.Codebuff.Model)
		unpriced := ""
		if modelID == "" {
			modelID = "unknown"
			if !usage.IsZero() {
				unpriced = "Codebuff message does not record the upstream model"
			}
		}
		id := strings.TrimSpace(msg.ID)
		key := ""
		if id != "" {
			key = identityKey("codebuff-message", id)
		} else {
			key = identityKey("codebuff-message", filepath.Clean(dir), strconv.Itoa(i))
		}
		stamp := parseTime(msg.Timestamp)
		if stamp.IsZero() {
			stamp = parseFlexibleTime(msg.Metadata.Timestamp)
		}
		if stamp.IsZero() {
			stamp = fallbackTime
		}
		turns = append(turns, model.Turn{Key: key, SessionID: session, Agent: model.Agent("codebuff"), Timestamp: stamp, Model: modelID, Project: project, Usage: usage, Credits: credits, UnpricedReason: unpriced})
	}
	return turns
}

func codebuffMessageUsage(msg codebuffMessage) model.Usage {
	u := msg.Metadata.Usage
	if u == nil {
		u = msg.Metadata.Codebuff.Usage
	}
	if u == nil {
		return model.Usage{}
	}
	grossInput := firstNonzero(u.InputTokens, u.InputTokensSnake, u.PromptTokens, u.PromptTokensSnake)
	cacheRead := firstNonzero(u.CachedInput, u.CacheRead, u.CacheReadSnake)
	output := firstNonzero(u.OutputTokens, u.OutputTokensSnake, u.CompletionTokens, u.CompletionTokensSnake)
	cacheWrite := firstNonzero(u.CacheWrite, u.CacheWriteSnake)
	counts, ok := sanitizeCallCounts(grossInput, output, cacheRead, cacheWrite, u.Reasoning)
	if !ok {
		return model.Usage{}
	}
	grossInput, output, cacheRead, cacheWrite, reasoning := counts[0], counts[1], counts[2], counts[3], counts[4]
	netInput := grossInput - cacheRead
	if netInput < 0 {
		netInput = 0
	}
	usage := model.Usage{Input: netInput, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite, Reasoning: reasoning, ContextTokens: grossInput}
	usage, ok = usage.Sanitize()
	if !ok {
		return model.Usage{}
	}
	return usage
}

// sanitizeCallCounts validates raw per-request buckets before callers subtract
// or add them. Usage.Sanitize remains the final check on normalized buckets.
func sanitizeCallCounts(values ...int64) ([]int64, bool) {
	out := append([]int64(nil), values...)
	for i, value := range out {
		if value < 0 {
			out[i] = 0
			continue
		}
		if value > model.MaxPerCallTokens {
			return nil, false
		}
	}
	return out, true
}
func firstNonzero(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}
func validCredits(value *float64) *float64 {
	if value == nil || *value <= 0 || math.IsNaN(*value) || math.IsInf(*value, 0) {
		return nil
	}
	v := *value
	return &v
}
func parseFlexibleTime(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return parseTime(text)
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		if n < 1_000_000_000_000 {
			return time.Unix(n, 0)
		}
		return time.UnixMilli(n)
	}
	return time.Time{}
}

var codebuffChatStamp = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2})-(\d{2})-(\d{2})(.*)$`)

func codebuffChatTime(id string) time.Time {
	match := codebuffChatStamp.FindStringSubmatch(id)
	if len(match) == 5 {
		return parseTime(match[1] + ":" + match[2] + ":" + match[3] + match[4])
	}
	return time.Time{}
}
