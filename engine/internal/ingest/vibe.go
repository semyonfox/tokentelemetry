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

// Vibe reads Mistral Vibe's session-level statistics. The producer does not
// persist per-request usage in messages.jsonl, so every emitted turn remains
// an explicit aggregate.
type Vibe struct{ root string }

func NewVibe() *Vibe {
	if home := os.Getenv("VIBE_HOME"); home != "" {
		if strings.HasPrefix(home, "~/") {
			home = filepath.Join(homeDir(), strings.TrimPrefix(home, "~/"))
		}
		return &Vibe{root: existingDir(filepath.Join(home, "logs", "session"))}
	}
	home := homeDir()
	if home == "" {
		return &Vibe{}
	}
	return &Vibe{root: existingDir(filepath.Join(home, ".vibe", "logs", "session"))}
}

func newVibeAt(root string) *Vibe  { return &Vibe{root: root} }
func (v *Vibe) Agent() model.Agent { return model.Agent("mistral-vibe") }
func (v *Vibe) Roots() []string {
	if v.root == "" {
		return nil
	}
	return []string{v.root}
}

type vibeMetadata struct {
	SessionID   string `json:"session_id"`
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
	Environment struct {
		WorkingDirectory string `json:"working_directory"`
	} `json:"environment"`
	Stats struct {
		Prompt     int64  `json:"session_prompt_tokens"`
		Completion int64  `json:"session_completion_tokens"`
		Cached     *int64 `json:"session_cached_tokens"`
	} `json:"stats"`
	Config struct {
		ActiveModel string `json:"active_model"`
		Models      []struct {
			Name  string `json:"name"`
			Alias string `json:"alias"`
		} `json:"models"`
	} `json:"config"`
}

func (v *Vibe) Scan(ctx context.Context, emit func(model.Turn)) error {
	if v.root == "" {
		return nil
	}
	var scanErr error
	err := filepath.WalkDir(v.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			scanErr = errors.Join(scanErr, walkErr)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "meta.json" {
			return nil
		}
		turn, ok, err := v.scanMetadata(path)
		if err != nil {
			scanErr = errors.Join(scanErr, err)
			return nil
		}
		if ok {
			emit(turn)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return scanErr
}

func (v *Vibe) scanMetadata(path string) (model.Turn, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return model.Turn{}, false, fmt.Errorf("mistral-vibe: read %s: %w", path, err)
	}
	var metadata vibeMetadata
	if err := json.Unmarshal(b, &metadata); err != nil {
		return model.Turn{}, false, fmt.Errorf("mistral-vibe: parse %s: %w", path, err)
	}
	prompt, completion := metadata.Stats.Prompt, metadata.Stats.Completion
	if prompt < 0 {
		prompt = 0
	}
	if completion < 0 {
		completion = 0
	}
	usage := model.Usage{Output: completion}
	unpriced := ""
	if metadata.Stats.Cached == nil {
		usage.Unclassified = prompt
		unpriced = "Vibe session metadata predates the cached-token split"
	} else if *metadata.Stats.Cached < 0 || *metadata.Stats.Cached > prompt {
		usage.Unclassified = prompt
		unpriced = "Vibe session metadata has an invalid cached-token split"
	} else {
		usage.Input = prompt - *metadata.Stats.Cached
		usage.CacheRead = *metadata.Stats.Cached
	}
	usage, ok := usage.SanitizeAggregate()
	if !ok {
		return model.Turn{}, false, fmt.Errorf("mistral-vibe: %s has implausible aggregate usage", path)
	}
	if usage.IsZero() {
		return model.Turn{}, false, nil
	}
	timestamp := parseTime(metadata.EndTime)
	if timestamp.IsZero() {
		timestamp = parseTime(metadata.StartTime)
	}
	if timestamp.IsZero() {
		return model.Turn{}, false, fmt.Errorf("mistral-vibe: %s has aggregate usage without a session timestamp", path)
	}
	modelID := vibeModel(metadata)
	if unpriced != "" {
		unpriced += "; "
	}
	unpriced += "Vibe stores one final model beside a session aggregate, so model-switch attribution is unavailable"
	if modelID == "" {
		modelID = "unknown"
		if unpriced != "" {
			unpriced += "; "
		}
		unpriced += "the session model is absent"
	}
	dir := filepath.Dir(path)
	relative, _ := filepath.Rel(v.root, dir)
	sessionID := metadata.SessionID
	key := ""
	if sessionID == "" {
		sessionID = filepath.Base(dir)
		key = identityKey("mistral-vibe-path", relative)
	} else {
		key = identityKey("mistral-vibe", sessionID)
	}
	return model.Turn{
		Key: key, SessionID: sessionID,
		Agent: model.Agent("mistral-vibe"), Timestamp: timestamp, Model: modelID,
		Provider: "mistral-vibe", Project: metadata.Environment.WorkingDirectory,
		Usage: usage, Subagent: strings.Contains(filepath.ToSlash(relative), "/agents/"),
		Aggregate: true, UnpricedReason: unpriced,
	}, true, nil
}

func vibeModel(metadata vibeMetadata) string {
	active := strings.TrimSpace(metadata.Config.ActiveModel)
	if active == "" {
		return ""
	}
	for _, configured := range metadata.Config.Models {
		if configured.Alias == active && strings.TrimSpace(configured.Name) != "" {
			return strings.TrimSpace(configured.Name)
		}
		if configured.Name == active {
			return active
		}
	}
	return active
}
