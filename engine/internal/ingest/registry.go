package ingest

import (
	"os"
	"sort"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
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

type providerStatus string

const (
	providerLocal         providerStatus = "local"
	providerImport        providerStatus = "import"
	providerCredits       providerStatus = "credits"
	providerSourceLimited providerStatus = "source-limited"
)

type providerEntry struct {
	agent      model.Agent
	newScanner func() Scanner
	coverage   string
	status     providerStatus
	statusFor  func(Scanner) providerStatus
}

func scannerProvider[T Scanner](agent model.Agent, newScanner func() T, coverage string, status providerStatus) providerEntry {
	return providerEntry{
		agent: agent,
		newScanner: func() Scanner {
			return newScanner()
		},
		coverage: coverage,
		status:   status,
	}
}

func sourceLimitedProvider(agent model.Agent, coverage string) providerEntry {
	return providerEntry{agent: agent, coverage: coverage, status: providerSourceLimited}
}

func importedAntigravity(Scanner) providerStatus {
	if os.Getenv("TT_ANTIGRAVITY_DIR") != "" {
		return providerImport
	}
	return providerLocal
}

func importedCursor(scanner Scanner) providerStatus {
	if cursor, ok := scanner.(*Cursor); ok && cursor.path != "" {
		return providerImport
	}
	return providerLocal
}

func importedCursorAgent(scanner Scanner) providerStatus {
	if cursor, ok := scanner.(*CursorAgent); ok && cursor.root != "" {
		return providerImport
	}
	return providerLocal
}

// providers is ordered for scan deduplication. Catalog sorts its independent
// view by agent name, but All must preserve this order because duplicate calls
// retain the first scanner's attribution.
var providers = []providerEntry{
	scannerProvider(model.AgentClaude, NewClaude, "", providerLocal),
	scannerProvider(model.AgentCodex, NewCodex, "", providerLocal),
	scannerProvider(model.AgentHermes, NewHermes, "", providerLocal),
	scannerProvider(model.AgentOpenCode, NewOpenCode, "", providerLocal),
	scannerProvider(model.AgentGemini, NewGemini, "", providerLocal),
	scannerProvider(model.AgentPi, NewPi, "", providerLocal),
	scannerProvider(model.AgentCline, NewCline, "Legacy IDE task requests; historical model attribution unavailable", providerLocal),
	scannerProvider(model.Agent("cline-cli"), NewClineCLI, "Native v1 session messages and aggregate fallback", providerLocal),
	scannerProvider(model.AgentCopilot, NewCopilot, "Native CLI accounting and measured VS Code chat totals; optional OTel file fallback", providerLocal),
	scannerProvider(model.Agent("kilo-code"), NewKilo, "", providerLocal),
	scannerProvider(model.Agent("roo-code"), NewRooCode, "Legacy IDE task requests; historical model attribution unavailable", providerLocal),
	scannerProvider(model.Agent("mux"), NewMux, "Assistant step aggregates and separately billed tool models", providerLocal),
	scannerProvider(model.Agent("zerostack"), NewZerostack, "Session totals using final protocol; historical model attribution unavailable", providerLocal),
	scannerProvider(model.Agent("quickdesk"), NewQuickdesk, "Legacy local EMF metrics; unpriced; no current cloud history", providerLocal),
	{agent: model.AgentAntigravity, newScanner: func() Scanner { return NewAntigravity() }, coverage: "Native CLI/IDE SQLite invocation counters; optional TT_ANTIGRAVITY_DIR stream-json replacement", status: providerLocal, statusFor: importedAntigravity},
	scannerProvider(model.Agent("vercel-gateway"), NewVercelGateway, "TT_VERCEL_REPORT: single report snapshot; unpriced; may overlap native logs", providerImport),
	{agent: model.Agent("cursor-agent"), newScanner: func() Scanner { return NewCursorAgent() }, coverage: "Automatic local SDK store discovery; optional TT_CURSOR_AGENT_DIR saved results; no historical CLI reconstruction", status: providerLocal, statusFor: importedCursorAgent},
	scannerProvider(model.AgentQwen, NewQwen, "", providerLocal),
	scannerProvider(model.Agent("kimi"), NewKimi, "", providerLocal),
	scannerProvider(model.Agent("kimicode"), NewKimiCode, "", providerLocal),
	scannerProvider(model.Agent("mistral-vibe"), NewVibe, "Session totals; historical model attribution unavailable", providerLocal),
	scannerProvider(model.Agent("zcode"), NewZCode, "Observed local model_usage SQLite schema", providerLocal),
	scannerProvider(model.Agent("forge"), NewForge, "Actual persisted usage only; conversation timestamp is approximate", providerLocal),
	scannerProvider(model.Agent("goose"), NewGoose, "Session totals; historical model attribution unavailable", providerLocal),
	scannerProvider(model.Agent("zed"), NewZed, "Request/session aggregates; historical model attribution unavailable", providerLocal),
	scannerProvider(model.Agent("openclaude"), NewOpenClaude, "", providerLocal),
	scannerProvider(model.Agent("openclaw"), NewOpenClaw, "Active SQLite transcripts and JSONL; compressed cold archives excluded", providerLocal),
	scannerProvider(model.Agent("omp"), NewOMP, "", providerLocal),
	scannerProvider(model.Agent("droid"), NewDroid, "Session totals; historical model attribution unavailable", providerLocal),
	scannerProvider(model.Agent("ibm-bob"), NewIBMBob, "Observed IDE task metrics; unpriced", providerLocal),
	scannerProvider(model.Agent("kiro"), NewKiro, "CLI credits only; no tokens, USD conversion, or IDE history", providerCredits),
	{agent: model.AgentCursor, newScanner: func() Scanner { return NewCursor() }, coverage: "Automatic IDE SQLite discovery; best-effort counters, gaps disclosed; TT_CURSOR_CSV replaces local history", status: providerLocal, statusFor: importedCursor},
	scannerProvider(model.AgentGrok, NewGrok, "", providerLocal),
	scannerProvider(model.Agent("codewhale"), NewCodeWhale, "Measured session total only; unclassified and unpriced", providerLocal),
	scannerProvider(model.Agent("codebuff"), NewCodebuff, "Native generation usage or credits; cumulative runState history excluded", providerLocal),
	scannerProvider(model.Agent("devin"), NewDevin, "Local CLI ATIF step metrics; no cloud account usage", providerLocal),
	scannerProvider(model.Agent("open-design"), NewOpenDesign, "", providerLocal),
	scannerProvider(model.Agent("lingtai-tui"), NewLingTai, "", providerLocal),
	scannerProvider(model.Agent("warp"), NewWarp, "Measured conversation/model total only; unclassified and unpriced", providerLocal),
	scannerProvider(model.Agent("dsh"), NewDSH, "", providerLocal),
	sourceLimitedProvider(model.Agent("crush"), "Stored counters describe the latest context, not cumulative usage; no measured history reader"),
	sourceLimitedProvider(model.Agent("grokbot"), "Local transcript mirror has no measured usage; no token estimates imported"),
}

// All returns every usage scanner, whether or not the agent is installed.
func All() []Scanner {
	scanners := make([]Scanner, 0, len(providers))
	for _, provider := range providers {
		if provider.newScanner != nil {
			scanners = append(scanners, provider.newScanner())
		}
	}
	return scanners
}

// Available returns only the selected scanners whose logs exist on this machine.
func Available(agents ...string) []Scanner {
	var out []Scanner
	for _, scanner := range All() {
		if Selected(scanner, agents) && len(scanner.Roots()) > 0 {
			out = append(out, scanner)
		}
	}
	return out
}

// Selected prevents an agent filter from reading every other provider's store.
// Overlapping gateway snapshots require explicit selection even when configured.
func Selected(scanner Scanner, agents []string) bool {
	if len(agents) == 0 {
		if explicit, ok := scanner.(interface{ ExplicitOnly() bool }); ok && explicit.ExplicitOnly() {
			return false
		}
		return true
	}
	for _, agent := range agents {
		if strings.EqualFold(string(scanner.Agent()), agent) {
			return true
		}
	}
	return false
}

// KnownAgent reports whether id names a scanner or audited source-limited adapter.
// It only checks registry metadata, so command validation never discovers logs.
func KnownAgent(id string) bool {
	for _, provider := range providers {
		if strings.EqualFold(string(provider.agent), id) {
			return true
		}
	}
	return false
}

// Catalog includes audited source-limited adapters alongside local scanners.
func Catalog() []ProviderInfo {
	rows := make([]ProviderInfo, 0, len(providers))
	for _, provider := range providers {
		row := ProviderInfo{Agent: string(provider.agent), Status: string(provider.status), Coverage: provider.coverage}
		if provider.newScanner != nil {
			scanner := provider.newScanner()
			roots := scanner.Roots()
			row.Installed = len(roots) > 0
			row.Roots = roots
			if provider.statusFor != nil {
				row.Status = string(provider.statusFor(scanner))
			}
			if explicit, ok := scanner.(interface{ ExplicitOnly() bool }); ok {
				row.ExplicitOnly = explicit.ExplicitOnly()
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	return rows
}

// SourceLimitation explains why an audited adapter is not a usage scanner.
func SourceLimitation(id string) string {
	for _, provider := range providers {
		if provider.status == providerSourceLimited && strings.EqualFold(string(provider.agent), id) {
			return provider.coverage
		}
	}
	return ""
}
