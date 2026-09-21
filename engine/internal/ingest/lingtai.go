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

type LingTai struct{ roots []string }

func NewLingTai() *LingTai                  { return &LingTai{roots: lingTaiRoots()} }
func newLingTaiAt(roots ...string) *LingTai { return &LingTai{roots: roots} }
func (l *LingTai) Agent() model.Agent       { return model.Agent("lingtai-tui") }
func (l *LingTai) Roots() []string          { return append([]string(nil), l.roots...) }

func lingTaiRoots() []string {
	var candidates []string
	explicit := firstNonEmpty(os.Getenv("LINGTAI_HOME"), os.Getenv("LINGTAI_TUI_HOME"))
	if explicit != "" {
		candidates = strings.Split(explicit, string(os.PathListSeparator))
	} else if h := homeDir(); h != "" {
		candidates = append(candidates, filepath.Join(h, ".lingtai"))
		candidates = append(candidates, lingTaiRegisteredHomes(filepath.Join(h, ".lingtai-tui", "registry.jsonl"))...)
	}
	if cwd, err := os.Getwd(); err == nil && explicit == "" {
		for cur := cwd; ; cur = filepath.Dir(cur) {
			candidates = append(candidates, filepath.Join(cur, ".lingtai"))
			next := filepath.Dir(cur)
			if next == cur {
				break
			}
		}
	}
	seen := map[string]bool{}
	var roots []string
	for _, p := range candidates {
		p = existingDir(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		roots = append(roots, p)
	}
	return roots
}
func lingTaiRegisteredHomes(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	for line := range jsonLines(f) {
		var row struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(line, &row) == nil && row.Path != "" {
			out = append(out, filepath.Join(row.Path, ".lingtai"))
		}
	}
	return out
}

type lingTaiManifest struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	Address   string `json:"address"`
	Nickname  string `json:"nickname"`
}
type lingTaiEntry struct {
	APICallID string          `json:"api_call_id"`
	Source    string          `json:"source"`
	EmID      string          `json:"em_id"`
	RunID     string          `json:"run_id"`
	Model     string          `json:"model"`
	Endpoint  string          `json:"endpoint"`
	TS        json.RawMessage `json:"ts"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	Thinking  json.RawMessage `json:"thinking"`
	Cached    json.RawMessage `json:"cached"`
}
type lingTaiSource struct{ path, project, agentID string }

func (l *LingTai) Scan(ctx context.Context, emit func(model.Turn)) error {
	var sources []lingTaiSource
	var scanErr error
	for _, root := range l.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			scanErr = errors.Join(scanErr, err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			agentDir := filepath.Join(root, entry.Name())
			ledger := filepath.Join(agentDir, "logs", "token_ledger.jsonl")
			if !fileExists(ledger) {
				continue
			}
			manifest := readLingTaiManifest(agentDir)
			project := firstNonEmpty(manifest.Nickname, manifest.AgentName, manifest.Address, entry.Name())
			agentID := firstNonEmpty(manifest.AgentID, entry.Name())
			sources = append(sources, lingTaiSource{ledger, project, agentID})
		}
	}
	for _, source := range sources {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := scanLingTaiLedger(source, emit); err != nil {
			scanErr = errors.Join(scanErr, err)
		}
	}
	return scanErr
}
func readLingTaiManifest(dir string) lingTaiManifest {
	raw, err := os.ReadFile(filepath.Join(dir, ".agent.json"))
	if err != nil {
		return lingTaiManifest{}
	}
	var m lingTaiManifest
	_ = json.Unmarshal(raw, &m)
	return m
}

func scanLingTaiLedger(source lingTaiSource, emit func(model.Turn)) error {
	f, err := os.Open(source.path)
	if err != nil {
		return err
	}
	defer f.Close()
	lineNo := 0
	for line := range jsonLines(f) {
		lineNo++
		var r lingTaiEntry
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		gross := lingTaiCount(r.Input)
		out := lingTaiCount(r.Output)
		thinking := lingTaiCount(r.Thinking)
		cached := lingTaiCount(r.Cached)
		counts, ok := sanitizeCallCounts(gross, out, thinking, cached)
		if !ok {
			continue
		}
		gross, out, thinking, cached = counts[0], counts[1], counts[2], counts[3]
		net := gross - cached
		if net < 0 {
			net = 0
		}
		usage := model.Usage{Input: net, Output: out + thinking, CacheRead: cached, Reasoning: thinking, ContextTokens: gross}
		usage, ok = usage.Sanitize()
		if !ok || usage.IsZero() {
			continue
		}
		modelID := strings.TrimSpace(r.Model)
		unpriced := ""
		if modelID == "" {
			modelID = "unknown"
			unpriced = "LingTai ledger entry does not record the request model"
		}
		sourceLabel := firstNonEmpty(r.Source, "main")
		session := firstNonEmpty(r.RunID, source.agentID+":"+sourceLabel)
		key := ""
		if r.APICallID != "" {
			key = identityKey("lingtai-api-call", r.APICallID)
		} else {
			key = identityKey("lingtai-ledger", filepath.Clean(source.path), strconv.Itoa(lineNo))
		}
		emit(model.Turn{Key: key, SessionID: session, Agent: model.Agent("lingtai-tui"), Timestamp: parseFlexibleTime(r.TS), Model: modelID, Endpoint: r.Endpoint, Project: source.project, Usage: usage, Subagent: r.Source == "daemon" || r.EmID != "" || r.RunID != "", UnpricedReason: unpriced})
	}
	return nil
}
func lingTaiCount(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		n, _ = strconv.ParseInt(text, 10, 64)
		return n
	}
	return 0
}
