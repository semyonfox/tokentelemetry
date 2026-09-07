package ingest

import (
	"encoding/json"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
)

// identityKey encodes components without delimiter collisions.
func identityKey(kind string, parts ...string) string {
	b, _ := json.Marshal(parts) // strings are always JSON-encodable
	return kind + "|" + string(b)
}

// codexStructured replaces token_count snapshots only in turns with valid
// structured usage. A rollout resumed after an upgrade can still contain older
// turns; suppressing the entire file would lose that history.
func codexStructured(records []codexRecord) ([]codexRecord, []model.Turn) {
	var ownID, modelID, provider, project, activeTurn string
	var subagent bool
	segment := 0
	segments := make([]int, len(records))
	covered := map[int]bool{}
	var exact []model.Turn
	for i, r := range records {
		p := r.Payload
		if r.Type == "session_meta" && ownID == "" {
			ownID = firstNonEmpty(p.ID, p.SessionID)
			modelID, provider, project = p.Model, p.ModelProvider, p.CWD
			subagent = p.ThreadSource == "subagent"
		}
		if r.Type == "turn_context" || (r.Type == "event_msg" && (p.Type == "task_started" || p.Type == "turn_started")) {
			if p.TurnID == "" || p.TurnID != activeTurn {
				segment++
			}
			activeTurn = p.TurnID
			if p.Model != "" {
				modelID = p.Model
			}
			if p.CWD != "" {
				project = p.CWD
			}
		}
		segments[i] = segment
		if r.Type != "token_usage_record" || p.Usage == nil || p.ThreadID == "" || p.ResponseID == "" {
			continue
		}
		ts := parseTime(r.Timestamp)
		if ts.IsZero() {
			continue
		}
		u, ok := structuredCodexUsage(*p.Usage)
		if !ok {
			continue
		}
		// Ownership survives copying into a fork, unlike the enclosing timestamp.
		// A copied response must never be billed to its child. The original rollout
		// supplies its original timestamp/model; no invented timestamp fallback.
		if p.ThreadID != ownID {
			covered[segment] = true
			continue
		}
		if activeTurn != "" && p.TurnID != "" && activeTurn != p.TurnID {
			// Inconsistent context cannot safely replace a legacy turn's usage.
			continue
		}
		covered[segment] = true
		exact = append(exact, model.Turn{
			Key:       identityKey("codex-response", p.ThreadID, p.ResponseID),
			SessionID: ownID, Agent: model.AgentCodex, Timestamp: ts,
			Model: modelID, Provider: provider, Project: project, Usage: u, Subagent: subagent,
		})
	}
	var legacy []codexRecord
	for i, r := range records {
		if covered[segments[i]] && r.Type == "event_msg" && r.Payload.Type == "token_count" {
			continue
		}
		legacy = append(legacy, r)
	}
	backfillModels(exact)
	return legacy, exact
}

func structuredCodexUsage(u codexUsage) (model.Usage, bool) {
	// Input is gross and includes both cache buckets in the upstream usage schema.
	if u.Input < 0 || u.Cached < 0 || u.CacheWrite < 0 || u.Output < 0 || u.Reasoning < 0 || u.Cached > u.Input || u.CacheWrite > u.Input-u.Cached {
		return model.Usage{}, false
	}
	v := model.Usage{Input: u.Input - u.Cached - u.CacheWrite, Output: u.Output, CacheRead: u.Cached, CacheWrite: u.CacheWrite, Reasoning: u.Reasoning, ContextTokens: u.Input}
	v, ok := v.Sanitize()
	return v, ok && !v.IsZero()
}
