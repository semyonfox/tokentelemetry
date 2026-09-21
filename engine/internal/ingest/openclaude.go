package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// OpenClaude reads the Claude-shaped transcripts written by OpenClaude.
type OpenClaude struct{ root string }

func NewOpenClaude() *OpenClaude {
	root := os.Getenv("OPENCLAUDE_CONFIG_DIR")
	if root == "" {
		if home := homeDir(); home != "" {
			root = filepath.Join(home, ".openclaude")
		}
	}
	return &OpenClaude{root: existingDir(filepath.Join(root, "projects"))}
}

func newOpenClaudeAt(root string) *OpenClaude { return &OpenClaude{root: root} }
func (o *OpenClaude) Agent() model.Agent      { return model.Agent("openclaude") }
func (o *OpenClaude) Roots() []string {
	if o.root == "" {
		return nil
	}
	return []string{o.root}
}

type openClaudeLine struct {
	Type, SessionID, Timestamp, UUID, CWD string
	IsSidechain                           bool   `json:"isSidechain"`
	ActualModelLower                      string `json:"actualmodel"`
	ActualModel                           string `json:"actualModel"`
	Provider, Endpoint                    string
	Message                               struct {
		ID, Model, ActualModel, Provider, Endpoint string
		ActualModelLower                           string `json:"actualmodel"`
		Usage                                      *struct {
			Input      int64 `json:"input_tokens"`
			Output     int64 `json:"output_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

func (o *OpenClaude) Scan(ctx context.Context, emit func(model.Turn)) error {
	if o.root == "" {
		return nil
	}
	var paths []string
	var scanErr error
	err := filepath.WalkDir(o.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			scanErr = errors.Join(scanErr, err)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, turn := range mapFiles(ctx, paths, scanOpenClaudeFile) {
		emit(turn)
	}
	return scanErr
}

func scanOpenClaudeFile(path string) []model.Turn {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fallbackSession := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	var turns []model.Turn
	index := 0
	for line := range jsonLines(f) {
		var r openClaudeLine
		if json.Unmarshal(line, &r) != nil || r.Type != "assistant" || r.Message.Usage == nil {
			continue
		}
		usage := model.Usage{Input: r.Message.Usage.Input, Output: r.Message.Usage.Output, CacheRead: r.Message.Usage.CacheRead, CacheWrite: r.Message.Usage.CacheWrite}
		usage.ContextTokens = usage.Input + usage.CacheRead + usage.CacheWrite
		usage, ok := usage.Sanitize()
		if !ok || usage.IsZero() {
			continue
		}
		index++
		session := firstNonEmpty(r.SessionID, fallbackSession)
		modelID := firstNonEmpty(r.Message.ActualModel, r.Message.ActualModelLower, r.ActualModel, r.ActualModelLower, r.Message.Model)
		unpriced := ""
		if modelID == "" {
			modelID = "unknown"
			unpriced = "OpenClaude transcript does not record the request model"
		}
		id := firstNonEmpty(r.Message.ID, r.UUID)
		key := identityKey("openclaude-session", session, strconv.Itoa(index))
		if id != "" {
			key = identityKey("openclaude-native", id)
		}
		turns = append(turns, model.Turn{Key: key, SessionID: session, Agent: model.Agent("openclaude"), Timestamp: parseTime(r.Timestamp), Model: modelID,
			Provider: firstNonEmpty(r.Message.Provider, r.Provider), Endpoint: firstNonEmpty(r.Message.Endpoint, r.Endpoint), Project: r.CWD, Usage: usage,
			Subagent: r.IsSidechain, UnpricedReason: unpriced})
	}
	return turns
}
