package ingest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func dshHeader(version int, id string) string {
	return `{"type":"session","version":` + strconv.Itoa(version) + `,"id":"` + id + `","createdAt":1786707340000,"cwd":"/fixture/project","isSeeded":false}`
}

func TestDSHFinalMessageReplacesDraftAndRetryAddsAttempt(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "--fixture--", "session-one")
	writeFile(t, filepath.Join(dir, "session.v3.jsonl"),
		dshHeader(3, "s1"),
		`{"type":"request/context","seq":0,"time":1786707337000,"data":{"model":"requested"}}`,
		`{"type":"assistant/attempt","seq":1,"time":1786707340000,"data":{"turn":1,"step":1,"stream":[{"type":"chunk","chunk":{"type":"usage","usage":{"inputTokens":10,"outputTokens":4,"cacheReadTokens":3,"cacheWriteTokens":2,"reasoningTokens":2}}}]}}`,
		`{"type":"llm/retry-started","seq":2,"time":1786707340001,"data":{"turn":1,"step":1}}`,
		`{"type":"assistant/message","seq":3,"time":1786707340002,"data":{"turn":1,"step":1,"message":{"source":{"model":"served"}},"usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":30,"cacheWriteTokens":5,"reasoningTokens":8},"stream":[{"type":"chunk","chunk":{"type":"usage","usage":{"inputTokens":90,"outputTokens":19}}}]}}`)
	turns := scan(t, newDSHAt(filepath.Join(root, "sessions")))
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(turns))
	}
	if turns[0].Usage != (model.Usage{Input: 10, Output: 4, CacheRead: 3, CacheWrite: 2, Reasoning: 2}) {
		t.Fatalf("first = %+v", turns[0])
	}
	if turns[1].Usage != (model.Usage{Input: 100, Output: 20, CacheRead: 30, CacheWrite: 5, Reasoning: 8}) || turns[1].Model != "served" {
		t.Fatalf("second = %+v", turns[1])
	}
	if turns[0].Key == turns[1].Key {
		t.Fatal("retry reused attempt identity")
	}
}

func TestDSHSelectsHighestGeneration(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "--fixture--", "session-one")
	writeFile(t, filepath.Join(dir, "session.jsonl"), dshHeader(0, "same"), `{"type":"assistant/message","time":1786707340000,"data":{"turn":1,"step":1,"usage":{"inputTokens":1,"outputTokens":1}}}`)
	writeFile(t, filepath.Join(dir, "session.v3.jsonl"), dshHeader(3, "same"), `{"type":"assistant/message","time":1786707340000,"data":{"turn":1,"step":1,"usage":{"inputTokens":3,"outputTokens":1}}}`)
	turns := scan(t, newDSHAt(filepath.Join(root, "sessions")))
	if len(turns) != 1 || turns[0].Usage.Input != 3 {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestDSHReadsIndependentZstdFramesAndIgnoresTornTail(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "--fixture--", "session-one", "session.v3.jsonl.zstd")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	first := encoder.EncodeAll([]byte(dshHeader(3, "zstd")+"\n"), nil)
	second := encoder.EncodeAll([]byte(`{"type":"assistant/message","time":1786707340000,"data":{"turn":1,"step":1,"message":{"source":{"model":"deepseek-v4"}},"usage":{"inputTokens":100,"outputTokens":20}}}`+"\n"), nil)
	data := append(append(bytes.Clone(first), second...), first[:len(first)/2]...)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	turns := scan(t, newDSHAt(filepath.Join(root, "sessions")))
	if len(turns) != 1 || turns[0].Usage.Input != 100 {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestDSHMessageReplacesChunkWithoutDoubleCounting(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "--fixture--", "session-one")
	writeFile(t, filepath.Join(dir, "session.v1.jsonl"), dshHeader(1, "v1"),
		`{"type":"assistant/chunk","seq":1,"time":1786707340000,"data":{"turn":2,"step":1,"chunk":{"type":"usage","usage":{"inputTokens":90,"outputTokens":9}}}}`,
		`{"type":"assistant/message","seq":2,"time":1786707340001,"data":{"turn":2,"step":1,"message":{"source":{"model":"final-model"}},"usage":{"inputTokens":100,"outputTokens":10}}}`)
	turns := scan(t, newDSHAt(filepath.Join(root, "sessions")))
	if len(turns) != 1 || turns[0].Usage.Input != 100 || turns[0].Model != "final-model" {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestDSHReadsEveryReleasedUsageLayout(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3} {
		t.Run(strconv.Itoa(version), func(t *testing.T) {
			root := t.TempDir()
			name := "session.jsonl"
			if version > 0 {
				name = "session.v" + strconv.Itoa(version) + ".jsonl"
			}
			path := filepath.Join(root, "sessions", "--fixture--", "session-one", name)
			if version <= 1 {
				writeFile(t, path, dshHeader(version, "layout"),
					`{"type":"assistant/chunk","seq":1,"time":1786707340000,"data":{"turn":1,"step":1,"chunk":{"type":"usage","usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":30,"cacheWriteTokens":5,"reasoningTokens":8}}}}`)
			} else {
				writeFile(t, path, dshHeader(version, "layout"),
					`{"type":"assistant/attempt","seq":1,"time":1786707340000,"data":{"turn":1,"step":1,"stream":[{"type":"chunk","chunk":{"type":"usage","usage":{"inputTokens":100,"outputTokens":20,"cacheReadTokens":30,"cacheWriteTokens":5,"reasoningTokens":8}}}]}}`)
			}
			turns := scan(t, newDSHAt(filepath.Join(root, "sessions")))
			want := model.Usage{Input: 100, Output: 20, CacheRead: 30, CacheWrite: 5, Reasoning: 8}
			if len(turns) != 1 || turns[0].Usage != want {
				t.Fatalf("turns = %+v, want usage %+v", turns, want)
			}
		})
	}
}

func TestDSHRejectsCorruptCompleteZstdFrameWithoutPartialAccounting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "--fixture--", "session-one", "session.v3.jsonl.zstd")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	first := encoder.EncodeAll([]byte(dshHeader(3, "corrupt")+"\n"), nil)
	second := encoder.EncodeAll([]byte(`{"type":"assistant/message","time":1786707340000,"data":{"turn":1,"step":1,"usage":{"inputTokens":100,"outputTokens":20}}}`+"\n"), nil)
	second[len(second)-1] ^= 0xff
	if err := os.WriteFile(path, append(first, second...), 0o644); err != nil {
		t.Fatal(err)
	}
	var turns []model.Turn
	err = newDSHAt(filepath.Join(root, "sessions")).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || len(turns) != 0 {
		t.Fatalf("err = %v, turns = %+v", err, turns)
	}
}

func TestDSHDoesNotFallBackFromUnknownHighestGeneration(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sessions", "--fixture--", "session-one")
	writeFile(t, filepath.Join(dir, "session.v3.jsonl"), dshHeader(3, "same"),
		`{"type":"assistant/message","time":1786707340000,"data":{"turn":1,"step":1,"usage":{"inputTokens":3,"outputTokens":1}}}`)
	writeFile(t, filepath.Join(dir, "session.v4.jsonl"), dshHeader(4, "same"),
		`{"type":"assistant/message","time":1786707340000,"data":{"turn":1,"step":1,"usage":{"inputTokens":4,"outputTokens":1}}}`)
	var turns []model.Turn
	err := newDSHAt(filepath.Join(root, "sessions")).Scan(context.Background(), func(turn model.Turn) { turns = append(turns, turn) })
	if err == nil || len(turns) != 0 {
		t.Fatalf("err = %v, turns = %+v", err, turns)
	}
}
