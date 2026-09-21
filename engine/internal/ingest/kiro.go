package ingest

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

type Kiro struct{ roots []string }

func NewKiro() *Kiro {
	p := filepath.Join(homeDir(), ".kiro", "sessions", "cli")
	if existingDir(p) == "" {
		return &Kiro{}
	}
	return &Kiro{[]string{p}}
}
func (k *Kiro) Agent() model.Agent { return model.Agent("kiro") }
func (k *Kiro) Roots() []string    { return append([]string(nil), k.roots...) }
func (k *Kiro) Scan(ctx context.Context, emit func(model.Turn)) error {
	for _, root := range k.roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			turns := scanKiroCLIMetadata(filepath.Join(root, entry.Name()))
			for _, turn := range turns {
				emit(turn)
			}
		}
	}
	return nil
}

type kiroCLIMeta struct {
	SessionID    string `json:"session_id"`
	CWD          string `json:"cwd"`
	CreatedAt    string `json:"created_at"`
	SessionState struct {
		ModelState struct {
			ModelInfo struct {
				ModelID string `json:"model_id"`
			} `json:"model_info"`
		} `json:"rts_model_state"`
		Conversation struct {
			Turns []struct {
				End      string `json:"end_timestamp"`
				Metering []struct {
					Value float64 `json:"value"`
					Unit  string  `json:"unit"`
				} `json:"metering_usage"`
			} `json:"user_turn_metadatas"`
		} `json:"conversation_metadata"`
	} `json:"session_state"`
}

func scanKiroCLIMetadata(path string) []model.Turn {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > 16<<20 {
		return nil
	}
	var meta kiroCLIMeta
	if json.Unmarshal(raw, &meta) != nil {
		return nil
	}
	sid := meta.SessionID
	if sid == "" {
		sid = strings.TrimSuffix(filepath.Base(path), ".json")
	}
	modelID := meta.SessionState.ModelState.ModelInfo.ModelID
	if modelID == "" || modelID == "auto" {
		modelID = "kiro-auto"
	}
	var out []model.Turn
	for i, item := range meta.SessionState.Conversation.Turns {
		credits := 0.0
		valid := true
		for _, meter := range item.Metering {
			if meter.Unit != "credit" || math.IsNaN(meter.Value) || math.IsInf(meter.Value, 0) || meter.Value < 0 {
				valid = false
				break
			}
			credits += meter.Value
		}
		if !valid || credits <= 0 || math.IsInf(credits, 0) {
			continue
		}
		value := credits
		out = append(out, model.Turn{Key: identityKey("kiro-cli-credit", sid, strconv.Itoa(i)), SessionID: sid, Agent: model.Agent("kiro"), Timestamp: localTimestamp(item.End), Model: modelID, Provider: "kiro", Project: meta.CWD, Credits: &value, UnpricedReason: "Kiro records credits without token counts", Aggregate: true})
	}
	return out
}
