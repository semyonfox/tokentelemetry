package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Mux reads native assistant totals, including separately billed tool models.
// These totals can contain several API steps, so they remain aggregates.
type Mux struct{ roots []string }

func NewMux() *Mux {
	for _, key := range []string{"XUM_ROOT", "MUX_ROOT"} {
		if root := os.Getenv(key); root != "" {
			return &Mux{roots: []string{root}}
		}
	}
	return &Mux{roots: []string{filepath.Join(homeDir(), ".xum"), filepath.Join(homeDir(), ".mux"), filepath.Join(homeDir(), ".cmux")}}
}
func (*Mux) Agent() model.Agent { return model.Agent("mux") }
func (m *Mux) Roots() []string {
	var roots []string
	for _, r := range m.roots {
		if existingDir(filepath.Join(r, "sessions")) != "" {
			roots = append(roots, r)
		}
	}
	return roots
}

type muxUsage struct {
	Input        int64 `json:"inputTokens"`
	Output       int64 `json:"outputTokens"`
	Cached       int64 `json:"cachedInputTokens"`
	Reasoning    int64 `json:"reasoningTokens"`
	InputDetails *struct {
		Fresh int64 `json:"noCacheTokens"`
		Read  int64 `json:"cacheReadTokens"`
		Write int64 `json:"cacheWriteTokens"`
	} `json:"inputTokenDetails"`
	OutputDetails *struct {
		Reasoning int64 `json:"reasoningTokens"`
	} `json:"outputTokenDetails"`
}
type muxProviderMetadata struct {
	Anthropic struct {
		Write int64 `json:"cacheCreationInputTokens"`
	} `json:"anthropic"`
}
type muxToolUsage struct {
	ToolCallID string              `json:"toolCallId"`
	Model      string              `json:"model"`
	Timestamp  int64               `json:"timestamp"`
	Usage      *muxUsage           `json:"usage"`
	Metadata   muxProviderMetadata `json:"providerMetadata"`
}
type muxMessage struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Created  string `json:"createdAt"`
	Metadata struct {
		Model     string              `json:"model"`
		Route     string              `json:"routeProvider"`
		Timestamp int64               `json:"timestamp"`
		Usage     *muxUsage           `json:"usage"`
		Provider  muxProviderMetadata `json:"providerMetadata"`
		Tools     []muxToolUsage      `json:"toolModelUsages"`
	} `json:"metadata"`
}

func (m *Mux) Scan(ctx context.Context, emit func(model.Turn)) error {
	var failures []error
	for _, root := range m.Roots() {
		projects := muxProjects(root)
		err := filepath.WalkDir(filepath.Join(root, "sessions"), func(path string, d fs.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				failures = append(failures, err)
				return nil
			}
			if !d.Type().IsRegular() || d.Name() != "chat.jsonl" {
				return nil
			}
			rel, _ := filepath.Rel(filepath.Join(root, "sessions"), path)
			parts := strings.Split(rel, string(filepath.Separator))
			session := filepath.Base(filepath.Dir(path))
			project := projects[parts[0]]
			if err := scanMuxFile(ctx, path, session, project, len(parts) > 2, emit); err != nil {
				failures = append(failures, err)
			}
			return nil
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func muxProjects(root string) map[string]string {
	projects := map[string]string{}
	var config struct {
		Projects [][]json.RawMessage `json:"projects"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil || json.Unmarshal(raw, &config) != nil {
		return projects
	}
	for _, pair := range config.Projects {
		if len(pair) != 2 {
			continue
		}
		var path string
		var details struct {
			Workspaces []struct {
				ID string `json:"id"`
			} `json:"workspaces"`
		}
		if json.Unmarshal(pair[0], &path) != nil || json.Unmarshal(pair[1], &details) != nil {
			continue
		}
		for _, w := range details.Workspaces {
			projects[w.ID] = path
		}
	}
	return projects
}

func scanMuxFile(ctx context.Context, path, session, project string, subagent bool, emit func(model.Turn)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var invalid bool
	for line := range jsonLines(f) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var msg muxMessage
		if json.Unmarshal(line, &msg) != nil || msg.Role != "assistant" || msg.ID == "" {
			continue
		}
		ts := parseTime(msg.Created)
		if msg.Metadata.Timestamp > 0 {
			ts = time.UnixMilli(msg.Metadata.Timestamp)
		}
		emitUsage := func(key, rawModel, route string, stamp time.Time, u *muxUsage, pm muxProviderMetadata) {
			if u == nil {
				return
			}
			read, write, reasoning := u.Cached, pm.Anthropic.Write, u.Reasoning
			if u.InputDetails != nil {
				read, write = u.InputDetails.Read, u.InputDetails.Write
			}
			if u.OutputDetails != nil {
				reasoning = u.OutputDetails.Reasoning
			}
			_, bounded := (model.Usage{Input: u.Input, Output: u.Output, CacheRead: read, CacheWrite: write, Reasoning: reasoning}).SanitizeAggregate()
			if !bounded || u.Input < 0 || read < 0 || write < 0 || reasoning < 0 || read+write > u.Input || u.Output < reasoning {
				invalid = true
				return
			}
			usage, ok := (model.Usage{Input: u.Input - read - write, Output: u.Output, CacheRead: read, CacheWrite: write, Reasoning: reasoning}).SanitizeAggregate()
			if !ok || usage.IsZero() {
				return
			}
			provider, id, hasProvider := strings.Cut(rawModel, ":")
			if !hasProvider {
				id = rawModel
				provider = ""
			}
			if route != "" {
				provider = route
			}
			if id == "" {
				id = "unknown"
			}
			emit(model.Turn{Key: "mux|" + session + "|" + key, SessionID: session, Agent: model.Agent("mux"), Timestamp: stamp, Model: id, Provider: provider, Project: project, Usage: usage, Subagent: subagent, Aggregate: true})
		}
		emitUsage(msg.ID, msg.Metadata.Model, msg.Metadata.Route, ts, msg.Metadata.Usage, msg.Metadata.Provider)
		for i, tool := range msg.Metadata.Tools {
			key := tool.ToolCallID
			if key == "" {
				key = fmt.Sprint(i)
			}
			stamp := ts
			if tool.Timestamp > 0 {
				stamp = time.UnixMilli(tool.Timestamp)
			}
			emitUsage(msg.ID+"|tool|"+key, tool.Model, "", stamp, tool.Usage, tool.Metadata)
		}
	}
	if invalid {
		return fmt.Errorf("mux: skipped legacy or inconsistent token buckets in %s; SDK 6+ accounting required", path)
	}
	return nil
}
