package ingest

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
)

// Legacy rollouts lack durable response identities. Keep their historical
// replay fallback separate from structured records and disclose its use.
const replayGap = time.Second

// Codex reads rollout history, preferring persisted token_usage_record entries
// when available. Older turns retain a separately labelled token_count fallback.
type Codex struct {
	root string
}

func NewCodex() *Codex {
	return &Codex{root: envDir("CODEX_HOME", ".codex")}
}

func (c *Codex) Agent() model.Agent { return model.AgentCodex }

func (c *Codex) Roots() []string {
	if c.root == "" {
		return nil
	}
	if d := existingDir(filepath.Join(c.root, "sessions")); d != "" {
		return []string{d}
	}
	return nil
}

type codexUsage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	Total      int64 `json:"total_tokens"`
}

type codexRecord struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type       string      `json:"type"`
		ThreadID   string      `json:"thread_id"`
		TurnID     string      `json:"turn_id"`
		ResponseID string      `json:"response_id"`
		Usage      *codexUsage `json:"usage"`

		// session_meta
		ID             string `json:"id"`
		SessionID      string `json:"session_id"`
		ForkedFromID   string `json:"forked_from_id"`
		ParentThreadID string `json:"parent_thread_id"`
		ThreadSource   string `json:"thread_source"`
		CWD            string `json:"cwd"`
		Model          string `json:"model"`
		ModelProvider  string `json:"model_provider"`

		// event_msg/token_count
		Info *struct {
			Total   *codexUsage `json:"total_token_usage"`
			Last    *codexUsage `json:"last_token_usage"`
			Context int64       `json:"model_context_window"`
		} `json:"info"`
	} `json:"payload"`
}

func (c *Codex) Scan(ctx context.Context, emit func(model.Turn)) error {
	roots := c.Roots()
	if len(roots) == 0 {
		return nil
	}
	var files []string
	err := filepath.WalkDir(roots[0], func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree is skipped, not fatal
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, t := range mapFiles(ctx, files, c.scanFile) {
		emit(t)
	}
	return nil
}

func (c *Codex) scanFile(path string) []model.Turn {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var records []codexRecord
	for line := range jsonLines(f) {
		var r codexRecord
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		switch r.Type {
		case "session_meta", "turn_context", "token_usage_record":
			records = append(records, r)
		case "event_msg":
			switch r.Payload.Type {
			case "token_count", "task_started", "turn_started":
				records = append(records, r)
			}
		}
	}
	legacy, exact := codexStructured(records)
	turns := scanCodexLegacy(legacy)
	return append(turns, exact...)
}

func scanCodexLegacy(records []codexRecord) []model.Turn {
	var (
		sessionID string
		project   string
		modelID   string
		provider  string
		forked    bool
		subagent  bool
		turns     []model.Turn

		// Replay detection — see replayGap.
		lastTS        time.Time
		burstResolved bool
		pending       []model.Turn
	)

	// flushBurst decides what to do with the held opening run once the first
	// real gap appears. More than one densely-packed leading event is inherited
	// history and is dropped; a single event is this thread's own first call
	// and is kept.
	flushBurst := func() {
		if burstResolved {
			return
		}
		burstResolved = true
		if len(pending) == 1 {
			turns = append(turns, pending[0])
		}
		pending = nil
	}

	for _, r := range records {
		if r.Type == "session_meta" {
			// The first session_meta is this file's own thread. Forked files
			// then write the parent's meta immediately after, which must not
			// overwrite our identity.
			if sessionID == "" {
				sessionID = firstNonEmpty(r.Payload.ID, r.Payload.SessionID)
				project = r.Payload.CWD
				provider = r.Payload.ModelProvider
				if r.Payload.Model != "" {
					modelID = r.Payload.Model
				}
				subagent = r.Payload.ThreadSource == "subagent"
				forked = subagent || r.Payload.ForkedFromID != "" || r.Payload.ParentThreadID != ""
			}
			continue
		}

		// turn_context carries the model in force for the coming turn, which
		// can differ from session_meta when the user switches mid-thread.
		if r.Type == "turn_context" {
			if r.Payload.Model != "" {
				modelID = r.Payload.Model
			}
			if r.Payload.CWD != "" {
				project = r.Payload.CWD
			}
			continue
		}

		if r.Payload.Type != "token_count" || r.Payload.Info == nil || r.Payload.Info.Last == nil {
			continue
		}
		lu := *r.Payload.Info.Last
		// input_tokens is GROSS here: it includes cache reads and writes.
		// Netting is required or cache reads get billed twice, once at the
		// input rate and once at the cache-read rate.
		net := lu.Input - lu.Cached - lu.CacheWrite
		if net < 0 {
			net = 0
		}
		usage := model.Usage{
			Input:      net,
			Output:     lu.Output,
			CacheRead:  lu.Cached,
			CacheWrite: lu.CacheWrite,
			Reasoning:  lu.Reasoning,
			// Gross input is the best available proxy for prompt size, which
			// is what drives long-context tier pricing.
			ContextTokens: lu.Input,
		}
		usage, ok := usage.Sanitize()
		if !ok || usage.IsZero() {
			continue // corrupt or empty record
		}

		turn := model.Turn{
			SessionID:       sessionID,
			Agent:           model.AgentCodex,
			Timestamp:       parseTime(r.Timestamp),
			Model:           modelID,
			Provider:        provider,
			Project:         project,
			Usage:           usage,
			Subagent:        subagent,
			ReplayHeuristic: forked,
		}

		if !forked || burstResolved {
			turns = append(turns, turn)
			continue
		}
		// Still in the leading run: hold the turn while events keep arriving
		// back-to-back, and cut the run at the first real pause.
		if len(pending) > 0 && turn.Timestamp.Sub(lastTS) >= replayGap {
			flushBurst()
			turns = append(turns, turn)
			continue
		}
		lastTS = turn.Timestamp
		pending = append(pending, turn)
	}
	flushBurst()

	backfillModels(turns)
	for i := range turns {
		turns[i].Key = codexKey(sessionID, i+1)
	}
	return turns
}

// backfillModels fills in turns logged before the first turn_context named a
// model. Codex writes turn_context at the start of a turn, but 61 of the
// audited machine's rollouts emit a token_count first, leaving the opening
// calls modelless — and an unnamed model is an unpriced turn, which silently
// dropped 1.03B tokens out of the totals.
//
// The fill runs backwards from the first known model, which is the model those
// calls were served by: a thread does not change model between a call and the
// context record that follows it.
func backfillModels(turns []model.Turn) {
	firstKnown := -1
	for i := range turns {
		if turns[i].Model != "" {
			firstKnown = i
			break
		}
	}
	if firstKnown <= 0 {
		return // every turn already has one, or none does
	}
	for i := range firstKnown {
		turns[i].Model = turns[firstKnown].Model
	}
}

// codexKey identifies a legacy event within a session after replay filtering.
func codexKey(sessionID string, seq int) string {
	if sessionID == "" {
		return ""
	}
	return "codex|" + sessionID + "|" + strconv.Itoa(seq)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
