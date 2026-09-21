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

type IBMBob struct{ roots []string }

func NewIBMBob() *IBMBob                  { return &IBMBob{roots: existingTaskRoots(ibmBobRoots())} }
func newIBMBobAt(roots ...string) *IBMBob { return &IBMBob{roots: roots} }
func (b *IBMBob) Agent() model.Agent      { return model.Agent("ibm-bob") }
func (b *IBMBob) Roots() []string         { return append([]string(nil), b.roots...) }

func ibmBobRoots() []string {
	h := homeDir()
	if h == "" {
		return nil
	}
	var base string
	if runtime.GOOS == "darwin" {
		base = filepath.Join(h, "Library", "Application Support")
	} else if runtime.GOOS == "windows" {
		base = os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(h, "AppData", "Roaming")
		}
	} else {
		base = os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(h, ".config")
		}
	}
	return []string{filepath.Join(base, "IBM Bob", "User", "globalStorage", "ibm.bob-code"), filepath.Join(base, "Bob-IDE", "User", "globalStorage", "ibm.bob-code")}
}

func (b *IBMBob) Scan(ctx context.Context, emit func(model.Turn)) error {
	var firstErr error
	for _, root := range b.roots {
		entries, err := os.ReadDir(filepath.Join(root, "tasks"))
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		var paths []string
		for _, e := range entries {
			if e.IsDir() {
				paths = append(paths, filepath.Join(root, "tasks", e.Name()))
			}
		}
		for _, turn := range mapFiles(ctx, paths, scanIBMBobTask) {
			emit(turn)
		}
	}
	return firstErr
}

func scanIBMBobTask(taskDir string) []model.Turn {
	raw, err := os.ReadFile(filepath.Join(taskDir, "ui_messages.json"))
	if err != nil {
		return nil
	}
	var messages []legacyClineMessage
	if json.Unmarshal(raw, &messages) != nil {
		return nil
	}
	modelID, project := legacyClineHistoryMeta(taskDir)
	if modelID == "" {
		modelID = "unknown"
	}
	taskID := filepath.Base(taskDir)
	index := 0
	var turns []model.Turn
	for _, message := range messages {
		if message.Type != "say" || message.Say != "api_req_started" {
			continue
		}
		n := index
		index++
		var metrics legacyClineMetrics
		if message.Text == "" || json.Unmarshal([]byte(message.Text), &metrics) != nil {
			continue
		}
		// Bob persists the classic Cline disjoint counters, but only the task's
		// final configured model survives. Preserve the measured buckets while
		// keeping the aggregate model attribution explicitly unpriced.
		u := model.Usage{Input: metrics.TokensIn, Output: metrics.TokensOut, CacheRead: metrics.CacheReads, CacheWrite: metrics.CacheWrites, ContextTokens: metrics.TokensIn + metrics.CacheReads + metrics.CacheWrites}
		u, ok := u.Sanitize()
		if !ok || u.IsZero() {
			continue
		}
		turns = append(turns, model.Turn{Key: identityKey("ibm-bob-task", taskID, strconv.Itoa(n)), SessionID: taskID, Agent: model.Agent("ibm-bob"), Timestamp: parseLegacyClineTime(message.TS), Model: modelID, Project: project, Usage: u, UnpricedReason: "IBM Bob task history does not preserve per-call model attribution"})
	}
	return turns
}
