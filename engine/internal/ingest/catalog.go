package ingest

import (
	"os"
	"sort"
	"strings"
)

// ProviderInfo describes coverage separately from whether a local source exists.
type ProviderInfo struct {
	Agent        string   `json:"agent"`
	Installed    bool     `json:"installed"`
	Roots        []string `json:"roots,omitempty"`
	Status       string   `json:"status"`
	Coverage     string   `json:"coverage,omitempty"`
	ExplicitOnly bool     `json:"explicit_only,omitempty"`
}

var sourceLimits = map[string]string{
	"crush":   "Stored counters describe the latest context, not cumulative usage; no measured history reader",
	"grokbot": "Local transcript mirror has no measured usage; no token estimates imported",
}

var coverageNotes = map[string]string{
	"antigravity":    "Native CLI/IDE SQLite invocation counters; optional TT_ANTIGRAVITY_DIR stream-json replacement",
	"cline":          "Legacy IDE task requests; historical model attribution unavailable",
	"cline-cli":      "Native v1 session messages and aggregate fallback",
	"codebuff":       "Native generation usage or credits; cumulative runState history excluded",
	"codewhale":      "Measured session total only; unclassified and unpriced",
	"copilot":        "Native CLI accounting and measured VS Code chat totals; optional OTel file fallback",
	"cursor":         "Automatic IDE SQLite discovery; best-effort counters, gaps disclosed; TT_CURSOR_CSV replaces local history",
	"cursor-agent":   "Automatic local SDK store discovery; optional TT_CURSOR_AGENT_DIR saved results; no historical CLI reconstruction",
	"devin":          "Local CLI ATIF step metrics; no cloud account usage",
	"droid":          "Session totals; historical model attribution unavailable",
	"forge":          "Actual persisted usage only; conversation timestamp is approximate",
	"goose":          "Session totals; historical model attribution unavailable",
	"ibm-bob":        "Observed IDE task metrics; unpriced",
	"kiro":           "CLI credits only; no tokens, USD conversion, or IDE history",
	"mistral-vibe":   "Session totals; historical model attribution unavailable",
	"mux":            "Assistant step aggregates and separately billed tool models",
	"openclaw":       "Active SQLite transcripts and JSONL; compressed cold archives excluded",
	"quickdesk":      "Legacy local EMF metrics; unpriced; no current cloud history",
	"roo-code":       "Legacy IDE task requests; historical model attribution unavailable",
	"vercel-gateway": "TT_VERCEL_REPORT: single report snapshot; unpriced; may overlap native logs",
	"warp":           "Measured conversation/model total only; unclassified and unpriced",
	"zcode":          "Observed local model_usage SQLite schema",
	"zed":            "Request/session aggregates; historical model attribution unavailable",
	"zerostack":      "Session totals using final protocol; historical model attribution unavailable",
}

// Catalog includes the two audited sources that cannot supply measured history.
// They are not registered as functioning scanners in All.
func Catalog() []ProviderInfo {
	var rows []ProviderInfo
	for _, scanner := range All() {
		id := string(scanner.Agent())
		roots := scanner.Roots()
		row := ProviderInfo{Agent: id, Installed: len(roots) > 0, Roots: roots, Status: "local", Coverage: coverageNotes[id]}
		switch id {
		case "vercel-gateway":
			row.Status = "import"
		case "antigravity":
			if os.Getenv("TT_ANTIGRAVITY_DIR") != "" {
				row.Status = "import"
			}
		case "cursor":
			if cursor, ok := scanner.(*Cursor); ok && cursor.path != "" {
				row.Status = "import"
			}
		case "cursor-agent":
			if cursor, ok := scanner.(*CursorAgent); ok && cursor.root != "" {
				row.Status = "import"
			}
		case "kiro":
			row.Status = "credits"
		}
		if e, ok := scanner.(interface{ ExplicitOnly() bool }); ok {
			row.ExplicitOnly = e.ExplicitOnly()
		}
		rows = append(rows, row)
	}
	for id, reason := range sourceLimits {
		rows = append(rows, ProviderInfo{Agent: id, Status: "source-limited", Coverage: reason})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	return rows
}

// SourceLimitation explains why an audited adapter is not a usage scanner.
func SourceLimitation(id string) string { return sourceLimits[strings.ToLower(id)] }
