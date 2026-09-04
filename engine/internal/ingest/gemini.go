package ingest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
)

// Gemini scans Gemini CLI chat sessions.
//
// Layout is ~/.gemini/tmp/<project-hash>/chats/session-*.json — a single JSON
// document per session with a messages array. Assistant turns have type
// "gemini" and carry their own token block and model, so turns keep per-call
// timestamps and per-call model attribution.
//
// The project is only recorded as an opaque hash of its path, so sessions
// group correctly but cannot be labelled with a real directory.
type Gemini struct {
	root string
}

func NewGemini() *Gemini {
	return &Gemini{root: envDir("GEMINI_CONFIG_DIR", ".gemini")}
}

// knownSlugs returns the set of project hashes that ~/.gemini/projects.json
// maps a real directory to.
//
// A session's own kind=="main" field alone does NOT mean Gemini CLI:
// Antigravity writes main-kind chats into the same tmp/ tree, under hashes
// projects.json has never heard of. The Python scanner uses exactly this
// list to decide, and the two runtimes MUST agree — sessions are merged on
// (agent, id), so one side labelling a session gemini and the other
// antigravity does not conflict, it produces two rows and counts the usage
// twice.
//
// A missing or unreadable projects.json yields an empty set, which
// classifies everything as Antigravity. That is the safe direction:
// Antigravity usage is scanned by Python either way, so the cost of being
// wrong is a session Go skips, not one it double-counts.
func (g *Gemini) knownSlugs() map[string]struct{} {
	slugs := map[string]struct{}{}
	if g.root == "" {
		return slugs
	}
	raw, err := os.ReadFile(filepath.Join(g.root, "projects.json"))
	if err != nil {
		return slugs
	}
	var doc struct {
		Projects map[string]string `json:"projects"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return slugs
	}
	for _, slug := range doc.Projects {
		slugs[slug] = struct{}{}
	}
	return slugs
}

func (g *Gemini) Agent() model.Agent { return model.AgentGemini }

func (g *Gemini) Roots() []string {
	if g.root == "" {
		return nil
	}
	if d := existingDir(filepath.Join(g.root, "tmp")); d != "" {
		return []string{d}
	}
	return nil
}

type geminiSession struct {
	SessionID   string `json:"sessionId"`
	ProjectHash string `json:"projectHash"`
	// Gemini CLI marks its own chats as kind="main". The same directory also
	// holds Antigravity state, which has a different agent and must not be
	// silently relabelled as Gemini.
	Kind     string `json:"kind"`
	Messages []struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Model     string `json:"model"`
		Tokens    *struct {
			Input    int64 `json:"input"`
			Output   int64 `json:"output"`
			Cached   int64 `json:"cached"`
			Thoughts int64 `json:"thoughts"`
			Tool     int64 `json:"tool"`
			Total    int64 `json:"total"`
		} `json:"tokens"`
	} `json:"messages"`
}

func (g *Gemini) Scan(ctx context.Context, emit func(model.Turn)) error {
	roots := g.Roots()
	if len(roots) == 0 {
		return nil
	}
	projects, err := os.ReadDir(roots[0])
	if err != nil {
		return err
	}
	known := g.knownSlugs()
	var paths []string
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		// Hashes projects.json doesn't know are Antigravity's, not Gemini
		// CLI's — see knownSlugs.
		if _, ok := known[p.Name()]; !ok {
			continue
		}
		paths = append(paths, globJSONL(filepath.Join(roots[0], p.Name(), "chats", "session-*.json"))...)
	}
	for _, t := range mapFiles(ctx, paths, g.scanFile) {
		emit(t)
	}
	return nil
}

func (g *Gemini) scanFile(path string) []model.Turn {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s geminiSession
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil
	}
	if s.Kind != "main" {
		return nil
	}
	var turns []model.Turn
	for i, m := range s.Messages {
		if m.Type != "gemini" || m.Tokens == nil || m.Model == "" {
			continue
		}
		// Gemini's input count is the whole effective prompt, including cached
		// content. Keep the buckets disjoint by subtracting the cached part.
		// Thinking tokens are separate from the visible response count, but
		// Google bills both at the output rate.
		netInput := m.Tokens.Input - m.Tokens.Cached
		if netInput < 0 {
			netInput = 0
		}
		usage := model.Usage{
			Input:         netInput,
			Output:        m.Tokens.Output + m.Tokens.Thoughts,
			CacheRead:     m.Tokens.Cached,
			Reasoning:     m.Tokens.Thoughts,
			ContextTokens: m.Tokens.Input,
		}
		usage, ok := usage.Sanitize()
		if !ok || usage.IsZero() {
			continue
		}
		key := ""
		if s.SessionID != "" {
			key = "gemini|" + s.SessionID + "|" + strconv.Itoa(i)
		}
		turns = append(turns, model.Turn{
			Key:       key,
			SessionID: s.SessionID,
			Agent:     model.AgentGemini,
			Timestamp: parseTime(m.Timestamp),
			Model:     m.Model,
			Provider:  "google",
			Usage:     usage,
		})
	}
	return turns
}
