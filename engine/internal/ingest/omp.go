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

type OMP struct{ root string }

func NewOMP() *OMP {
	agentDir := os.Getenv("PI_CODING_AGENT_DIR")
	if agentDir == "" {
		if h := homeDir(); h != "" {
			agentDir = filepath.Join(h, ".omp", "agent")
		}
	}
	return &OMP{root: existingDir(filepath.Join(agentDir, "sessions"))}
}
func newOMPAt(root string) *OMP   { return &OMP{root: root} }
func (o *OMP) Agent() model.Agent { return model.Agent("omp") }
func (o *OMP) Roots() []string {
	if o.root == "" {
		return nil
	}
	return []string{o.root}
}

type ompUsage struct{ Input, Output, CacheRead, CacheWrite int64 }
type ompRecord struct {
	Type, ID, ParentID, Timestamp, CWD, ParentSession, Provider, Model, ResponseID string
	Message                                                                        struct {
		Role, Provider, Model, ResponseID string
		Usage                             *ompUsage `json:"usage"`
	} `json:"message"`
	Usage *ompUsage `json:"usage"`
}

func (o *OMP) Scan(ctx context.Context, emit func(model.Turn)) error {
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
	for _, turn := range mapFiles(ctx, paths, o.scanFile) {
		emit(turn)
	}
	return scanErr
}

func (o *OMP) scanFile(path string) []model.Turn {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	session := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	project, parentSession, currentModel, currentProvider := "", "", "", ""
	var turns []model.Turn
	index := 0
	for line := range jsonLines(f) {
		var r ompRecord
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		switch r.Type {
		case "session":
			session = firstNonEmpty(r.ID, session)
			project = r.CWD
			parentSession = r.ParentSession
			continue
		case "model_change":
			parts := strings.SplitN(r.Model, "/", 2)
			if len(parts) == 2 {
				currentProvider, currentModel = parts[0], parts[1]
			} else {
				currentModel = r.Model
			}
			continue
		}
		var usage *ompUsage
		modelID, provider, responseID := "", "", ""
		if r.Type == "message" && r.Message.Role == "assistant" {
			usage = r.Message.Usage
			modelID = r.Message.Model
			provider = r.Message.Provider
			responseID = r.Message.ResponseID
		}
		if r.Type == "model_usage" {
			usage = r.Usage
			modelID = r.Model
			provider = r.Provider
			responseID = r.ResponseID
		}
		if usage == nil {
			continue
		}
		u := model.Usage{Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite}
		u.ContextTokens = u.Input + u.CacheRead + u.CacheWrite
		u, ok := u.Sanitize()
		if !ok || u.IsZero() {
			continue
		}
		index++
		modelID = firstNonEmpty(modelID, currentModel)
		provider = firstNonEmpty(provider, currentProvider)
		unpriced := ""
		if modelID == "" {
			modelID = "unknown"
			unpriced = "OMP session entry does not record the request model"
		}
		key := identityKey("omp-session", session, strconv.Itoa(index))
		if responseID != "" {
			key = identityKey("omp-response", responseID)
		} else if r.ID != "" {
			key = identityKey("omp-entry", r.ID)
		}
		turns = append(turns, model.Turn{Key: key, SessionID: session, Agent: model.Agent("omp"), Timestamp: parseTime(r.Timestamp), Model: modelID, Provider: provider,
			Project: project, Usage: u, Subagent: parentSession != "" || ompNestedSession(o.root, path), UnpricedReason: unpriced})
	}
	return turns
}

func ompNestedSession(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return len(strings.Split(filepath.Clean(rel), string(filepath.Separator))) > 2
}
