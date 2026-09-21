package ingest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

const (
	dshMaxFileBytes  = 128 << 20
	dshMaxFrameBytes = 64 << 20
)

var dshSessionName = regexp.MustCompile(`^session(?:\.v([0-9]+))?\.jsonl(\.zstd)?$`)

// DSH reads DeepSeek Harness's versioned JSONL session store.
type DSH struct{ root string }

func NewDSH() *DSH {
	home := os.Getenv("DSH_HOME")
	if home == "" {
		if userHome := homeDir(); userHome != "" {
			home = filepath.Join(userHome, ".dsh")
		}
	}
	if home == "" {
		return &DSH{}
	}
	return newDSHAt(filepath.Join(home, "sessions"))
}

func newDSHAt(sessionsRoot string) *DSH { return &DSH{root: existingDir(sessionsRoot)} }
func (d *DSH) Agent() model.Agent       { return model.Agent("dsh") }
func (d *DSH) Roots() []string {
	if d.root == "" {
		return nil
	}
	return []string{d.root}
}

type dshFile struct {
	path       string
	version    int
	compressed bool
}

func (d *DSH) Scan(ctx context.Context, emit func(model.Turn)) error {
	if d.root == "" {
		return nil
	}
	byDir := map[string][]dshFile{}
	invalidDir := map[string]bool{}
	var scanErr error
	err := filepath.WalkDir(d.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			scanErr = errors.Join(scanErr, walkErr)
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		match := dshSessionName.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil
		}
		version := 0
		if match[1] != "" {
			parsed, err := strconv.ParseUint(match[1], 10, 31)
			if err != nil {
				scanErr = errors.Join(scanErr, fmt.Errorf("dsh: ambiguous generation %q", path))
				invalidDir[filepath.Dir(path)] = true
				return nil
			}
			version = int(parsed)
		}
		byDir[filepath.Dir(path)] = append(byDir[filepath.Dir(path)], dshFile{path: path, version: version, compressed: match[2] != ""})
		return nil
	})
	if err != nil {
		return err
	}

	dirs := make([]string, 0, len(byDir))
	for dir := range byDir {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		if invalidDir[dir] {
			continue
		}
		files := byDir[dir]
		sort.Slice(files, func(i, j int) bool {
			if files[i].version != files[j].version {
				return files[i].version > files[j].version
			}
			return files[i].compressed && !files[j].compressed
		})
		selected := files[0]
		if len(files) > 1 && files[1].version == selected.version && files[1].compressed == selected.compressed {
			scanErr = errors.Join(scanErr, fmt.Errorf("dsh: ambiguous generation files in %q", dir))
			continue
		}
		if selected.version > 3 {
			scanErr = errors.Join(scanErr, fmt.Errorf("dsh: unsupported session format v%d in %q", selected.version, selected.path))
			continue
		}
		turns, err := scanDSHFile(selected)
		if err != nil {
			scanErr = errors.Join(scanErr, fmt.Errorf("dsh: %s: %w", selected.path, err))
			continue
		}
		for _, turn := range turns {
			emit(turn)
		}
	}
	return scanErr
}

