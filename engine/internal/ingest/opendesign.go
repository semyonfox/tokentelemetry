package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

type OpenDesign struct{ root string }

func NewOpenDesign() *OpenDesign              { return &OpenDesign{root: existingDir(defaultOpenDesignRoot())} }
func newOpenDesignAt(root string) *OpenDesign { return &OpenDesign{root: root} }
func (o *OpenDesign) Agent() model.Agent      { return model.Agent("open-design") }
func (o *OpenDesign) Roots() []string {
	if o.root == "" {
		return nil
	}
	return []string{o.root}
}
func defaultOpenDesignRoot() string {
	h := homeDir()
	if h == "" {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(h, "Library", "Application Support", "Open Design")
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(h, "AppData", "Roaming")
		}
		return filepath.Join(base, "Open Design")
	default:
		return filepath.Join(h, ".config", "Open Design")
	}
}

type openDesignUsage struct {
	Input          *int64 `json:"input_tokens"`
	Output         *int64 `json:"output_tokens"`
	CachedRead     *int64 `json:"cached_read_tokens"`
	CachedWrite    *int64 `json:"cached_write_tokens"`
	CacheCreation  *int64 `json:"cache_creation_tokens"`
	AnthropicRead  *int64 `json:"cache_read_input_tokens"`
	AnthropicWrite *int64 `json:"cache_creation_input_tokens"`
	Thought        *int64 `json:"thought_tokens"`
}
type openDesignEvent struct {
	ID        string          `json:"id"`
	Event     string          `json:"event"`
	Timestamp json.RawMessage `json:"timestamp"`
	Data      struct {
		Type, Model string
		Usage       *openDesignUsage `json:"usage"`
	} `json:"data"`
}
type openDesignSource struct{ path, project string }

func (o *OpenDesign) Scan(ctx context.Context, emit func(model.Turn)) error {
	if o.root == "" {
		return nil
	}
	sources, err := discoverOpenDesign(o.root)
	if err != nil {
		return err
	}
	paths := make([]string, len(sources))
	byPath := map[string]string{}
	for i, s := range sources {
		paths[i] = s.path
		byPath[s.path] = s.project
	}
	for _, turn := range mapFiles(ctx, paths, func(path string) []model.Turn { return scanOpenDesignFile(path, byPath[path]) }) {
		emit(turn)
	}
	return nil
}

func discoverOpenDesign(root string) ([]openDesignSource, error) {
	seen := map[string]bool{}
	var out []openDesignSource
	add := func(runs, project string) {
		entries, _ := os.ReadDir(runs)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(runs, e.Name(), "events.jsonl")
			if fileExists(p) && !seen[p] {
				seen[p] = true
				out = append(out, openDesignSource{p, project})
			}
		}
	}
	base := filepath.Base(root)
	switch base {
	case "runs":
		add(root, filepath.Base(filepath.Dir(filepath.Dir(root))))
	case "data":
		add(filepath.Join(root, "runs"), filepath.Base(filepath.Dir(root)))
	case "namespaces":
		entries, err := os.ReadDir(root)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				add(filepath.Join(root, e.Name(), "data", "runs"), e.Name())
			}
		}
	default:
		add(filepath.Join(root, "data", "runs"), base)
		add(filepath.Join(root, "runs"), base)
		namespaces := filepath.Join(root, "namespaces")
		entries, _ := os.ReadDir(namespaces)
		for _, e := range entries {
			if e.IsDir() {
				add(filepath.Join(namespaces, e.Name(), "data", "runs"), e.Name())
			}
		}
	}
	return out, nil
}

func scanOpenDesignFile(path, project string) []model.Turn {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	session := filepath.Base(filepath.Dir(path))
	currentModel := ""
	index := 0
	var turns []model.Turn
	for line := range jsonLines(f) {
		var e openDesignEvent
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if e.Event == "start" {
			currentModel = firstNonEmpty(e.Data.Model, currentModel)
			continue
		}
		if e.Event != "agent" {
			continue
		}
		if e.Data.Type == "status" {
			currentModel = firstNonEmpty(e.Data.Model, currentModel)
			continue
		}
		if e.Data.Type != "usage" || e.Data.Usage == nil {
			continue
		}
		usage, ok := normalizeOpenDesignUsage(e.Data.Usage)
		if !ok {
			continue
		}
		index++
		modelID := currentModel
		unpriced := ""
		if modelID == "" {
			modelID = "unknown"
			unpriced = "Open Design usage event has no active model"
		}
		id := e.ID
		if id == "" {
			id = "line-" + strconv.Itoa(index)
		}
		turns = append(turns, model.Turn{Key: identityKey("open-design-event", session, id), SessionID: session, Agent: model.Agent("open-design"), Timestamp: parseFlexibleTime(e.Timestamp), Model: modelID, Project: project, Usage: usage, UnpricedReason: unpriced})
	}
	return turns
}

func normalizeOpenDesignUsage(u *openDesignUsage) (model.Usage, bool) {
	input := pointerCount(u.Input)
	out := pointerCount(u.Output)
	thinking := pointerCount(u.Thought)
	cacheRead, cacheWrite, net := int64(0), int64(0), input
	if u.AnthropicRead != nil || u.AnthropicWrite != nil {
		cacheRead = pointerCount(u.AnthropicRead)
		cacheWrite = pointerCount(u.AnthropicWrite)
	} else {
		cacheRead = pointerCount(u.CachedRead)
		cacheWrite = firstNonzero(pointerCount(u.CachedWrite), pointerCount(u.CacheCreation))
	}
	counts, ok := sanitizeCallCounts(input, out, thinking, cacheRead, cacheWrite)
	if !ok {
		return model.Usage{}, false
	}
	input, out, thinking, cacheRead, cacheWrite = counts[0], counts[1], counts[2], counts[3], counts[4]
	net = input
	if u.AnthropicRead == nil && u.AnthropicWrite == nil {
		net = input - cacheRead - cacheWrite
		if net < 0 {
			net = 0
		}
	}
	usage := model.Usage{Input: net, Output: out + thinking, CacheRead: cacheRead, CacheWrite: cacheWrite, Reasoning: thinking}
	if u.AnthropicRead != nil || u.AnthropicWrite != nil {
		usage.ContextTokens = input + cacheRead + cacheWrite
	} else {
		usage.ContextTokens = input
	}
	usage, ok = usage.Sanitize()
	return usage, ok && !usage.IsZero()
}
