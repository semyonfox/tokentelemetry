package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

const copilotOTelFileExporterPathEnv = "COPILOT_OTEL_FILE_EXPORTER_PATH"

// copilotOTelExporterPath returns only the file explicitly configured for
// Copilot CLI's file exporter. GitHub documents no default file location.
func copilotOTelExporterPath() string {
	path := strings.TrimSpace(os.Getenv(copilotOTelFileExporterPathEnv))
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

// copilotOTelSpan is the documented Copilot CLI file-export envelope needed
// for accounting. Attributes remain raw so content-capture fields are never
// decoded or retained.
type copilotOTelSpan struct {
	Type       string                     `json:"type"`
	TraceID    string                     `json:"traceId"`
	SpanID     string                     `json:"spanId"`
	EndTime    []json.RawMessage          `json:"endTime"`
	Attributes map[string]json.RawMessage `json:"attributes"`
}

// scanCopilotOTel reads Copilot CLI's optional JSON-lines file exporter. It
// accepts only direct chat spans, never invoke_agent rollups or token metrics.
func scanCopilotOTel(ctx context.Context, path string) ([]model.Turn, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Copilot OpenTelemetry export: %w", err)
	}
	defer f.Close()

	var turns []model.Turn
	for line := range jsonLines(f) {
		if err := ctx.Err(); err != nil {
			return turns, err
		}
		var span copilotOTelSpan
		if json.Unmarshal(line, &span) != nil {
			continue
		}
		turn, ok := copilotTurnFromOTelSpan(span)
		if ok {
			turns = append(turns, turn)
		}
	}
	return turns, nil
}

func copilotTurnFromOTelSpan(span copilotOTelSpan) (model.Turn, bool) {
	if span.Type != "span" || copilotOTelAttrString(span.Attributes, "gen_ai.operation.name") != "chat" {
		return model.Turn{}, false
	}
	traceID := strings.TrimSpace(span.TraceID)
	spanID := strings.TrimSpace(span.SpanID)
	sessionID := copilotOTelAttrString(span.Attributes, "gen_ai.conversation.id")
	modelID := firstNonEmpty(
		copilotOTelAttrString(span.Attributes, "gen_ai.response.model"),
		copilotOTelAttrString(span.Attributes, "gen_ai.request.model"),
	)
	timestamp := copilotOTelEndTime(span.EndTime)
	if traceID == "" || spanID == "" || sessionID == "" || modelID == "" || timestamp.IsZero() {
		return model.Turn{}, false
	}

	input, inputOK := copilotOTelAttrInt(span.Attributes, "gen_ai.usage.input_tokens")
	output, outputOK := copilotOTelAttrInt(span.Attributes, "gen_ai.usage.output_tokens")
	cacheRead, readOK := copilotOTelOptionalAttrInt(span.Attributes, "gen_ai.usage.cache_read.input_tokens")
	cacheWrite, writeOK := copilotOTelOptionalAttrInt(span.Attributes, "gen_ai.usage.cache_creation.input_tokens")
	if !inputOK || !outputOK || !readOK || !writeOK {
		return model.Turn{}, false
	}
	usage, _, ok := normalizeCopilotUsage(input, output, cacheRead, cacheWrite, 0, false)
	if !ok || usage.IsZero() {
		return model.Turn{}, false
	}

	return model.Turn{
		Key:       identityKey("copilot-otel", traceID, spanID),
		SessionID: sessionID,
		Agent:     model.AgentCopilot,
		Timestamp: timestamp,
		Model:     modelID,
		Provider:  "github-copilot",
		Usage:     usage,
	}, true
}

func copilotOTelAttrString(attributes map[string]json.RawMessage, key string) string {
	raw, ok := attributes[key]
	if !ok {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func copilotOTelAttrInt(attributes map[string]json.RawMessage, key string) (int64, bool) {
	raw, ok := attributes[key]
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	return value, err == nil
}

func copilotOTelOptionalAttrInt(attributes map[string]json.RawMessage, key string) (int64, bool) {
	if _, ok := attributes[key]; !ok {
		return 0, true
	}
	return copilotOTelAttrInt(attributes, key)
}

func copilotOTelEndTime(parts []json.RawMessage) time.Time {
	if len(parts) != 2 {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(string(parts[0]), 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}
	}
	nanos, err := strconv.ParseInt(string(parts[1]), 10, 64)
	if err != nil || nanos < 0 || nanos >= int64(time.Second) {
		return time.Time{}
	}
	return time.Unix(seconds, nanos).UTC()
}
