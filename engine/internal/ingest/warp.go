package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

const (
	warpGroupContainer = "2BBY89MBSN.dev.warp"
	warpStableBundle   = "dev.warp.Warp-Stable"
	warpPreviewBundle  = "dev.warp.Warp-Preview"
)

// Warp reads the conversation-level token totals in Warp's local database.
// Warp does not persist a trustworthy input/output split, so each model total
// remains unclassified rather than being estimated from transcript text.
type Warp struct{ dbPath string }

func NewWarp() *Warp {
	if override := os.Getenv("WARP_DB_PATH"); override != "" {
		if fileExists(override) {
			return &Warp{dbPath: override}
		}
		return &Warp{}
	}
	home := homeDir()
	if home == "" {
		return &Warp{}
	}
	for _, bundle := range []string{warpStableBundle, warpPreviewBundle} {
		path := filepath.Join(home, "Library", "Group Containers", warpGroupContainer,
			"Library", "Application Support", bundle, "warp.sqlite")
		if fileExists(path) {
			return &Warp{dbPath: path}
		}
	}
	return &Warp{}
}

func newWarpAt(path string) *Warp  { return &Warp{dbPath: path} }
func (w *Warp) Agent() model.Agent { return model.Agent("warp") }
func (w *Warp) Roots() []string {
	if w.dbPath == "" {
		return nil
	}
	return []string{w.dbPath}
}

type warpConversationData struct {
	Usage struct {
		TokenUsage []warpTokenUsage `json:"token_usage"`
	} `json:"conversation_usage_metadata"`
}

type warpTokenUsage struct {
	ModelID    string          `json:"model_id"`
	WarpTokens json.RawMessage `json:"warp_tokens"`
	BYOKTokens json.RawMessage `json:"byok_tokens"`
}

func (w *Warp) Scan(ctx context.Context, emit func(model.Turn)) error {
	if w.dbPath == "" {
		return nil
	}
	db, err := sql.Open("sqlite", warpReadOnlyDSN(w.dbPath))
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT conversation_id, conversation_data, last_modified_at FROM agent_conversations`)
	if err != nil {
		return fmt.Errorf("warp database %q: %w", w.dbPath, err)
	}
	defer rows.Close()

	var scanErr error
	for rows.Next() {
		var conversationID, rawData string
		var modified sql.NullString
		if err := rows.Scan(&conversationID, &rawData, &modified); err != nil {
			return err
		}
		if conversationID == "" {
			continue
		}
		var data warpConversationData
		if err := json.Unmarshal([]byte(rawData), &data); err != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("warp conversation %q: invalid conversation_data: %w", conversationID, err))
			continue
		}
		timestamp := parseWarpTime(modified.String)
		if timestamp.IsZero() {
			scanErr = errors.Join(scanErr, fmt.Errorf("warp conversation %q has usage without a valid last_modified_at", conversationID))
		}

		// The array can contain more than one accounting row for a model. Merge
		// those rows before emitting so each conversation/model total appears once.
		byModel := make(map[string]int64)
		invalidModel := make(map[string]bool)
		for _, entry := range data.Usage.TokenUsage {
			modelID := strings.TrimSpace(entry.ModelID)
			if modelID == "" {
				modelID = "unknown"
			}
			warpTokens, okWarp := warpTokenCount(entry.WarpTokens)
			byokTokens, okBYOK := warpTokenCount(entry.BYOKTokens)
			if !okWarp || !okBYOK || warpTokens > math.MaxInt64-byokTokens {
				scanErr = errors.Join(scanErr, fmt.Errorf("warp conversation %q has invalid token totals", conversationID))
				invalidModel[modelID] = true
				delete(byModel, modelID)
				continue
			}
			total := warpTokens + byokTokens
			if total == 0 || invalidModel[modelID] {
				continue
			}
			if byModel[modelID] > math.MaxInt64-total {
				scanErr = errors.Join(scanErr, fmt.Errorf("warp conversation %q model %q exceeds token limit", conversationID, modelID))
				invalidModel[modelID] = true
				delete(byModel, modelID)
				continue
			}
			byModel[modelID] += total
		}
		for modelID, total := range byModel {
			if total == 0 {
				continue
			}
			usage, ok := (model.Usage{Unclassified: total}).SanitizeAggregate()
			if !ok {
				scanErr = errors.Join(scanErr, fmt.Errorf("warp conversation %q model %q exceeds aggregate token limit", conversationID, modelID))
				continue
			}
			emit(model.Turn{
				Key: identityKey("warp", conversationID, modelID), SessionID: conversationID,
				Agent: model.Agent("warp"), Timestamp: timestamp, Model: modelID, Provider: "warp",
				Usage: usage, Aggregate: true,
				UnpricedReason: "Warp records only a conversation-level total without an input/output/cache split",
			})
		}
	}
	return errors.Join(scanErr, rows.Err())
}

func warpTokenCount(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, true
	}
	var value json.Number
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	count, err := strconv.ParseInt(value.String(), 10, 64)
	if err != nil || count < 0 {
		return 0, false
	}
	return count, true
}

func warpReadOnlyDSN(path string) string {
	uriPath := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&_pragma=busy_timeout(3000)"}).String()
}

func parseWarpTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if t := parseTime(raw); !t.IsZero() {
		return t
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return t
		}
	}
	return time.Time{}
}
