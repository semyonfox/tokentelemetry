package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Kimi reads the legacy Kimi CLI wire protocol under ~/.kimi.
type Kimi struct{ root string }

func NewKimi() *Kimi {
	return &Kimi{root: envDir("KIMI_SHARE_DIR", ".kimi")}
}

func newKimiAt(root string) *Kimi  { return &Kimi{root: root} }
func (k *Kimi) Agent() model.Agent { return model.Agent("kimi") }
func (k *Kimi) Roots() []string {
	if k.root == "" {
		return nil
	}
	sessions := existingDir(filepath.Join(k.root, "sessions"))
	if sessions == "" {
		return nil
	}
	return []string{sessions}
}

type kimiWireSource struct {
	path, session, source, project string
	subagent                       bool
}

func (k *Kimi) Scan(ctx context.Context, emit func(model.Turn)) error {
	if len(k.Roots()) == 0 {
		return nil
	}
	var scanErr error
	err := filepath.WalkDir(filepath.Join(k.root, "sessions"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			scanErr = errors.Join(scanErr, walkErr)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "wire.jsonl" {
			return nil
		}
		source := kimiSource(path)
		if err := scanKimiWire(source, emit); err != nil {
			scanErr = errors.Join(scanErr, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return scanErr
}

func kimiSource(path string) kimiWireSource {
	dir := filepath.Dir(path)
	if filepath.Base(filepath.Dir(dir)) == "subagents" {
		sessionDir := filepath.Dir(filepath.Dir(dir))
		return kimiWireSource{path: path, session: filepath.Base(sessionDir), source: filepath.Base(dir), project: filepath.Base(filepath.Dir(sessionDir)), subagent: true}
	}
	return kimiWireSource{path: path, session: filepath.Base(dir), source: "main", project: filepath.Base(filepath.Dir(dir))}
}

type kimiEnvelope struct {
	Type    string `json:"type"`
	Payload struct {
		MessageID string `json:"message_id"`
		Model     string `json:"model"`
		ModelName string `json:"model_name"`
		Usage     *struct {
			InputOther       int64 `json:"input_other"`
			Output           int64 `json:"output"`
			InputCacheRead   int64 `json:"input_cache_read"`
			InputCacheCreate int64 `json:"input_cache_creation"`
		} `json:"token_usage"`
	} `json:"payload"`
}

func scanKimiWire(source kimiWireSource, emit func(model.Turn)) error {
	f, err := os.Open(source.path)
	if err != nil {
		return fmt.Errorf("kimi: open %s: %w", source.path, err)
	}
	defer f.Close()
	lineNumber := 0
	var scanErr error
	for line := range jsonLines(f) {
		lineNumber++
		var record struct {
			Timestamp json.RawMessage `json:"timestamp"`
			Message   json.RawMessage `json:"message"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		var envelope kimiEnvelope
		if len(record.Message) > 0 && string(record.Message) != "null" {
			if json.Unmarshal(record.Message, &envelope) != nil {
				continue
			}
		} else {
			envelope.Type = record.Type
			if json.Unmarshal(record.Payload, &envelope.Payload) != nil {
				continue
			}
		}
		if envelope.Type != "StatusUpdate" || envelope.Payload.Usage == nil {
			continue
		}
		raw := envelope.Payload.Usage
		usage := model.Usage{Input: raw.InputOther, Output: raw.Output, CacheRead: raw.InputCacheRead, CacheWrite: raw.InputCacheCreate}
		usage, ok := usage.Sanitize()
		if !ok || usage.IsZero() {
			continue
		}
		timestamp := kimiWireTime(record.Timestamp)
		if timestamp.IsZero() {
			scanErr = errors.Join(scanErr, fmt.Errorf("kimi: %s line %d has measured usage without a timestamp", source.path, lineNumber))
			continue
		}
		modelID := strings.TrimSpace(envelope.Payload.Model)
		if modelID == "" {
			modelID = strings.TrimSpace(envelope.Payload.ModelName)
		}
		unpriced := ""
		if modelID == "" {
			modelID = "unknown"
			unpriced = "Kimi wire usage does not record the request model"
		}
		identity := envelope.Payload.MessageID
		if identity == "" {
			identity = strconv.Itoa(lineNumber)
		}
		emit(model.Turn{
			Key: identityKey("kimi", source.session, source.source, identity), SessionID: source.session,
			Agent: model.Agent("kimi"), Timestamp: timestamp, Model: modelID, Provider: "kimi",
			Project: source.project, Usage: usage, Subagent: source.subagent, UnpricedReason: unpriced,
		})
	}
	return scanErr
}

func kimiWireTime(raw json.RawMessage) time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return parseTime(text)
	}
	var number json.Number
	if json.Unmarshal(raw, &number) != nil {
		return time.Time{}
	}
	value, err := strconv.ParseFloat(number.String(), 64)
	if err != nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return time.Time{}
	}
	if value < 1_000_000_000_000 {
		seconds, fraction := math.Modf(value)
		return time.Unix(int64(seconds), int64(fraction*float64(time.Second))).UTC()
	}
	seconds, fraction := math.Modf(value / 1000)
	return time.Unix(int64(seconds), int64(fraction*float64(time.Second))).UTC()
}
