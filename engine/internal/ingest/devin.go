package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

type Devin struct{ root string }

func NewDevin() *Devin              { return &Devin{root: existingDir(defaultDevinRoot())} }
func newDevinAt(root string) *Devin { return &Devin{root: root} }
func (d *Devin) Agent() model.Agent { return model.Agent("devin") }
func (d *Devin) Roots() []string {
	if d.root == "" {
		return nil
	}
	return []string{d.root}
}
func defaultDevinRoot() string {
	h := homeDir()
	if h == "" {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(h, "Library", "Application Support", "devin", "cli")
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(h, "AppData", "Roaming")
		}
		return filepath.Join(base, "devin", "cli")
	default:
		return filepath.Join(h, ".local", "share", "devin", "cli")
	}
}

type devinTranscript struct {
	SchemaVersion string `json:"schema_version"`
	SessionID     string `json:"session_id"`
	Agent         struct {
		ModelName string `json:"model_name"`
	} `json:"agent"`
	Steps []devinStep `json:"steps"`
}
type devinMetrics struct {
	Prompt     *int64 `json:"prompt_tokens"`
	Completion *int64 `json:"completion_tokens"`
	Cached     *int64 `json:"cached_tokens"`
	Extra      struct {
		CacheCreation *int64 `json:"cache_creation_input_tokens"`
	} `json:"extra"`
}
type devinStep struct {
	StepID    int64         `json:"step_id"`
	Timestamp string        `json:"timestamp"`
	Source    string        `json:"source"`
	ModelName string        `json:"model_name"`
	Metrics   *devinMetrics `json:"metrics"`
	Extra     struct {
		GenerationModel string `json:"generation_model"`
	} `json:"extra"`
	Metadata struct {
		CreatedAt       string `json:"created_at"`
		GenerationModel string `json:"generation_model"`
		Metrics         *struct {
			Input         *int64 `json:"input_tokens"`
			Output        *int64 `json:"output_tokens"`
			CacheCreation *int64 `json:"cache_creation_tokens"`
			CacheRead     *int64 `json:"cache_read_tokens"`
		} `json:"metrics"`
	} `json:"metadata"`
}
type devinSession struct {
	project, model string
	hidden         bool
}

func (d *Devin) Scan(ctx context.Context, emit func(model.Turn)) error {
	if d.root == "" {
		return nil
	}
	sessions := loadDevinSessions(ctx, filepath.Join(d.root, "sessions.db"))
	entries, err := os.ReadDir(filepath.Join(d.root, "transcripts"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var paths []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			paths = append(paths, filepath.Join(d.root, "transcripts", e.Name()))
		}
	}
	for _, turn := range mapFiles(ctx, paths, func(path string) []model.Turn { return scanDevinTranscript(path, sessions) }) {
		emit(turn)
	}
	return nil
}

func scanDevinTranscript(path string, sessions map[string]devinSession) []model.Turn {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var tr devinTranscript
	if json.Unmarshal(raw, &tr) != nil {
		return nil
	}
	sessionID := strings.TrimSpace(tr.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	meta := sessions[sessionID]
	if meta.hidden {
		return nil
	}
	var turns []model.Turn
	for _, step := range tr.Steps {
		usage, ok := devinStepUsage(step)
		if !ok {
			continue
		}
		modelID := firstNonEmpty(step.Metadata.GenerationModel, step.Extra.GenerationModel, step.ModelName, tr.Agent.ModelName)
		unpriced := ""
		if modelID == "" && meta.model != "" {
			modelID = meta.model
			unpriced = "Devin session metadata does not preserve per-step model attribution"
		} else if modelID == "" {
			modelID = "unknown"
			unpriced = "Devin ATIF step does not record the request model"
		}
		stamp := parseTime(step.Timestamp)
		if stamp.IsZero() {
			stamp = parseTime(step.Metadata.CreatedAt)
		}
		turns = append(turns, model.Turn{Key: identityKey("devin-step", sessionID, strconv.FormatInt(step.StepID, 10)), SessionID: sessionID, Agent: model.Agent("devin"), Timestamp: stamp, Model: modelID, Project: meta.project, Usage: usage, UnpricedReason: unpriced})
	}
	return turns
}

func devinStepUsage(step devinStep) (model.Usage, bool) {
	var gross, out, cacheRead, cacheWrite int64
	if step.Metrics != nil && devinHasMetrics(step.Metrics) {
		gross = pointerCount(step.Metrics.Prompt)
		out = pointerCount(step.Metrics.Completion)
		cacheRead = pointerCount(step.Metrics.Cached)
		cacheWrite = pointerCount(step.Metrics.Extra.CacheCreation)
	} else if step.Metadata.Metrics != nil {
		gross = pointerCount(step.Metadata.Metrics.Input)
		out = pointerCount(step.Metadata.Metrics.Output)
		cacheRead = pointerCount(step.Metadata.Metrics.CacheRead)
		cacheWrite = pointerCount(step.Metadata.Metrics.CacheCreation)
	} else {
		return model.Usage{}, false
	}
	counts, ok := sanitizeCallCounts(gross, out, cacheRead, cacheWrite)
	if !ok {
		return model.Usage{}, false
	}
	gross, out, cacheRead, cacheWrite = counts[0], counts[1], counts[2], counts[3]
	net := gross - cacheRead
	if net < 0 {
		net = 0
	}
	usage := model.Usage{Input: net, Output: out, CacheRead: cacheRead, CacheWrite: cacheWrite, ContextTokens: gross + cacheWrite}
	usage, ok = usage.Sanitize()
	return usage, ok && !usage.IsZero()
}
func devinHasMetrics(m *devinMetrics) bool {
	return m.Prompt != nil || m.Completion != nil || m.Cached != nil || m.Extra.CacheCreation != nil
}
func pointerCount(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func loadDevinSessions(ctx context.Context, path string) map[string]devinSession {
	out := map[string]devinSession{}
	if !fileExists(path) {
		return out
	}
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&_pragma=busy_timeout(3000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return out
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id,COALESCE(working_directory,''),COALESCE(model,''),COALESCE(hidden,0) FROM sessions`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, project, modelID string
		var hidden int
		if rows.Scan(&id, &project, &modelID, &hidden) != nil {
			continue
		}
		out[id] = devinSession{project: project, model: modelID, hidden: hidden != 0}
	}
	return out
}
