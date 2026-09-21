# Provider ports and CLI feature comparison

Research snapshot: 19 September 2026. The comparison below describes the pre-port baseline.
Implementation now includes 40 readers/importers and two explicit source limitations.
The target is all 42 adapters in the inspected upstream registry, with honest
coverage limits. See [the complete provider list](provider-inventory.md) for
prioritization and implementation status.

20 September follow-up: Cursor now discovers its IDE SQLite database
automatically and reads populated token counters with explicit completeness and
cache-split limits. A configured CSV replaces that history. The earlier import
recommendation below remains useful for records the IDE never persisted; see
[current Cursor evidence](provider-evidence-next-cli.md#cursor-local-database).

21 September integration: Antigravity now uses native CLI/IDE SQLite history
by default, with optional stream-json captures replacing that source. Copilot
retains native CLI, measured VS Code and optional OpenTelemetry support.
Current coverage is recorded in the inventory above.

Graph work is deferred at the user's request. The chart and terminal-library
comparison below records potential options only; it is not part of the current
implementation scope. Analysis commands and desktop/MCP integrations are also
separate from the provider-reader work.

Keep TokenTelemetry's accounting engine and add coverage in small batches. Start
with Cline and Copilot CLI, then current Kilo. Treat Cursor as a separate import
problem because its local conversation store does not reliably record billable
usage. Revisit graphs and evidence-based analysis after provider coverage, without
importing CodeBurn's entire optimization system.

The ccdeck fork is in `semyonfox/ai-analysis/ccdeck`. This review inspected
[ai-analysis at b9726d0](https://github.com/semyonfox/ai-analysis/tree/b9726d0fd189caabb22ce0923962d5f7c11980fa),
whose ccdeck package is private version 0.1.0, and upstream
[CodeBurn 0.9.24, commit 4cf1885](https://github.com/getagentseal/codeburn/tree/4cf18855939512605826cf6b0a47b6486b0c8619).
TokenTelemetry's baseline is commit `3e9d5882b7153cdee1da1c9479813ca6df38a127`.

The earlier comparison needs updating. The fork registers 26 adapters, not 28.
CodeBurn's checked registry contains
42 adapters, comprising 31 eagerly loaded and 11 lazily loaded adapters. Its
website says 41 and its README says 37. These are adapters, including product
variants and a gateway, not 42 equally complete sources of usage. Current
upstream also has a browser dashboard, Windows tray support, device sharing,
quota queries, and optional desktop telemetry. The fork remains a terminal
application with MCP and native tray integrations. Sources:
[fork registry](https://github.com/semyonfox/ai-analysis/blob/b9726d0fd189caabb22ce0923962d5f7c11980fa/ccdeck/src/providers/index.ts),
[upstream registry](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/index.ts),
[web dashboard](https://codeburn.app/docs/web),
[upstream README](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/README.md).

| Product at the checked revision | Reader coverage | Interface and extras |
| --- | --- | --- |
| TokenTelemetry baseline | Six scanners, including Hermes | Go binary; print-once reports and JSON; dated list-rate accounting. |
| ccdeck fork | 26 adapters; includes five of TokenTelemetry's six, but no Hermes reader | Commander/Ink terminal dashboard; MCP; macOS/GNOME integration; Antigravity hook; personal archive tooling. |
| Current CodeBurn | 42 adapters; every fork adapter has a counterpart | Extends the terminal dashboard and analysis; adds the web/device/desktop features described above. |

The fork has no provider absent from current upstream. Its own additions matter
mainly for discovering and organizing collected local histories. Its charts,
classifier and optimizer are an older snapshot, so use the fork to identify
needed behavior and current provider source to implement it.

Preserve these assets in `ai-analysis` independently of the CLI's future:

| Asset | Useful behavior | Porting decision |
| --- | --- | --- |
| `scripts/collect-ai-sessions.ps1` | Explicit local/SSH collection, hash deduplication and manifests; carries SQLite sidecars | Keep as opt-in archive tooling. It handles raw transcripts/configuration and should not run during ordinary usage scans. |
| `scripts/organize-sessions-by-project.ps1` | Resolves project identity and builds a hardlinked project view with a manifest | Preserve path-resolution cases as tests; avoid a second raw-transcript store in the normal CLI. |
| `scripts/build-paxel-input.ps1` | Converts archive manifests to normalized input trees and retains sidecars | Keep as an explicit import/migration helper until its workflow has a replacement. |
| `ccdeck/ccdeck.ps1` | Windows launcher and collected Claude root selection | Keep the discovery behavior; native packaging replaces Node bootstrapping. Do not copy personal paths. |
| `ccdeck/src/mcp/server.ts` | Accepts an `all_time` alias | Preserve the alias if a future MCP server needs compatibility. |
| Archive exclusions in `.gitignore` | Keeps collected session payloads out of the repository | Preserve the exclusion when retaining collection scripts. |

Sources: [collection helpers](https://github.com/semyonfox/ai-analysis/tree/b9726d0fd189caabb22ce0923962d5f7c11980fa/scripts),
[fork launcher](https://github.com/semyonfox/ai-analysis/blob/b9726d0fd189caabb22ce0923962d5f7c11980fa/ccdeck/ccdeck.ps1),
[fork MCP server](https://github.com/semyonfox/ai-analysis/blob/b9726d0fd189caabb22ce0923962d5f7c11980fa/ccdeck/src/mcp/server.ts).
Keeping SQLite sidecars is necessary for some captured databases, but copying
live DB/WAL files sequentially does not establish a consistent snapshot. Any
future importer needs a consistent capture contract and validation. This review
did not execute these scripts or inspect captured payloads.

| Area | TokenTelemetry baseline | What to take from CodeBurn |
| --- | --- | --- |
| Accounting | Six registered scanners, centralized deduplication, disjoint token buckets, dated pricing, context/cache rates, explicit aggregate caveats | Reader discovery and format knowledge. Preserve TokenTelemetry's normalization and reconciliation rules. |
| Meaning of cost | API list value plus separately configured plan costs; neither is an invoice | Keep provider-reported charges and credits separate when adding imports. Do not combine them with list value. |
| Provider reach | Claude Code, Codex, Gemini CLI, OpenCode, Hermes, Pi | Expand by useful source contracts, with IDE and CLI coverage stated separately. |
| Terminal reports | Prints once; daily, model, agent and project tables; handmade bars; JSON | Better dimensions, labels and optional compact trends. Interactive navigation can wait. |
| Task classification | No prompt or tool activity classifier | Optional tool-based categories later, with unknown/mixed categories and an explicit heuristic label. |
| Optimize | No advisory analysis | Measured outliers, cache composition and tool activity first. Treat configuration advice as a separate, opt-in scan. |
| Model comparison | Per-model usage and list value | Side-by-side cost and volume summaries. Avoid calling edits or one-shot rates model quality. |
| MCP and shell breakdowns | Accounting turns do not contain this activity | Useful after a minimal activity schema exists. Call counts are stronger evidence than attributed dollars. |
| MCP server | CLI already emits JSON | Redacted aggregate output is useful sooner. Add a read-only stdio server only for clients that need it. |
| Tray/statusline | None | A small `status --json` contract before native applications. Native menus are separate projects. |
| Web/device sharing | Deliberately retired or absent | Outside this port. They add serving, pairing, retention and deployment work. |

These priorities are a judgment about likely reach and accounting feasibility,
not a measured market-share ranking. Cursor and Copilot deserve attention early,
but popularity cannot make missing counters reliable.

| Order | Reader or import | Proposed scope | Effort and main constraint |
| --- | --- | --- | --- |
| 0 | Coverage diagnostics and source contracts | Make missing, unsupported and partially read sources visible; pin each format to upstream evidence | Small. Avoid a general plugin framework. |
| 1 | Cline | Current SDK/CLI session messages plus legacy extension task usage | Medium. Two storage generations; historical model attribution is not always available. |
| 1 | Copilot CLI | Local per-request usage when supported; reconcile shutdown aggregates without adding them twice | Medium to large. Schema/version changes, crashes, resumes and compaction. |
| 2 | Kilo | Current SQLite store; separate legacy extension JSON support | Medium. Modern Kilo is no longer just the old Cline parser with another directory. |
| 2 | Cursor | Validate official manual usage export first; optional admin API later | Medium discovery spike. Local context meters and transcript text are insufficient. |
| 3 | Roo Code | Historical extension tasks using a narrowly shared task decoder | Small to medium after Cline. Verify current product status and useful demand before expanding. |
| 3 | Kimi CLI, Qwen | Provider-owned persisted usage formats | Small to medium per reader after confirming per-request identities and cache semantics. |
| 3 | Goose, Mistral Vibe | Labelled session aggregates where per-call history is unavailable | Medium. Session totals must not become fabricated per-call history. |
| 3 | Antigravity CLI | Opt-in capture of documented structured output; statusline for live context display | Medium. Future captures do not recover old history; identity and timestamp coverage need validation. |
| Later in the full coverage target | Droid, Zed, OMP/OpenClaw, Crush, other adapters | Verify each format and implement its supported evidence level | Varies. Crush needs further counter-semantics work. Unsupported fields must stay explicit. |

This order was the initial recommendation; the user can reprioritize the complete
42-adapter target. Effort describes relative implementation and fixture work, not a delivery date.
The first useful release can contain Cline and Copilot CLI without waiting for
Cursor's export research or any UI work.

The fork also has an immediately useful discovery improvement. Its
[Claude reader](https://github.com/semyonfox/ai-analysis/blob/b9726d0fd189caabb22ce0923962d5f7c11980fa/ccdeck/src/providers/claude.ts)
accepts multiple roots, checks XDG and legacy locations, normalizes an explicit
`projects` directory, and understands explicitly selected collected-session
directories. TokenTelemetry currently selects one Claude root through
`CLAUDE_CONFIG_DIR` or `~/.claude`. Add explicit multiple-root discovery before
replacing the collection workflow. Preserve global native-request deduplication
across copied archives, without copying machine-specific launcher paths.

Cline, Roo and legacy Kilo store request usage in each task's `ui_messages.json`.
The relevant entry is `type: say`, `say: api_req_started`; its JSON text includes
`tokensIn`, `tokensOut`, `cacheWrites`, `cacheReads` and `cost`. Read the latest
persisted request state, not every streaming update as another call.
`api_conversation_history.json` is primarily conversation context, so it should
not become a second accounting source. Extension roots vary with operating
system, editor flavor and custom storage settings.

Current Cline also has an SDK/CLI session store under
`~/.cline/data/sessions/<id>/<id>.messages.json`. Its assistant messages include
model/provider identity and token metrics. Current Kilo uses an XDG data store
such as `~/.local/share/kilo/kilo.db`, with per-message JSON as well as session
summaries. Read message usage and use session summaries for reconciliation.
Do not bill both. Shared code should cover demonstrated common JSON fields or
directory discovery, while each scanner owns its model, cache and replay rules.

The primary implementations show why a generic Cline-family token mapper would
be wrong:

| Source | Input semantics | Mapping to TokenTelemetry |
| --- | --- | --- |
| Cline canonical v1 `metrics.inputTokens` | Includes cache reads and writes | Subtract both; emit only assistant messages carrying metrics. |
| Cline classic/webview `tokensIn` | Disjoint from cache in the checked translator | Keep as fresh input; verify older generations separately. |
| Roo finalized `tokensIn` | Stores normalized total input including cache | Subtract cache reads and writes. Keep cancelled requests when they contain measured usage. |
| Kilo current `tokens.input` | Already disjoint | Do not subtract cache again. Prefer current `session_message` records over duplicate legacy `message`/`part` data. |

These rules come from Cline's
[v1 contract](https://github.com/cline/cline/blob/93ceab9782e262e4af5b9271a393f29d26f45606/sdk/packages/core/docs/messages-contract-v1.md)
and [translator](https://github.com/cline/cline/blob/93ceab9782e262e4af5b9271a393f29d26f45606/apps/vscode/src/sdk/message-translator.ts),
Roo's [request persistence](https://github.com/RooCodeInc/Roo-Code/blob/b867ec9145750d0ae1ff7f02d35406e9bf2a0b16/src/core/task/Task.ts)
and [cost normalization](https://github.com/RooCodeInc/Roo-Code/blob/b867ec9145750d0ae1ff7f02d35406e9bf2a0b16/src/shared/cost.ts),
and Kilo's [message schema](https://github.com/Kilo-Org/kilocode/blob/010f511d731df2649bb3f9b660e309ea7e869ac8/packages/schema/src/session-message.ts)
and [SQLite schema](https://github.com/Kilo-Org/kilocode/blob/010f511d731df2649bb3f9b660e309ea7e869ac8/packages/core/src/session/sql.ts).

Honor Cline's documented session/data directory overrides and Roo's custom
storage path through an explicit override. Kilo also supports database overrides
and channel-specific files. Prefer schema feature detection for its changing
SQLite generations. Legacy Cline/Roo records may lack per-call model identity;
do not assign a whole mixed-model task to a model found in prompt text. Storage
references: [Cline legacy reader](https://github.com/cline/cline/blob/93ceab9782e262e4af5b9271a393f29d26f45606/apps/vscode/src/sdk/legacy-state-reader.ts),
[Roo storage](https://github.com/RooCodeInc/Roo-Code/blob/b867ec9145750d0ae1ff7f02d35406e9bf2a0b16/src/utils/storage.ts),
[Kilo database selection](https://github.com/Kilo-Org/kilocode/blob/010f511d731df2649bb3f9b660e309ea7e869ac8/packages/core/src/database/database.ts).

Copilot must be split into CLI and IDE support. CLI history can include
`~/.copilot/session-state/<id>/events.jsonl` and, in newer versions,
`~/.copilot/session-store.db`. Per-model shutdown totals are useful fallbacks,
but do not reliably survive every crash and restart. Exact local usage support
exists in recent releases, while the SQLite schema remains an internal contract.
Official OpenTelemetry events are another option for future opt-in collection.
VS Code and JetBrains chat history alone must not be advertised as equivalent
token coverage. AI credits, premium-request multipliers and model list value
are distinct quantities. GitHub's own
[configuration directory reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-config-dir-reference)
documents the stores, while its
[SDK usage contract](https://github.com/github/copilot-sdk/blob/main/docs/features/usage-and-billing.md)
documents per-call usage and billing fields. The local table interpretation still
needs versioned fixtures rather than treating CodeBurn as the schema authority.

For a known `assistant_usage_events` schema, use request rows with model/time,
input/output and cache fields. Probe columns and do not guess missing ones.
Copilot input is cache-inclusive; subtract cache reads and writes. Prefer stable
request identities when available and test database replacement, since an
autoincrement row ID alone is insufficient. Reconcile shutdown totals and request
rows within their proper resume/compaction intervals. Do not independently add
message output, shutdown input and database calls without proving the overlap.

OTel is a useful later file importer, with collection off until explicitly
configured and content capture disabled. Its cache detail fields are included
in gross input under the [OpenTelemetry contract](https://github.com/open-telemetry/semantic-conventions/blob/main/docs/registry/attributes/gen-ai.md).
The checked CodeBurn Copilot IDE OTel path passes gross input plus cache buckets
to pricing; copying that would overcount conforming spans. Normalize at the
reader boundary and deduplicate against any local request records. Configuration
is described in [GitHub's OTel reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#opentelemetry-monitoring).

Cursor's local SQLite data is useful for session discovery and activity. Its
context occupancy meter is a snapshot, not the sum of all requests in a session.
CodeBurn also has text-based estimates and a default-model pricing fallback.
Keep these out of TokenTelemetry's accounting totals. A manual official export
is preferable to introducing background account access. Validate its columns,
timestamps, model identifiers and duplicate behavior before committing to an
importer. If it contains only charges, support charges without inventing tokens.
The documented admin usage API is a later opt-in team feature, with credentials,
aggregation and pagination handled separately from local scanning.

Cursor confirms the [manual usage CSV export](https://forum.cursor.com/t/not-able-to-see-usage-graph-in-my-cursor-website/163322/8)
and documents [team CSV downloads](https://cursor.com/docs/account/teams/analytics).
The exact usage CSV schema is not versioned in the docs. Observed exports use
date/model, separate fresh-input/cache-write/cache-read/output columns, a total,
and a cost/status column. Treat the meaning of ambiguous headers such as
`Input (w/ Cache Write)` as an acceptance check against a known export and its
reported total, not as permission to infer semantics from the label. Preserve
`Included`, `Free`, absent charge and numeric charge distinctly.

Do not claim request count, project or session coverage from a CSV that lacks
those identifiers. Identical rows can represent distinct usage events, so a row
hash alone loses data. Start with explicit, named export snapshots and exact-file
reimport detection. For overlapping exports, replace the selected account/window
snapshot or reconcile complete windows with multiplicity preserved. Handle
provider corrections as replacement, not another append. Cross-source CSV/API
merging stays disabled until its reconciliation contract exists.

The [Cursor Admin API](https://cursor.com/docs/account/teams/admin-api#get-usage-events-data)
distinguishes recorded `chargedCents` from model `totalCents` and provides token
buckets. It requires team-admin credentials and network access. Preserve those
separate values, pagination and the reporting delay. GitHub's
[organization usage reports](https://docs.github.com/en/copilot/reference/copilot-usage-metrics/copilot-usage-metrics)
are also a separate aggregate source; CLI token totals do not imply equivalent
IDE token coverage. Neither API belongs in automatic local discovery.

The existing six readers are maintained coverage, not six adapters to replace.
Compare upstream changes against their fixtures where useful, particularly
new compressed histories, replay identities and subagent formats. Do not weaken
the current Codex or Hermes reconciliation to match another tool's totals.

The remaining reader ideas have different evidence limits:

| Candidate | Evidence and decision |
| --- | --- |
| Antigravity | Official [headless output](https://antigravity.google/docs/cli/headless/) includes step usage and a final total. Prefer terminal step records plus final reconciliation, never their sum. The [statusline contract](https://www.antigravity.google/docs/cli/statusline/) includes current context and cumulative fields, but no universal per-response accounting identity. Repeated identical calls cannot safely be deduplicated by usage values alone. Keep a live context display separate from historical spend. |
| Droid | Current [SDK documentation](https://docs.factory.ai/sdk/typescript) distinguishes per-user-turn results, which can include delegated work, from cumulative session updates. CodeBurn's [local reader](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/droid.ts) instead spreads a session total across assistant messages. Prefer a labelled aggregate for old files, or validated structured captures for newer runs. Do not assume one SDK user turn equals one model request. |
| Warp | [Warp documents credit-based usage](https://docs.warp.dev/support-and-community/plans-and-billing/credits). CodeBurn's [adapter notes](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/docs/providers/warp.md) describe estimated allocation and no reliable per-exchange input/output split. Defer measured-token support. |
| Kiro | [Kiro exposes credit limits](https://kiro.dev/docs/billing/proactive-usage-notifications/). CodeBurn's [adapter notes](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/docs/providers/kiro.md) describe recorded credits alongside text-estimated tokens. A future credit import can be useful; a public overage rate is not proof of what a subscription user paid. |
| Zed | Its [own docs](https://zed.dev/docs/ai/agent-panel) distinguish the built-in agent and external integrations. CodeBurn's [reader](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/zed.ts) reads compressed thread JSON and request counters but uses thread metadata for attribution. Audit Zed's own serializer before a port; preserve unknown historic model/time details. External-agent records may overlap existing readers. |
| OMP and OpenClaw | Promising because of related transcript formats, but sharing ancestry with Pi does not prove counter or fork compatibility. Audit their own serializers before reusing Pi's scanner. |
| Kimi CLI | Its own [usage-format discussion](https://github.com/MoonshotAI/kimi-cli/issues/2394) shows `wire.jsonl` status events with disjoint input/cache/output buckets. Promising next reader. Require historical model identity rather than falling back to today's configuration. |
| Qwen | Its [session documentation](https://github.com/QwenLM/qwen-code/blob/main/docs/users/qwen-serve.md) and [cache documentation](https://github.com/QwenLM/qwen-code/blob/main/docs/users/features/token-caching.md) support a local-reader investigation. Select one authoritative record family across chats, archives and usage telemetry. Normalize cache-inclusive prompt counts. |
| Goose | The [session manager](https://github.com/block/goose/blob/main/crates/goose/src/session/session_manager.rs) stores accumulated usage in SQLite. Start with a labelled aggregate and avoid adding migrated JSONL history twice. Model changes and per-call dates still need source-specific validation. |
| Mistral Vibe | [Upstream](https://github.com/mistralai/mistral-vibe) stores session metadata and messages. The shortlisted metadata totals support aggregate-only coverage; pin and recheck its exact schema before implementation. Do not evenly distribute session totals across messages. |
| Crush | Its [database implementation](https://github.com/charmbracelet/crush/tree/main/internal/db) needs more reconciliation work: persisted token counters and cumulative cost do not necessarily describe the same scope, and cache detail is incomplete. Defer instead of promising measured history. |
| Aider | Default chat/input histories are not a per-call usage ledger. Its opt-in [analytics log](https://github.com/Aider-AI/aider/blob/main/aider/website/assets/sample-analytics.jsonl) is the better investigation target. Separate per-message `cost` from `total_cost`; do not use raw LLM history as an accounting shortcut. This is an additional candidate, not a checked CodeBurn adapter. |
| Windsurf | Common enough to track as a separate research item. No reader found in this CodeBurn registry. That is not evidence that a supported export is impossible. |
| Other registered adapters | Codewhale, Codebuff, Devin, DSH, IBM Bob, Kimi's separate adapter, Lingtai, Mux, OpenClaude, Open Design, QuickDesk, Zerostack, Grok, Grokbot, Forge and ZCode remain demand-led. Their first-party storage contracts were not fully audited in this pass. |
| Vercel AI Gateway | A network billing source, not another local coding agent. Keep any future import separate so calls already captured by an agent are not counted again. |

The reader implementation should extend the current flow:

```text
source discovery -> provider decoder -> source reconciliation
                 -> ingest.Run dedup -> dated pricing -> reports
```

Concrete changes belong in the existing packages:

| File or package | Planned change |
| --- | --- |
| `engine/internal/ingest/ingest.go` | Register scanners. Add structured coverage diagnostics through a small compatible mechanism. Respect `--agent` before doing expensive scans. |
| `engine/internal/ingest/<agent>.go` | Explicit version/shape checks, read-only access, cache normalization, source selection and stable identities. Reuse `modernc.org/sqlite`; no Node helper. |
| `engine/internal/model/types.go` | Add agent identities where missing. Introduce only provenance/unknown-field data that an actual reader requires. Existing enum values alone do not establish support. |
| `engine/internal/cost/classify.go` | Review new routes and mixed billing modes. An agent name alone cannot establish exact subscription coverage. |
| `engine/internal/report/` | Preserve completeness and aggregate caveats. Keep source charges and credits separate if imports require them. |
| `engine/internal/cli/` | Show usable versus detected sources and partial coverage; keep JSON complete and display limits cosmetic. |
| `engine/README.md` | Document tested source versions, supported product variants, paths, evidence quality and gaps. |

Keep `model.Turn` for measured requests and the already-supported unresolved
aggregates. Do not turn absent token fields into measured zero values. If the
first imports contain partial counters or charge-only records, add explicit
field availability or a separate import record rather than forcing them through
`Usage.IsZero`. Add source type/version and an attribution caveat where needed;
there is no need for a generic provider SDK.

Scope identities by agent and source account/store when necessary. Prefer
provider request IDs; never use just token values, timestamps or visible text.
Define authority between local files, databases, captures and exports before
combining them. The current deduper keeps the larger usage snapshot and retains
the original attribution. That is appropriate only for compatible snapshots;
it cannot reconcile arbitrary hourly exports against per-request history.
Unresolved overlapping sources should be alternative views until they can be
matched safely.

Keep source-reported USD, list-rate USD, credits and configured subscription
costs distinct. A source's own `cost` field may itself be an estimate. Store its
basis if retained. Unknown model, timestamp, cache split or billing route stays
unknown. A session total gets an aggregate flag and approximate date attribution,
without invented request count, model mix or context-tier pricing.

Use explicit paths and ordinary documented environment overrides. Do not crawl
credential stores to find settings. Parse only needed keys from provider files,
never print raw errors containing file payloads, and never activate MCP servers
or execute provider code merely to inspect usage. Read live SQLite through
read-only queries with bounded waits and proper WAL visibility.

The analysis port should start much smaller than `optimize`.

| Analysis idea | Recommendation |
| --- | --- |
| Cache and context composition | Add measured fresh input, cache reads/writes, output and reasoning shares. State the denominator and missing fields. This can reuse existing usage data. |
| Expensive sessions | Show outliers within the selected project and period, with sample size. High cost alone is not waste. |
| Tool/MCP activity | Add normalized names and invocation counts, with source coverage. Keep raw arguments, commands and prompts out of exported reports. |
| Per-tool dollars | Only exact with explicit causal accounting data. A model request may use several tools. Any proportional allocation must be labelled and excluded from additive spend totals. |
| Repeated reads and retries | Advisory observations after activity capture exists. Re-reading after an edit or verification step can be correct. |
| Unused MCP servers | Report no observed calls in a window. Infer paid schema overhead only with evidence about loaded/deferred definitions and caching. |
| Task classifier | Optional later. Tool activity first; keyword classification only with an explicit content-analysis choice. Categories remain heuristics, not measures of productivity. |
| Model efficiency/compare | Compare observed volume, list value and measured latency where available. Different tasks, model switching and delegation make quality rankings unjustified. |
| A-F health grade, no-edit waste, automatic fixes | Leave out. Research sessions can be valuable without edits, and arbitrary thresholds do not establish savings. |

CodeBurn's [classifier](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/classifier.ts)
uses tool patterns and prompt keywords. Its
[model-efficiency calculation](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/model-efficiency.ts)
can assign a mixed-model turn's total edit cost to its first non-synthetic model.
Its [context budget](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/context-budget.ts)
uses fixed assumptions such as five tools per server and 400 tokens per tool.
These are useful UI ideas, not measurements to transplant into accounting.
The current [optimizer](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/optimize.ts)
does distinguish measured and estimated findings and considers cache/deferral
behavior. Preserve that distinction and reduce the number of assumptions.

Anthropic now documents deferred MCP tool loading as the normal default, with
provider, proxy and explicit-setting exceptions. An unused configured server
does not automatically incur its full tool schema on every turn. Recommendations
must inspect the relevant mode and observed definitions, and should describe
possible list-value savings separately from cash savings. See
[Claude Code's tool-search contract](https://code.claude.com/docs/en/mcp#scale-with-mcp-tool-search).

## Deferred terminal options

For the terminal, keep the current print-once experience. CodeBurn's
[`HBar` implementation](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/dashboard.tsx)
draws block characters itself. Ink supplies layout, input and rendering lifecycle;
it is not a requirement for these charts.

The fork already offers daily cost/calls, weekday summaries, hourly heatmaps,
top-hour tables and breakdowns by project/model/activity/tool/MCP. These can be
rendered from aggregates without copying its React components. Hour-of-day and
weekday views are optional analysis commands; they add little to the default
spend summary. The checked fork's
[dashboard](https://github.com/semyonfox/ai-analysis/blob/b9726d0fd189caabb22ce0923962d5f7c11980fa/ccdeck/src/dashboard.tsx)
and [time rollups](https://github.com/semyonfox/ai-analysis/blob/b9726d0fd189caabb22ce0923962d5f7c11980fa/ccdeck/src/time-rollups.ts)
are useful behavioral references. TokenTelemetry's existing local-day accounting
should remain authoritative.

| Go option | What it buys | Decision |
| --- | --- | --- |
| Existing renderer | Simple ranked bars and tables, no new dependency | Keep for the next release. Improve cell-width handling, units and layout only when needed. |
| [asciigraph](https://github.com/guptarohit/asciigraph) | Static line charts; its [module](https://github.com/guptarohit/asciigraph/blob/master/go.mod) declares no dependencies | First library to consider if a real line chart would answer a useful question. |
| [Bubble Tea v2](https://charm.land/blog/v2/) with Lip Gloss and selected Bubbles | Stateful keyboard navigation, resize handling, view updates and reusable widgets | Closest practical Go counterpart to Ink, using an Elm-style update loop rather than React. Use for a later explicit `explore` command. |
| [NTCharts v2](https://github.com/NimbleMarkets/ntcharts/tree/v2) | Bars, sparklines, time series, heatmaps and interactive plotting | Consider after choosing a TUI. Its [module](https://github.com/NimbleMarkets/ntcharts/blob/v2/go.mod) brings the Charm stack and further dependencies. Too much scope for today's bars. |
| [PTerm](https://github.com/pterm/pterm) | Broad tables, charts and progress components | Overlaps the working renderer and introduces a broader dependency set. Skip for this port. |
| [tview](https://github.com/rivo/tview) | Widget-oriented full-screen interface | Viable alternative, but no reason to adopt both it and Bubble Tea. |

Dependency count is a maintenance consideration, not a measured startup or RAM
penalty. Go libraries still ship inside the native binary. No comparative
performance benchmark was run here. If a TUI is proposed later, measure binary
size, cold start, peak RSS, scan time and idle refresh CPU on the same synthetic
dataset. A terminal framework might save more maintenance than it costs.

| Question | Chart choice |
| --- | --- |
| Which model, project or agent drove cost? | Sorted horizontal bars plus numeric list value and unpriced indicators. |
| When was usage recorded? | Daily columns or a compact sparkline with dated endpoints. Preserve missing dates as gaps; do not interpolate or mark them verified zero. |
| What made up the token total? | Stacked horizontal bars for disjoint fresh-input, cache-read, cache-write and output buckets, with a plain table fallback. |
| Am I near a quota? | A gauge only when the provider reports a quota and reset window. Plan proration is not a quota API. |
| Which model is better? | A sortable comparison table with workload/coverage caveats. A colorful ranking cannot establish quality. |

Avoid pie charts across many providers and decorative heatmaps in the default
summary. Keep numeric values visible, label each chart's scale, honor `NO_COLOR`,
`--plain` and redirected output, and test narrow terminals plus Unicode labels.
A future `--metric tokens|list-cost` should change both the ranking and label,
without changing headline totals or JSON completeness.

For desktop integration, first define a small versioned `status --json` response
with period, list value, measured tokens, coverage and last-observed time. It can
feed existing panels without maintaining Swift and GNOME applications. Add
polling only after measuring scan cost. A retained index is justified by measured
latency, not by copying CodeBurn's cache. Any index needs parser-version
invalidation, file replacement/truncation handling and a separate decision on
retaining history after provider logs are deleted.

A future MCP server should consume the same report layer and redact project
paths, session identities and any activity data by default. CodeBurn's
[redaction implementation](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/mcp/redact.ts)
is worth studying for fields that can leak indirectly. Redacting just the visible
project name is insufficient. No raw transcript query tool is needed.

Implementation can proceed as independently reviewable changes:

1. Establish source fixtures, multiple-root discovery and coverage diagnostics.
   Pin provider versions and expected counts; select requested agents before
   scanning. Test duplicate archives and recorded project-path fidelity. Do not
   redesign the working engine.
2. Add Cline's supported source generations. Prove their overlap rules and keep
   unknown legacy models unpriced.
3. Add Copilot CLI. Prefer supported request records; reconcile resumes,
   compaction and shutdown fallback. Keep missing IDE coverage visible.
4. Add modern Kilo, then its legacy format and Roo where useful. Shared decoding
   must not erase provider-specific accounting rules.
5. Finish Cursor's export contract spike. Ship an offline importer if the data
   supports it; document a precise blocker if it does not. Network/admin support
   is a separate product decision.
6. Add the best next local readers from Qwen, Vibe, Kimi and Goose based
   on verified data quality and actual demand. Antigravity capture is its own
   opt-in feature, not an automatic hook installation.
7. Complete the remaining adapters in the inventory at their supported evidence
   level. A detected source with no reliable usage is not a measured reader.
8. Keep analysis, graphs, statusline, MCP and interactive navigation deferred.
   Their options above remain a record for later work.

Every reader needs synthetic behavioral fixtures for complete and missing usage,
streamed snapshots, repeated identical but distinct requests, resumed/forked
history, model switches, cache inclusion/exclusion, reasoning already included in
output, midnight/timezone boundaries, malformed or changing files and overlapping
source generations. SQLite readers also need WAL, missing-table, busy and schema
change cases. Importers need repeat-import idempotence and overlapping date
windows. Independently calculate expected normalized totals and dated prices.

Useful numerical regression cases include Cline v1 or Roo gross input 10,000,
cache read 8,000 and cache write 1,000 becoming fresh input 1,000. Kilo input 100
with cache read 900 must stay fresh input 100. Copilot OTel follows the gross
input case. Cursor CSV fixtures need header reordering, BOM/CRLF, missing buckets,
reported-total mismatches, included/free rows and legitimate duplicate rows.

Unknown models must retain tokens without invented prices; aggregates must retain
their caveats; source failures must not look like zero usage. Test that JSON and
terminal totals agree and that limits only change display. Run `make test`,
`make check` and `make build` for implementation changes.

Retiring ccdeck should follow demonstrated replacement of the features actually
used, not a target adapter count. Keep its collectors until their capture and
deduplication behavior has a verified replacement. Archiving a repository is
also separate from fixing vulnerable dependencies; this research did not verify
the supplied Dependabot alert count.
