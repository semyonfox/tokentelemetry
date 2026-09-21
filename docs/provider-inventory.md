# Provider implementation inventory

All 42 adapters registered by [CodeBurn at 4cf1885](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/index.ts) have been assessed. TokenTelemetry now registers 40 readers/importers, including its original six. Crush and Grokbot remain source-limited because their audited local stores cannot supply measured usage history. They appear in `tokentelemetry agents` with that explanation, not as functioning token readers.

Coverage is deliberately narrower than a vendor-wide claim. Copilot reads measured CLI and supported VS Code records, Cursor's automatic local counters are incomplete with CSV as an optional replacement, and Kiro supplies credits. These are adapter counts, not independent vendors or guarantees for every product version. Synthetic tests cover the formats described below; no personal logs were used.

Graph work remains deferred. The [comparison and porting plan](provider-port-plan.md#deferred-terminal-options) records handmade bars, asciigraph, Bubble Tea, NTCharts, PTerm and tview for later consideration. No graph dependency or interactive UI was added.

## Complete list

Use these numbers or identifiers to prioritize deeper coverage. Entries marked **Existing** were already present before this port.

| # | Adapter | CLI identifier | Implementation | Supported data and limits |
| --- | --- | --- | --- | --- |
| 1 | Antigravity | `antigravity` | Local or import | Native CLI/IDE SQLite invocation counters; optional cumulative stream-json replacement. [Evidence](provider-evidence-imports.md). |
| 2 | Claude Code | `claude` | Local | Multiple config roots, nested archives and native request deduplication. [Evidence](provider-evidence-imports.md). |
| 3 | Cline | `cline` | Local | Legacy IDE request metrics; latest task model cannot establish historical cost. [Evidence](provider-evidence-json.md). |
| 4 | Cline CLI | `cline-cli` | Local | Native v1 sessions, per-message usage and bounded aggregate fallback. [Evidence](provider-evidence-json.md). |
| 5 | Codebuff | `codebuff` | Local | Generation usage or reported credits; cumulative nested snapshots excluded. [Evidence](provider-evidence-tail.md). |
| 6 | CodeWhale | `codewhale` | Local aggregate | Measured session total, unclassified/unpriced. [Evidence](provider-evidence-next-cli.md). |
| 7 | Codex | `codex` | Existing | Native JSONL and replay reconciliation; compressed rollouts excluded. |
| 8 | GitHub Copilot | `copilot` | Local | CLI per-request SQLite and shutdown reconciliation, measured VS Code chat totals, and optional OTel fallback; no account totals. [Evidence](provider-evidence-next-cli.md). |
| 9 | Crush | `crush` | Source-limited | Latest context counters are not cumulative usage; no fabricated token reader. [Evidence](provider-evidence-sqlite.md). |
| 10 | Cursor | `cursor` | Local or import | Automatic IDE SQLite discovery; measured counters only, missing usage disclosed. Optional CSV replaces local history. [Evidence](provider-evidence-next-cli.md). |
| 11 | Cursor Agent | `cursor-agent` | Local or import | Automatic SDK SQLite discovery with explicit selection; saved SDK results also supported. Historical CLI transcript usage unavailable. [Evidence](provider-evidence-imports.md). |
| 12 | DeepSeek Harness | `dsh` | Local | Versioned JSONL and bounded compressed session journals. [Evidence](provider-evidence-warp-dsh.md). |
| 13 | Devin | `devin` | Local | Local CLI ATIF step/generation metrics; no cloud account totals. [Evidence](provider-evidence-tail.md). |
| 14 | Droid | `droid` | Local aggregate | Session totals; historical model attribution unavailable. [Evidence](provider-evidence-json.md). |
| 15 | Forge | `forge` | Local | Actual persisted conversation usage; approximate dates; no estimated tokens. [Evidence](provider-evidence-sqlite.md). |
| 16 | Gemini CLI | `gemini` | Existing | Native session usage. |
| 17 | Goose | `goose` | Local aggregate | Session counters; historical model attribution unavailable. [Evidence](provider-evidence-sqlite.md). |
| 18 | Grok | `grok` | Local | Official usage.json plus legacy fallback; reconciled model rows, fork and child overlap handling. [Evidence](provider-evidence-next-cli.md). |
| 19 | Grokbot | `grokbot` | Source-limited | Local mirror lacks measured usage; no character-derived estimate. [Evidence](provider-evidence-next-cli.md). |
| 20 | Hermes | `hermes` | Existing, updated | Per-call reconciliation or labelled aggregates; linked Codex runtime mirrors are excluded from combined totals. [Evidence](provider-evidence-overlap.md). |
| 21 | IBM Bob | `ibm-bob` | Local | Observed IDE task metrics; unpriced. [Evidence](provider-evidence-json.md). |
| 22 | Kilo Code | `kilo-code` | Local | Current SQLite and legacy history, modern rows take precedence. [Evidence](provider-evidence-sqlite.md). |
| 23 | Kimi | `kimi` | Local | Legacy wire StatusUpdate usage, native subagent records. [Evidence](provider-evidence-cli.md). |
| 24 | Kimi Code | `kimicode` | Local | Current per-generation wire usage.record paired with request model. [Evidence](provider-evidence-cli.md). |
| 25 | Kiro | `kiro` | Credits | CLI metering credits only; no token/USD conversion or IDE history. [Evidence](provider-evidence-extra.md). |
| 26 | Lingtai TUI | `lingtai-tui` | Local | Parent token ledgers; nested daemon copies excluded. [Evidence](provider-evidence-tail.md). |
| 27 | Mistral Vibe | `mistral-vibe` | Local aggregate | Session counters; historical model attribution unavailable. [Evidence](provider-evidence-cli.md). |
| 28 | Mux / Xum | `mux` | Local | Assistant step aggregates, subagents and separately billed tool models. [Evidence](provider-evidence-imports.md). |
| 29 | Oh My Pi | `omp` | Local | Native v3 session and model-usage records. [Evidence](provider-evidence-json.md). |
| 30 | OpenClaw | `openclaw` | Local | Active SQLite transcripts and JSONL; compressed cold archives excluded. [Evidence](provider-evidence-json.md). |
| 31 | OpenClaude | `openclaude` | Local | Native transcript usage; actual model when recorded. [Evidence](provider-evidence-json.md). |
| 32 | Open Design | `open-design` | Local | NDJSON usage with native model changes and protocol-specific cache normalization. [Evidence](provider-evidence-tail.md). |
| 33 | OpenCode | `opencode` | Local, updated | Modern session_message and legacy SQLite/JSON; migrated rows deduplicated. [Evidence](provider-evidence-imports.md). |
| 34 | Pi | `pi` | Existing | Native session JSONL. |
| 35 | Quick Desktop | `quickdesk` | Local legacy | Observed local EMF metrics; unpriced; current cloud history unavailable. [Evidence](provider-evidence-imports.md). |
| 36 | Qwen | `qwen` | Local | Native ChatRecord usage and fork origin IDs. [Evidence](provider-evidence-cli.md). |
| 37 | Roo Code | `roo-code` | Local | IDE request metrics; historical model attribution unavailable. [Evidence](provider-evidence-json.md). |
| 38 | Vercel AI Gateway | `vercel-gateway` | Import | One saved report snapshot, single grouping dimension; unpriced. [Evidence](provider-evidence-imports.md). |
| 39 | Warp | `warp` | Local aggregate | Measured conversation/model total, unclassified/unpriced. [Evidence](provider-evidence-warp-dsh.md). |
| 40 | ZCode | `zcode` | Local | Observed local model_usage SQLite schema. [Evidence](provider-evidence-sqlite.md). |
| 41 | Zed | `zed` | Local aggregate | JSON/zstd request/session counters; historical model attribution unavailable. [Evidence](provider-evidence-sqlite.md). |
| 42 | Zerostack | `zerostack` | Local aggregate | Session totals using final protocol; historical model attribution unavailable. [Evidence](provider-evidence-imports.md). |

## Running and importing

```sh
tokentelemetry agents
tokentelemetry agents --json
tokentelemetry daily --agent copilot,cline-cli,qwen --plain
tokentelemetry model --agent kilo-code --json

TT_CURSOR_CSV=/path/to/usage.csv tokentelemetry daily --agent cursor --plain
tokentelemetry daily --agent cursor-agent --plain
TT_CURSOR_AGENT_DIR=/path/to/results tokentelemetry daily --agent cursor-agent --plain
TT_ANTIGRAVITY_DIR=/path/to/captures tokentelemetry daily --agent antigravity --plain
TT_VERCEL_REPORT=/path/to/report.json tokentelemetry daily --agent vercel-gateway --plain
```

Replace Cursor CSV and Vercel report snapshots when refreshing them. Do not append overlapping exports. Cursor Agent and Vercel Gateway require explicit `--agent` selection because they can overlap Cursor billing exports or native agent logs. The CLI rejects selecting both `cursor` and `cursor-agent` in one report because the exports do not share native request identities. Selecting Vercel Gateway with overlapping native sources can still double count.

`summary` defaults to the last 30 days. Undated SDK results, Antigravity captures and model-grouped Vercel reports need an all-history command such as `daily` or `model`. Dates and models absent from the source are not invented. Importers do not obtain credentials, make account requests or install capture hooks.

`agents --json` includes `status`, `coverage` and `explicit_only`. Its `installed` field means a configured readable source path was found, not that an application installation was checked. Source-limited entries do not probe application stores.

## Accounting rules

- Disjoint fresh input, output and cache buckets are normalized using each native contract. Reasoning is not added twice when already included in output.
- Measured tokens with no reliable decomposition appear as `usage.unclassified`. They count toward tokens and contribute no dollar value.
- Model/protocol ambiguity carries an `unpriced_reason`; knowing the final session model does not establish what earlier requests used.
- Kiro and Codebuff credits appear separately in `totals.credits_by_agent`. Credits are neither tokens nor dollars and are not comparable across providers.
- Native IDs deduplicate copied or resumed records. Sources without request IDs have explicitly narrower guarantees. Aggregate dates describe the available source timestamp, not reconstructed daily request activity.
- Provider-reported charges, subscription inclusion and BYOK zeros are not substituted for API list value. Text length and context-window estimates are not measured usage.

Cross-runtime overlap handling is documented in [the runtime evidence note](provider-evidence-overlap.md). An explicitly linked Hermes/Codex aggregate is not added twice; older histories without shared identities remain a reconciliation limit.

## Follow-up priorities

User priorities are Grok, Grokbot, Antigravity, Kimi and Kimi Code, while retaining the full inventory. Grok uses its official native usage ledger, with legacy fallback. Grokbot needs an authoritative export before a measured reader is possible. After those, my recommended deeper version coverage is Cursor/Copilot, Cline/Roo/Kilo, then Qwen/Goose/Zed. Kiro IDE credit formats remain separate follow-up work.

[Validation results](provider-validation.md) record the final test commands and independently checked CLI totals.
