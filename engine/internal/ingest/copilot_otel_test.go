package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestCopilotOTelExporterPathIsExplicitOnly(t *testing.T) {
	t.Setenv(copilotOTelFileExporterPathEnv, "")
	if path := copilotOTelExporterPath(); path != "" {
		t.Fatalf("unset exporter path = %q, want no undocumented default", path)
	}

	want := filepath.Join(t.TempDir(), "usage.jsonl")
	t.Setenv(copilotOTelFileExporterPathEnv, "  "+want+"  ")
	if got := copilotOTelExporterPath(); got != want {
		t.Fatalf("exporter path = %q, want %q", got, want)
	}
}

func TestCopilotOTelReadsDirectChatSpan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "copilot.jsonl")
	writeFile(t, path,
		`{"type":"span","traceId":"trace-a","spanId":"span-a","name":"chat requested-model","endTime":[1789812672,123456789],"attributes":{"gen_ai.operation.name":"chat","gen_ai.provider.name":"anthropic","gen_ai.request.model":"requested-model","gen_ai.response.model":"resolved-model","gen_ai.conversation.id":"session-a","gen_ai.response.id":"response-a","github.copilot.turn_id":"turn-a","github.copilot.interaction_id":"interaction-a","gen_ai.usage.input_tokens":1000,"gen_ai.usage.output_tokens":70,"gen_ai.usage.cache_read.input_tokens":600,"gen_ai.usage.cache_creation.input_tokens":100,"gen_ai.input.messages":"private content must not be read"}}`,
	)

	turns, err := scanCopilotOTel(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 {
		t.Fatalf("turns = %#v, want one direct chat call", turns)
	}
	turn := turns[0]
	if turn.Key != identityKey("copilot-otel", "trace-a", "span-a") || turn.SessionID != "session-a" {
		t.Fatalf("identity = %#v", turn)
	}
	if turn.Agent != model.AgentCopilot || turn.Model != "resolved-model" || turn.Provider != "github-copilot" || turn.Aggregate {
		t.Fatalf("attribution = %#v", turn)
	}
	wantTime := time.Date(2026, 9, 19, 10, 11, 12, 123_456_789, time.UTC)
	if !turn.Timestamp.Equal(wantTime) {
		t.Fatalf("timestamp = %s, want %s", turn.Timestamp, wantTime)
	}
	wantUsage := model.Usage{Input: 300, Output: 70, CacheRead: 600, CacheWrite: 100, ContextTokens: 1000}
	if turn.Usage != wantUsage {
		t.Fatalf("usage = %#v, want %#v", turn.Usage, wantUsage)
	}
}

func TestCopilotOTelIgnoresAggregatesMetricsAndLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed.jsonl")
	writeFile(t, path,
		`{"type":"span","traceId":"trace-root","spanId":"span-root","endTime":[1789803072,0],"attributes":{"gen_ai.operation.name":"invoke_agent","gen_ai.response.model":"gpt-5","gen_ai.conversation.id":"session","gen_ai.usage.input_tokens":100,"gen_ai.usage.output_tokens":10}}`,
		`{"type":"metric","name":"gen_ai.client.token.usage","attributes":{"gen_ai.operation.name":"chat","gen_ai.response.model":"gpt-5","gen_ai.conversation.id":"session","gen_ai.usage.input_tokens":100,"gen_ai.usage.output_tokens":10}}`,
		`{"type":"log","traceId":"trace-log","spanId":"span-log","endTime":[1789803072,0],"attributes":{"gen_ai.operation.name":"chat","gen_ai.response.model":"gpt-5","gen_ai.conversation.id":"session","gen_ai.usage.input_tokens":100,"gen_ai.usage.output_tokens":10}}`,
		`not json`,
	)

	turns, err := scanCopilotOTel(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Fatalf("turns = %#v, want no aggregate or duplicate signal", turns)
	}
}

func TestCopilotOTelRequiresDocumentedIdentityAndShape(t *testing.T) {
	base := `{"type":"span","traceId":"trace","spanId":"span","endTime":[1789803072,0],"attributes":{"gen_ai.operation.name":"chat","gen_ai.response.model":"gpt-5","gen_ai.conversation.id":"session","gen_ai.usage.input_tokens":100,"gen_ai.usage.output_tokens":10}}`
	cases := map[string]string{
		"missing conversation": strings.Replace(base, `"gen_ai.conversation.id":"session",`, "", 1),
		"missing trace":        strings.Replace(base, `"traceId":"trace",`, "", 1),
		"missing span":         strings.Replace(base, `"spanId":"span",`, "", 1),
		"missing end time":     strings.Replace(base, `"endTime":[1789803072,0],`, "", 1),
		"missing model":        strings.Replace(base, `"gen_ai.response.model":"gpt-5",`, "", 1),
		"string token count":   strings.Replace(base, `"gen_ai.usage.input_tokens":100`, `"gen_ai.usage.input_tokens":"100"`, 1),
		"aggregate operation":  strings.Replace(base, `"gen_ai.operation.name":"chat"`, `"gen_ai.operation.name":"invoke_agent"`, 1),
		"wrong record type":    strings.Replace(base, `"type":"span"`, `"type":"metric"`, 1),
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.jsonl")
			writeFile(t, path, line)
			turns, err := scanCopilotOTel(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			if len(turns) != 0 {
				t.Fatalf("turns = %#v, want unsupported record skipped", turns)
			}
		})
	}
}

func TestCopilotOTelRejectsContradictoryCacheBuckets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-usage.jsonl")
	writeFile(t, path,
		`{"type":"span","traceId":"trace","spanId":"span","endTime":[1789803072,0],"attributes":{"gen_ai.operation.name":"chat","gen_ai.response.model":"gpt-5","gen_ai.conversation.id":"session","gen_ai.usage.input_tokens":100,"gen_ai.usage.output_tokens":10,"gen_ai.usage.cache_read.input_tokens":90,"gen_ai.usage.cache_creation.input_tokens":20}}`,
	)
	turns, err := scanCopilotOTel(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Fatalf("turns = %#v, want contradictory usage skipped", turns)
	}
}

func TestCopilotOTelReportsOpenAndCancellationErrors(t *testing.T) {
	if _, err := scanCopilotOTel(context.Background(), filepath.Join(t.TempDir(), "missing.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v, want os.ErrNotExist", err)
	}

	path := filepath.Join(t.TempDir(), "cancelled.jsonl")
	writeFile(t, path, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanCopilotOTel(ctx, path); err != context.Canceled {
		t.Fatalf("cancelled scan error = %v, want context.Canceled", err)
	}
}
