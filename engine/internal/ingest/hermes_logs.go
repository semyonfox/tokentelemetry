package ingest

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/model"
)

type hermesAggregate struct {
	task    string
	session string
	calls   int64
	turn    model.Turn
}

type hermesLogCall struct {
	session, model, provider, key string
	timestamp                     time.Time
	usage                         model.Usage
}

// Hermes's normal INFO format carries local wall-clock time and session context.
// These logs contain gross prompt counts, including both cache buckets.
var hermesCallLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2},\d{3}) INFO \[([^\]]+)\] agent\.(?:conversation_loop|turn_usage): API call #(\d+): model=(\S+) provider=(\S+) in=(\d+) out=(\d+) total=(\d+) latency=[0-9.]+s(.*)$`)
var hermesCache = regexp.MustCompile(`(?:^| )cache=(\d+)/(\d+)(?: |$)`)
var hermesWrite = regexp.MustCompile(`(?:^| )write=(\d+)(?: |$)`)
var hermesResponseID = regexp.MustCompile(`(?:^| )id=(\S+)`)

func parseHermesCall(line string) (hermesLogCall, bool) {
	m := hermesCallLine.FindStringSubmatch(line)
	if m == nil {
		return hermesLogCall{}, false
	}
	ts, err := time.ParseInLocation("2006-01-02 15:04:05,000", m[1], time.Local)
	if err != nil {
		return hermesLogCall{}, false
	}
	parse := func(s string) (int64, bool) {
		n, e := strconv.ParseInt(s, 10, 64)
		return n, e == nil && n >= 0 && n <= model.MaxPerCallTokens
	}
	in, ok1 := parse(m[6])
	out, ok2 := parse(m[7])
	total, err := strconv.ParseInt(m[8], 10, 64)
	if !ok1 || !ok2 || err != nil || total != in+out {
		return hermesLogCall{}, false
	}
	var read, write int64
	if v := hermesCache.FindStringSubmatch(m[9]); v != nil {
		var ok bool
		read, ok = parse(v[1])
		den, ok2 := parse(v[2])
		if !ok || !ok2 || den != in {
			return hermesLogCall{}, false
		}
	} else if strings.Contains(m[9], "cache=") {
		return hermesLogCall{}, false
	}
	if v := hermesWrite.FindStringSubmatch(m[9]); v != nil {
		var ok bool
		write, ok = parse(v[1])
		if !ok {
			return hermesLogCall{}, false
		}
	} else if strings.Contains(m[9], "write=") {
		return hermesLogCall{}, false
	}
	if read+write > in {
		return hermesLogCall{}, false
	}
	key := identityKey("log-line", m[2], m[1], m[3])
	if id := hermesResponseID.FindStringSubmatch(m[9]); id != nil {
		key = identityKey("response", m[2], id[1])
	}
	return hermesLogCall{session: m[2], model: m[4], provider: m[5], key: key, timestamp: ts, usage: model.Usage{
		Input: in - read - write, Output: out, CacheRead: read, CacheWrite: write, ContextTokens: in,
	}}, true
}

func hermesLogGroup(session, modelID, provider string) string {
	return identityKey("group", session, modelID, provider)
}

// Reconciliation is deliberately all-or-nothing per unambiguous route/task row.
// Rotated logs are not extra spend: incomplete/conflicting groups retain the DB
// aggregate. Matching all four buckets (and call count, when recorded) prevents
// older lines missing cache-write detail from producing false precise totals.
func reconcileHermesLogs(ctx context.Context, dbPath string, aggregates []hermesAggregate) ([]model.Turn, error) {
	groups := map[string][]hermesLogCall{}
	invalid := map[string]bool{}
	seen := map[string]hermesLogCall{}
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(dbPath), "logs", "agent.log*"))
	sort.Strings(paths)
	for _, path := range paths {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// RotatingFileHandler uses numeric suffixes; do not ingest exports/backups.
		suffix := strings.TrimPrefix(filepath.Base(path), "agent.log")
		if suffix != "" {
			if !strings.HasPrefix(suffix, ".") {
				continue
			}
			if _, err := strconv.Atoi(suffix[1:]); err != nil {
				continue
			}
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		for raw := range jsonLines(f) {
			if ctx.Err() != nil {
				f.Close()
				return nil, ctx.Err()
			}
			call, ok := parseHermesCall(string(raw))
			if !ok {
				continue
			}
			group := hermesLogGroup(call.session, call.model, call.provider)
			if old, exists := seen[call.key]; exists {
				if old != call {
					invalid[group] = true
					invalid[hermesLogGroup(old.session, old.model, old.provider)] = true
				}
				continue
			}
			seen[call.key] = call
			groups[group] = append(groups[group], call)
		}
		f.Close()
	}
	counts := map[string]int{}
	for _, a := range aggregates {
		counts[hermesLogGroup(a.session, a.turn.Model, a.turn.Provider)]++
	}
	var turns []model.Turn
	for _, a := range aggregates {
		t := a.turn
		group := hermesLogGroup(a.session, t.Model, t.Provider)
		calls := groups[group]
		var total model.Usage
		for _, c := range calls {
			total.Add(c.usage)
		}
		match := a.task == "" && len(calls) > 0 && !invalid[group] && counts[group] == 1 &&
			(a.calls == 0 || int64(len(calls)) == a.calls) &&
			total.Input == t.Usage.Input && total.Output == t.Usage.Output &&
			total.CacheRead == t.Usage.CacheRead && total.CacheWrite == t.Usage.CacheWrite
		if !match {
			turns = append(turns, t)
			continue
		}
		for _, c := range calls {
			call := t
			call.Key = identityKey("hermes-call", dbPath, c.key)
			call.Timestamp = c.timestamp
			call.Usage = c.usage
			call.Aggregate = false
			turns = append(turns, call)
		}
		// Reasoning is a subset of output, not an extra charge. Logs do not expose
		// its per-call split; retain that metadata without assigning it to a call.
		if t.Usage.Reasoning > 0 {
			t.Key += "|reasoning"
			t.Usage = model.Usage{Reasoning: t.Usage.Reasoning}
			turns = append(turns, t)
		}
	}
	return turns, nil
}
