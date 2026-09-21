package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

const (
	scanCacheVersion = 1
	maxScanCacheSize = 256 << 20
)

type scanCacheRecord struct {
	Version     int          `json:"version"`
	Namespace   string       `json:"namespace"`
	Fingerprint string       `json:"fingerprint"`
	Turns       []model.Turn `json:"turns"`
}

type fileCacheContext struct {
	dir       string
	namespace string
	agent     model.Agent
}

type fileCacheContextKey struct{}

// scannerCacheInputs is intentionally private. Production scanners are
// whitelisted below; synthetic scanners use it to exercise cache invariants.
type scannerCacheInputs interface {
	cacheInputs() []string
}

var errNestedCacheSymlink = errors.New("nested source symlink")

func cacheInputsForScanner(scanner Scanner) ([]string, bool) {
	if source, ok := scanner.(scannerCacheInputs); ok {
		return source.cacheInputs(), true
	}
	inputs := append([]string(nil), scanner.Roots()...)
	switch s := scanner.(type) {
	case *Claude, *Codex, *Antigravity, *Cursor, *CursorAgent, *OpenCode:
		return inputs, true
	case *Hermes:
		for _, db := range s.Roots() {
			inputs = append(inputs, filepath.Join(filepath.Dir(db), "logs"))
		}
		return inputs, true
	case *Gemini:
		inputs = append(inputs, filepath.Join(s.root, "projects.json"))
		return inputs, true
	default:
		// OpenClaw is deliberately absent: persisted bindings have expiries, so
		// its result can change while every source file remains byte-identical.
		return nil, false
	}
}

func sameCacheInputs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	for i := range a {
		a[i], b[i] = filepath.Clean(a[i]), filepath.Clean(b[i])
	}
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func cacheNamespace(scanner Scanner) string {
	h := sha256.New()
	io.WriteString(h, "tokentelemetry-scan-cache-v1\n")
	io.WriteString(h, runtime.Version()+"\n")
	io.WriteString(h, fmt.Sprintf("%T\n", scanner))
	io.WriteString(h, string(scanner.Agent())+"\n")
	io.WriteString(h, "mode:"+scannerCacheMode(scanner)+"\n")
	if exe, err := os.Executable(); err == nil {
		if info, err := os.Stat(exe); err == nil {
			fmt.Fprintf(h, "exe:%d:%d:%o\n", info.Size(), info.ModTime().UnixNano(), info.Mode())
		}
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		io.WriteString(h, info.Main.Path+"@"+info.Main.Version+"\n")
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision", "vcs.modified", "vcs.time":
				io.WriteString(h, setting.Key+"="+setting.Value+"\n")
			}
		}
	}
	zone, offset := time.Now().Zone()
	fmt.Fprintf(h, "tz:%s:%s:%s:%d\n", os.Getenv("TZ"), time.Local.String(), zone, offset)
	return hex.EncodeToString(h.Sum(nil))
}

func scannerCachePath(cacheDir string, scanner Scanner, inputs []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%T\n%s\n%s\n", scanner, scanner.Agent(), scannerCacheMode(scanner))
	for _, input := range inputs {
		io.WriteString(h, filepath.Clean(input)+"\n")
	}
	name := safeCacheName(string(scanner.Agent()))
	return filepath.Join(cacheDir, "providers", name+"-"+hex.EncodeToString(h.Sum(nil)[:12])+".json")
}

func scannerCacheMode(scanner Scanner) string {
	switch s := scanner.(type) {
	case *Antigravity:
		if s.streamRoot != "" {
			return "saved-stream"
		}
		return "native-sqlite"
	case *Cursor:
		if s.dbPath != "" {
			return "native-sqlite"
		}
		return "csv-import"
	case *CursorAgent:
		if s.root != "" {
			return "saved-sdk-results"
		}
		return "native-sdk-sqlite"
	default:
		return "native"
	}
}

func safeCacheName(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "provider"
	}
	return b.String()
}