type dshUsage struct {
	Input      *int64 `json:"inputTokens"`
	Output     *int64 `json:"outputTokens"`
	CacheRead  *int64 `json:"cacheReadTokens"`
	CacheWrite *int64 `json:"cacheWriteTokens"`
	Reasoning  *int64 `json:"reasoningTokens"`
}
type dshChunk struct {
	Type  string    `json:"type"`
	Usage *dshUsage `json:"usage"`
}
type dshStreamRecord struct {
	Type  string    `json:"type"`
	Time  int64     `json:"time"`
	Chunk *dshChunk `json:"chunk"`
}
type dshEvent struct {
	Type          string `json:"type"`
	Seq           *int64 `json:"seq"`
	Time          int64  `json:"time"`
	Version       *int   `json:"version"`
	ID            string `json:"id"`
	CWD           string `json:"cwd"`
	CreatedAt     int64  `json:"createdAt"`
	ParentSession string `json:"parentSession"`
	SeedLength    *int64 `json:"seedLength"`
	IsSeeded      bool   `json:"isSeeded"`
	Data          struct {
		Turn      *int64 `json:"turn"`
		Step      *int64 `json:"step"`
		Inherited bool   `json:"inherited"`
		Header    struct {
			Config struct {
				Model string `json:"model"`
			} `json:"config"`
		} `json:"header"`
		Model   string            `json:"model"`
		Usage   *dshUsage         `json:"usage"`
		Chunk   *dshChunk         `json:"chunk"`
		Stream  []dshStreamRecord `json:"stream"`
		Message struct {
			Source struct {
				Model string `json:"model"`
			} `json:"source"`
		} `json:"message"`
	} `json:"data"`
}
type dshObservation struct {
	usage     dshUsage
	timestamp int64
	model     string
	final     bool
}
type dshBucket struct{ observations []dshObservation }

func scanDSHFile(file dshFile) ([]model.Turn, error) {
	content, err := readDSHContent(file)
	if err != nil {
		return nil, err
	}
	lines, err := splitDSHLines(content)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, errors.New("empty session log")
	}
	events := make([]dshEvent, 0, len(lines))
	corruptInterior := false
	for i, line := range lines {
		var event dshEvent
		if err := json.Unmarshal(line, &event); err != nil {
			if i == 0 {
				return nil, errors.New("invalid session header")
			}
			if i != len(lines)-1 {
				corruptInterior = true
			}
			continue
		}
		events = append(events, event)
	}
	if len(events) == 0 || events[0].Type != "session" || events[0].Version == nil || *events[0].Version != file.version {
		return nil, errors.New("filename and session header versions disagree")
	}
	version := file.version
	if version >= 2 && corruptInterior {
		return nil, errors.New("malformed interior event")
	}
	header := events[0]
	inheritedCut := int64(-1)
	if version <= 1 && header.ParentSession != "" && header.SeedLength != nil {
		inheritedCut = *header.SeedLength - 1
	}
	if version >= 2 {
		found := false
		for _, event := range events {
			if event.Type == "session/end-seed" && event.Data.Inherited && event.Seq != nil {
				inheritedCut = *event.Seq
				found = true
			}
		}
		if header.IsSeeded != found {
			return nil, errors.New("seed metadata is inconsistent")
		}
	}

	headerModel, contextModel := "unknown", ""
	currentTurn := int64(0)
	buckets := map[string]*dshBucket{}
	active := map[string]int{}
	for _, event := range events[1:] {
		if event.Type == "request/header" {
			if next := event.Data.Header.Config.Model; next != "" {
				if next != headerModel {
					contextModel = ""
				}
				headerModel = next
			}
			continue
		}
		if event.Type == "request/context" {
			if event.Data.Model != "" {
				contextModel = event.Data.Model
			}
			continue
		}
		if event.Seq != nil && *event.Seq <= inheritedCut {
			continue
		}
		if event.Type == "turn/start" {
			if event.Data.Turn != nil {
				currentTurn = *event.Data.Turn
			}
			continue
		}
		turn, step := currentTurn, int64(0)
		if event.Data.Turn != nil {
			turn = *event.Data.Turn
		}
		if event.Data.Step != nil {
			step = *event.Data.Step
		}
		if turn < 0 || step < 0 {
			continue
		}
		key := strconv.FormatInt(turn, 10) + ":" + strconv.FormatInt(step, 10)
		if event.Type == "llm/retry-started" {
			delete(active, key)
			continue
		}

		var usage *dshUsage
		final := false
		reportedModel := contextModel
		if reportedModel == "" {
			reportedModel = headerModel
		}
		switch {
		case version <= 1 && event.Type == "assistant/chunk" && event.Data.Chunk != nil && event.Data.Chunk.Type == "usage":
			usage = event.Data.Chunk.Usage
		case event.Type == "assistant/message":
			usage, final = event.Data.Usage, true
			if usage == nil && version >= 2 {
				usage = dshUsageFromStream(event.Data.Stream)
			}
			if event.Data.Message.Source.Model != "" {
				reportedModel = event.Data.Message.Source.Model
			}
		case version >= 2 && event.Type == "assistant/attempt":
			usage, final = dshUsageFromStream(event.Data.Stream), true
		default:
			continue
		}
		if usage == nil {
			continue
		}
		bucket := buckets[key]
		if bucket == nil {
			bucket = &dshBucket{}
			buckets[key] = bucket
		}
		observation := dshObservation{usage: *usage, timestamp: event.Time, model: reportedModel, final: final}
		if index, ok := active[key]; ok {
			if !bucket.observations[index].final || final {
				bucket.observations[index] = observation
			}
		} else {
			bucket.observations = append(bucket.observations, observation)
			active[key] = len(bucket.observations) - 1
		}
	}

	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		ai, aj := dshCoordinates(keys[i])
		bi, bj := dshCoordinates(keys[j])
		return ai < bi || ai == bi && aj < bj
	})
	sessionID := header.ID
	if sessionID == "" {
		sessionID = file.path
	}
	var turns []model.Turn
	for _, key := range keys {
		for attempt, observation := range buckets[key].observations {
			usage, complete, ok := normalizeDSHUsage(observation.usage)
			if !ok || usage.IsZero() {
				continue
			}
			timestamp := dshTime(observation.timestamp)
			if timestamp.IsZero() {
				timestamp = dshTime(header.CreatedAt)
			}
			if timestamp.IsZero() {
				continue
			}
			attemptKey := key
			if attempt > 0 {
				attemptKey += ":attempt:" + strconv.Itoa(attempt+1)
			}
			modelID := observation.model
			if modelID == "" {
				modelID = "unknown"
			}
			unpriced := ""
			if !complete {
				unpriced = "DSH usage is incomplete or internally inconsistent"
			}
			turns = append(turns, model.Turn{
				Key: identityKey("dsh", sessionID, attemptKey), SessionID: sessionID, Agent: model.Agent("dsh"),
				Timestamp: timestamp, Model: modelID, Provider: "dsh", Project: header.CWD,
				Usage: usage, UnpricedReason: unpriced,
			})
		}
	}
	return turns, nil
}

