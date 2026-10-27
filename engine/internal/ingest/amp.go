package ingest

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// Amp scans Amp thread files.
//
// Layout is ~/.local/share/amp/threads/**/*.json, one JSON object per thread.
// AMP_DATA_DIR replaces the default with one or more comma-separated roots.
//
// Assistant messages carry Anthropic-shaped usage: inputTokens is NET of
// cache, and totalInputTokens is input plus cache writes plus cache reads.
// That is the measured per-call source. Threads can also hold a usage ledger
// whose events point at a message through toMessageId. A ledger event that
// matches a message adds only its credits, because the message already counts
// the tokens. Unmatched events are separate calls (other models, tools) and
// count on their own, without a cache split the ledger does not record.
const ampAgent = model.Agent("amp")

type Amp struct {
	roots []string
}

func NewAmp() *Amp {
	var candidates []string
	if value := strings.TrimSpace(os.Getenv("AMP_DATA_DIR")); value != "" {
		for _, part := range strings.Split(value, ",") {
			candidates = append(candidates, strings.TrimSpace(part))
		}
	} else {
		data := os.Getenv("XDG_DATA_HOME")
		if !filepath.IsAbs(data) {
			if home := homeDir(); home != "" {
				data = filepath.Join(home, ".local", "share")
			}
		}
		if data != "" {
			candidates = append(candidates, filepath.Join(data, "amp"))
		}
	}
	return newAmpAt(candidates...)
}

func newAmpAt(dirs ...string) *Amp {
	a := &Amp{}
	seen := make(map[string]bool)
	for _, dir := range dirs {
		threads := existingDir(filepath.Join(dir, "threads"))
		if threads == "" || seen[threads] {
			continue
		}
		seen[threads] = true
		a.roots = append(a.roots, threads)
	}
	return a
}

func (a *Amp) Agent() model.Agent { return ampAgent }

func (a *Amp) Roots() []string { return a.roots }

type ampThread struct {
	ID       string            `json:"id"`
	Messages []json.RawMessage `json:"messages"`
	Ledger   *struct {
		Events []json.RawMessage `json:"events"`
	} `json:"usageLedger"`
}

type ampMessage struct {
	Role      string          `json:"role"`
	MessageID json.RawMessage `json:"messageId"`
	Timestamp json.RawMessage `json:"timestamp"`
	Model     string          `json:"model"`
	Usage     *struct {
		Model       string          `json:"model"`
		Input       int64           `json:"inputTokens"`
		Output      int64           `json:"outputTokens"`
		CacheWrite  int64           `json:"cacheCreationInputTokens"`
		CacheRead   int64           `json:"cacheReadInputTokens"`
		TotalTokens int64           `json:"totalTokens"`
		Timestamp   json.RawMessage `json:"timestamp"`
		Credits     *float64        `json:"credits"`
	} `json:"usage"`
}

type ampLedgerEvent struct {
	ID          json.RawMessage `json:"id"`
	Timestamp   json.RawMessage `json:"timestamp"`
	Model       string          `json:"model"`
	ToMessageID json.RawMessage `json:"toMessageId"`
	Credits     *float64        `json:"credits"`
	Tokens      *struct {
		Input  int64 `json:"input"`
		Output int64 `json:"output"`
		Total  int64 `json:"total"`
	} `json:"tokens"`
}

func (a *Amp) Scan(ctx context.Context, emit func(model.Turn)) error {
	var paths []string
	var scanErr error
	for _, root := range a.roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				scanErr = errors.Join(scanErr, err)
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
				paths = append(paths, path)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	for _, turn := range mapFiles(ctx, paths, func(path string) []model.Turn {
		return cachedParsedFile(ctx, path, "amp-thread-v1", func() []model.Turn { return scanAmpThread(path) })
	}) {
		emit(turn)
	}
	return scanErr
}

func scanAmpThread(path string) []model.Turn {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var thread ampThread
	if json.Unmarshal(raw, &thread) != nil {
		return nil
	}
	threadID := cmp.Or(thread.ID, strings.TrimSuffix(filepath.Base(path), ".json"))

	var turns []model.Turn
	byMessage := make(map[string]int)
	for i, rawMessage := range thread.Messages {
		var m ampMessage
		if json.Unmarshal(rawMessage, &m) != nil || m.Role != "assistant" || m.Usage == nil {
			continue
		}
		u := m.Usage
		usage := model.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
		if usage.IsZero() {
			// a bare total cannot be split, so it is kept but left unpriced
			usage.Unclassified = u.TotalTokens
		} else {
			usage.ContextTokens = usage.Input + usage.CacheRead + usage.CacheWrite
		}
		usage, ok := usage.Sanitize()
		if !ok || usage.IsZero() {
			continue
		}
		messageID := ampID(m.MessageID)
		key := identityKey("amp-message", threadID, cmp.Or(messageID, "index:"+strconv.Itoa(i)))
		turn := model.Turn{
			Key:       key,
			SessionID: threadID,
			Agent:     ampAgent,
			Timestamp: cmp.Or(ampTime(u.Timestamp), ampTime(m.Timestamp)),
			Model:     cmp.Or(u.Model, m.Model, "unknown"),
			Usage:     usage,
			Credits:   u.Credits,
		}
		if turn.Model == "unknown" {
			turn.UnpricedReason = "Amp message does not record the request model"
		}
		if messageID != "" {
			byMessage[messageID] = len(turns)
		}
		turns = append(turns, turn)
	}

	if thread.Ledger == nil {
		return turns
	}
	for i, rawEvent := range thread.Ledger.Events {
		var e ampLedgerEvent
		if json.Unmarshal(rawEvent, &e) != nil {
			continue
		}
		if at, ok := byMessage[ampID(e.ToMessageID)]; ok {
			if turns[at].Credits == nil {
				turns[at].Credits = e.Credits
			}
			continue
		}
		if e.Tokens == nil {
			continue
		}
		usage := model.Usage{Input: e.Tokens.Input, Output: e.Tokens.Output}
		if usage.IsZero() {
			usage.Unclassified = e.Tokens.Total
		}
		usage, ok := usage.Sanitize()
		if !ok || usage.IsZero() {
			continue
		}
		turn := model.Turn{
			Key:       identityKey("amp-ledger", threadID, cmp.Or(ampID(e.ID), "index:"+strconv.Itoa(i))),
			SessionID: threadID,
			Agent:     ampAgent,
			Timestamp: ampTime(e.Timestamp),
			Model:     cmp.Or(e.Model, "unknown"),
			Usage:     usage,
			Credits:   e.Credits,
		}
		if turn.Model == "unknown" {
			turn.UnpricedReason = "Amp ledger event does not record the request model"
		}
		turns = append(turns, turn)
	}
	return turns
}

// ampID reads an identifier that Amp writes as either a number or a string.
func ampID(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// ampTime accepts RFC 3339 strings and Unix milliseconds.
func ampTime(raw json.RawMessage) time.Time {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return parseTime(s)
	}
	var ms int64
	if json.Unmarshal(raw, &ms) == nil && ms > 0 {
		return time.UnixMilli(ms).UTC()
	}
	return time.Time{}
}