func sourceFingerprint(inputs []string) (string, bool, error) {
	inputs = append([]string(nil), inputs...)
	sort.Strings(inputs)
	h := sha256.New()
	for _, input := range inputs {
		if err := fingerprintPath(h, filepath.Clean(input), filepath.Clean(input), true); err != nil {
			if errors.Is(err, errNestedCacheSymlink) {
				return "", false, nil
			}
			return "", false, err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), true, nil
}

func fingerprintPath(h io.Writer, logical, physical string, root bool) error {
	info, err := os.Lstat(physical)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(h, "missing\x00%s\n", logical)
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(physical)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "link\x00%s\x00%s\x00%d\x00%d\x00%o\n", logical, target, info.Size(), info.ModTime().UnixNano(), info.Mode())
		if !root {
			// Scanner behavior differs here (Codex opens matching symlink files,
			// while most WalkDir scanners skip them), so bypass provider caching.
			return errNestedCacheSymlink
		}
		resolved, err := filepath.EvalSymlinks(physical)
		if err != nil {
			return err
		}
		return fingerprintPath(h, logical+"->"+resolved, resolved, true)
	}
	if info.IsDir() {
		// Directory size and mtime are filesystem bookkeeping. Membership and
		// every descendant's metadata below are the stable data fingerprint;
		// ignoring the directory clock also prevents SQLite -shm churn from
		// causing perpetual misses.
		fmt.Fprintf(h, "dir\x00%s\x00%o\n", logical, info.Mode())
	} else {
		fmt.Fprintf(h, "node\x00%s\x00%d\x00%d\x00%o\n", logical, info.Size(), info.ModTime().UnixNano(), info.Mode())
	}
	if !info.IsDir() {
		if root && info.Mode().IsRegular() && sqliteLike(physical) {
			for _, suffix := range []string{"-wal", "-journal"} {
				if err := fingerprintPath(h, logical+suffix, physical+suffix, false); err != nil {
					return err
				}
			}
		}
		return nil
	}
	entries, err := os.ReadDir(physical)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "-shm") {
			continue
		}
		if err := fingerprintPath(h, filepath.Join(logical, entry.Name()), filepath.Join(physical, entry.Name()), false); err != nil {
			return err
		}
	}
	return nil
}

func sqliteLike(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".sqlite") || strings.HasSuffix(name, ".vscdb")
}

func loadScanCache(path, namespace, fingerprint string) ([]model.Turn, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxScanCacheSize {
		return nil, false
	}
	var record scanCacheRecord
	dec := json.NewDecoder(io.LimitReader(f, maxScanCacheSize+1))
	if dec.Decode(&record) != nil || dec.Decode(&struct{}{}) != io.EOF ||
		record.Version != scanCacheVersion || record.Namespace != namespace || record.Fingerprint != fingerprint {
		return nil, false
	}
	return record.Turns, true
}

func writeScanCache(path, namespace, fingerprint string, turns []model.Turn) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	record := scanCacheRecord{Version: scanCacheVersion, Namespace: namespace, Fingerprint: fingerprint, Turns: turns}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > maxScanCacheSize {
		if err != nil {
			return err
		}
		return errors.New("scan cache record exceeds size bound")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".scan-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func sanitizeCachedTurns(turns []model.Turn) ([]model.Turn, bool) {
	out := append([]model.Turn(nil), turns...)
	for i := range out {
		if out[i].Endpoint == "" {
			continue
		}
		u, err := url.Parse(out[i].Endpoint)
		if err != nil || u.Scheme == "" || u.Host == "" || u.Hostname() == "" || u.User != nil {
			return nil, false
		}
		scheme := strings.ToLower(u.Scheme)
		if scheme != "http" && scheme != "https" {
			return nil, false
		}
		out[i].Endpoint = (&url.URL{Scheme: scheme, Host: strings.ToLower(u.Host)}).String()
	}
	return out, true
}

func withFileCache(ctx context.Context, cacheDir, namespace string, agent model.Agent) context.Context {
	return context.WithValue(ctx, fileCacheContextKey{}, fileCacheContext{dir: cacheDir, namespace: namespace, agent: agent})
}

func cachedParsedFile(ctx context.Context, path, parser string, parse func() []model.Turn) []model.Turn {
	cache, ok := ctx.Value(fileCacheContextKey{}).(fileCacheContext)
	if !ok || cache.dir == "" {
		return parse()
	}
	inputs := []string{path}
	before, stable, err := sourceFingerprint(inputs)
	if err != nil || !stable {
		return parse()
	}
	h := sha256.Sum256([]byte(string(cache.agent) + "\x00" + parser + "\x00" + filepath.Clean(path)))
	cachePath := filepath.Join(cache.dir, "files", safeCacheName(string(cache.agent))+"-"+hex.EncodeToString(h[:12])+".json")
	namespace := cache.namespace + ":" + parser
	if turns, hit := loadScanCache(cachePath, namespace, before); hit {
		after, afterStable, statErr := sourceFingerprint(inputs)
		if statErr == nil && afterStable && after == before {
			return turns
		}
	}
	turns := parse()
	after, afterStable, statErr := sourceFingerprint(inputs)
	if statErr == nil && afterStable && after == before {
		if safe, ok := sanitizeCachedTurns(turns); ok {
			_ = writeScanCache(cachePath, namespace, before, safe)
		}
	}
	return turns
}