func dshUsageFromStream(stream []dshStreamRecord) *dshUsage {
	for i := len(stream) - 1; i >= 0; i-- {
		if stream[i].Type == "chunk" && stream[i].Chunk != nil && stream[i].Chunk.Type == "usage" {
			return stream[i].Chunk.Usage
		}
	}
	return nil
}
func dshCoordinates(key string) (int64, int64) {
	parts := strings.Split(key, ":")
	a, _ := strconv.ParseInt(parts[0], 10, 64)
	b, _ := strconv.ParseInt(parts[1], 10, 64)
	return a, b
}
func dshTime(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	if value < 1_000_000_000_000 {
		value *= 1000
	}
	t := time.UnixMilli(value).UTC()
	if t.UnixMilli() < 1_000_000_000_000 {
		return time.Time{}
	}
	return t
}
func normalizeDSHUsage(raw dshUsage) (model.Usage, bool, bool) {
	value := func(p *int64) int64 {
		if p == nil || *p < 0 {
			return 0
		}
		return *p
	}
	u := model.Usage{Input: value(raw.Input), Output: value(raw.Output), CacheRead: value(raw.CacheRead), CacheWrite: value(raw.CacheWrite), Reasoning: value(raw.Reasoning)}
	complete := raw.Input != nil && raw.Output != nil && *raw.Input >= 0 && *raw.Output >= 0
	for _, p := range []*int64{raw.CacheRead, raw.CacheWrite, raw.Reasoning} {
		if p != nil && *p < 0 {
			complete = false
		}
	}
	if u.Reasoning > u.Output {
		u.Reasoning = u.Output
		complete = false
	}
	u, ok := u.Sanitize()
	return u, complete, ok
}

func readDSHContent(file dshFile) ([]byte, error) {
	info, err := os.Stat(file.path)
	if err != nil {
		return nil, err
	}
	if info.Size() > dshMaxFileBytes {
		return nil, fmt.Errorf("file exceeds %d-byte cap", dshMaxFileBytes)
	}
	raw, err := os.ReadFile(file.path)
	if err != nil || !file.compressed {
		return raw, err
	}
	frames, err := scanDSHZstdFrames(raw)
	if err != nil {
		return nil, err
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(dshMaxFrameBytes), zstd.WithDecodeAllCapLimit(true))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	decoded := make([]byte, 0, min(len(raw)*3, dshMaxFileBytes))
	for _, frame := range frames {
		remaining := dshMaxFileBytes - len(decoded)
		if remaining <= 0 {
			return nil, errors.New("decoded content exceeds limit")
		}
		capForFrame := min(remaining, dshMaxFrameBytes)
		part, err := decoder.DecodeAll(raw[frame[0]:frame[1]], make([]byte, 0, capForFrame))
		if err != nil {
			return nil, err
		}
		if len(part) > capForFrame {
			return nil, errors.New("decoded frame exceeds limit")
		}
		decoded = append(decoded, part...)
	}
	return decoded, nil
}

func splitDSHLines(content []byte) ([][]byte, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 64<<10), dshMaxFileBytes)
	var lines [][]byte
	for scanner.Scan() {
		if line := bytes.TrimSpace(scanner.Bytes()); len(line) > 0 {
			lines = append(lines, bytes.Clone(line))
		}
	}
	return lines, scanner.Err()
}

// scanDSHZstdFrames returns only structurally complete frames. An incomplete
// final frame is an interrupted append and is ignored; invalid complete
// structure rejects the whole file before any usage is emitted.
func scanDSHZstdFrames(data []byte) ([][2]int, error) {
	const magic uint32 = 0xfd2fb528
	var frames [][2]int
	for offset := 0; offset < len(data); {
		start := offset
		if len(data)-offset < 4 {
			break
		}
		if binary.LittleEndian.Uint32(data[offset:]) != magic {
			return nil, fmt.Errorf("invalid zstd magic at byte %d", offset)
		}
		offset += 4
		if offset == len(data) {
			break
		}
		descriptor := data[offset]
		offset++
		if descriptor&24 != 0 {
			return nil, fmt.Errorf("reserved zstd header bit at byte %d", offset-1)
		}
		contentSizeFlag := descriptor >> 6
		singleSegment, checksum := descriptor&32 != 0, descriptor&4 != 0
		dictionaryFlag := int(descriptor & 3)
		dictionaryBytes := dictionaryFlag
		if dictionaryFlag == 3 {
			dictionaryBytes = 4
		}
		contentSizeBytes := 0
		if contentSizeFlag == 0 {
			if singleSegment {
				contentSizeBytes = 1
			}
		} else {
			contentSizeBytes = 1 << contentSizeFlag
		}
		headerBytes := dictionaryBytes + contentSizeBytes
		if !singleSegment {
			headerBytes++
		}
		if len(data)-offset < headerBytes {
			break
		}
		offset += headerBytes
		complete := false
		for {
			if len(data)-offset < 3 {
				break
			}
			block := int(data[offset]) | int(data[offset+1])<<8 | int(data[offset+2])<<16
			offset += 3
			last, blockType, blockSize := block&1 != 0, (block>>1)&3, block>>3
			if blockType == 3 {
				return nil, fmt.Errorf("reserved zstd block type at byte %d", offset-3)
			}
			payload := blockSize
			if blockType == 1 {
				payload = 1
			}
			if len(data)-offset < payload {
				break
			}
			offset += payload
			if last {
				complete = true
				break
			}
		}
		if !complete {
			break
		}
		if checksum {
			if len(data)-offset < 4 {
				break
			}
			offset += 4
		}
		frames = append(frames, [2]int{start, offset})
	}
	return frames, nil
}
