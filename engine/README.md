# TokenTelemetry CLI reference

The Go engine is the primary application. For installation, project origins and
accuracy limits, see the [project README](../README.md).

## Commands

```
summary                      overview with terminal charts; default command
 daily | weekly | monthly    usage over time
session | model | project    usage by dimension
price <model>                a model's rate history
agents                       provider coverage and detected log paths
version                      build version
```

`summary` defaults to the last 30 local calendar days, including today. An explicit
`--since` or `--until` replaces that default. `--all-time`, abbreviated `-a`,
includes all available history, including undated records. It works on every
report command and cannot be combined with date bounds. Other report commands
already default to all available history. Usage logs stay local.

Report flags can be used without a command name: `tokentelemetry -a` is an
all-time summary, and `tokentelemetry --plain` is a summary without charts.

See the [complete 42-adapter inventory](../docs/provider-inventory.md) for local
readers, explicit imports, credits-only sources and unsupported source limits.
`agents --json` includes coverage status and whether explicit selection is required.

## Filters and output

```
--all-time / -a          all available history; incompatible with date bounds
--since / --until DATE   inclusive local-day bounds, YYYY-MM-DD
--agent NAME            repeatable, or comma-separated
--model NAME            repeatable, or comma-separated
--project NAME          full path or trailing folder name; repeatable
--subagents MODE        include (default) | only | exclude
--group-by DIMS         nested detailed reports: day, week, month, agent,
                        model, provider, project, session; "none" for flat
--compact               abbreviated counts in detailed tables
--verbose               scan, cache and deduplication diagnostics
--no-cache               read source logs without using or updating the scan cache
--json                  machine-readable output
--limit N               detailed report row limit; 0 means all
--no-color              disable colour; also honours NO_COLOR
--plain                 omit summary bars
```

Summary charts show seven rows by default, including with `--limit 0`; a positive
limit changes that cap. The daily chart selects the latest recorded days; models
and agents are ranked by list cost. Chart limits never reduce the totals or JSON
output. Summary JSON includes the daily series and all dimension aggregates.
`--group-by` adds nested data to JSON and detailed tables; summary charts stay flat.
Detailed timeline and session views default to a model breakdown. `project` stays
flat by default, ranked by list cost, so it remains a useful overview; add
`--group-by model` or `--breakdown` when the per-model detail is needed.

Project grouping normalizes path separators. An exact Codex session link from
an active T3 project takes precedence; otherwise live Git metadata groups nested
CWDs and linked worktrees under the main checkout. Unambiguous T3 worktree
metadata can also recover deleted worktrees. Unknown or ambiguous paths stay
separate rather than being guessed together. JSON project rows retain their
recorded CWDs in `project_paths`.

```sh
./dist/tokentelemetry -a
./dist/tokentelemetry summary --all-time --agent claude,codex
./dist/tokentelemetry summary --since 2026-08-01 --until 2026-08-31 --json
./dist/tokentelemetry daily --group-by agent,model
./dist/tokentelemetry session --project tokentelemetry --limit 10
```

## Scan cache

Reports cache parsed usage under the operating system's user cache directory,
in `tokentelemetry/scans`. On Linux this is normally
`~/.cache/tokentelemetry/scans`, or `$XDG_CACHE_HOME/tokentelemetry/scans`.

Caching currently covers Claude Code, Codex, Antigravity, Cursor IDE/SDK,
OpenCode, Hermes and Gemini. Other providers continue to read their sources.
Claude and Codex also reuse individual unchanged files when another session is
active. Scans with warnings or errors are read again so diagnostics stay visible.

The cache stores normalized accounting records rather than transcripts. Prices,
report filters, project grouping, duplicate detection and runtime overlap checks
are recomputed on each run. `--no-cache` reads the sources directly without
reading or updating this cache. `--verbose` reports provider cache hits and misses;
individual file reuse inside a changed provider is not included in those counts.

Cache reuse checks source file metadata and directory membership, including
SQLite WAL changes. Sources changed during a scan are not saved as a stable
result. Missing, corrupt or unwritable cache files fall back to a fresh scan.
A new executable invalidates previous parsed results. Use `--no-cache` after a
manual edit that preserves both file size and modification time.

## Cursor usage

Cursor's IDE database is discovered automatically on Linux, macOS and Windows:

```sh
./dist/tokentelemetry daily --agent cursor --plain
```

Only recorded token counters are included. Cursor often leaves those counters
empty, so the reader reports missing usage and cannot recover a complete history.
Its local input counters have no cache breakdown and stay unclassified and
unpriced. `TT_CURSOR_DB` can point to a database in a custom data directory.

For fuller history, `TT_CURSOR_CSV` optionally selects a usage export containing
the request date, model, input, output, cache and total token columns:

```sh
TT_CURSOR_CSV=/path/to/usage.csv ./dist/tokentelemetry daily --agent cursor --plain
```

The CSV replaces local database history entirely, preventing overlap. Replace
that file when downloading a newer export. Reports read the current
snapshot, so rerunning a report does not accumulate earlier imports. Identical
rows within one export remain separate requests. See the
[CSV format and accounting limits](../docs/provider-evidence-next-cli.md#cursor-dashboard-csv).

Local Cursor SDK stores are discovered under `~/.cursor/projects/` when selected:

```sh
./dist/tokentelemetry daily --agent cursor-agent --plain
```

This reads SDK runs, not ordinary Cursor CLI transcripts. Terminal runs provide
measured token buckets; their model selection cannot establish the historical
model for every subagent call, so these aggregates stay unpriced. Saved SDK
results remain available through `TT_CURSOR_AGENT_DIR`.

The SDK source requires explicit selection, and the CLI rejects selecting both `cursor`
and `cursor-agent` in one report because the sources can overlap without shared
request IDs. Neither importer fetches account data automatically.

## Plan costs

Optionally configure `~/.tokentelemetry/plans.json`:

```json
{
  "subscriptions": [
    { "agent": "codex", "name": "My coding plan", "monthly_usd": 100 }
  ]
}
```

Configured plan costs are prorated over the matched activity window, including
its first and last local days. They are separate from API list value and are not
an invoice total. Model/project filters do not allocate a subscription's cost to
that subset of usage. Without a plans file, the CLI does not invent a plan cost.

## Pricing

Rates in `internal/pricing/data/pricing.json` are embedded in the executable.
Unknown prices remain explicit, and usage predating the available price history
uses the earliest rate with a disclosure in the report.

Maintainer commands, from `engine/`:

```sh
go run ./cmd/pricing-sync -dry-run
go run ./cmd/pricing-sync
```

Pricing sync fetches current data over the network.
Override rates using `~/.tokentelemetry/pricing.json` in the same schema, or set
`TT_PRICING_FILE` to an override file.

The same file can map an agent's opaque model id to a released model for both
display and list-rate accounting. For example, Z.ai revealed Ox Alpha as
GLM-5.3-Flash, while OpenCode recorded it under `x-preview-f-free`:

```json
{
  "schema": 2,
  "aliases": {
    "x-preview-f-free": "glm-5.3-flash"
  }
}
```

Aliases are exact after normalization. The original id remains in raw ingested
turn data, while reports group it under the canonical model.

## Development and packaging

From the repository root, run `make test`, `make check`, and `make build`.
`make packages` additionally requires Node.js and cross-compiles six native
packages plus the npm launcher into `engine/dist/npm`. Publishing is separate;
use the local binary to test this checkout. Distributed packages include the MIT
licence notice.

The [provider inventory](../docs/provider-inventory.md) describes all 42 audited adapters.
The scanner interface and registration live in `internal/ingest/ingest.go`.

## GitHub Copilot

GitHub Copilot needs no TokenTelemetry-specific setup. The reader checks
the default `~/.copilot/session-store.db` (or an already-configured
`COPILOT_HOME`) for its schema-gated observed `assistant_usage_events` table.
This is a local implementation detail, not a documented Copilot reporting API,
so unknown layouts are skipped. It reads the already-written
`session-state/*/events.jsonl` shutdown aggregate and VS Code's persisted
`chatSessions` records when they contain complete
`modelTotals` fields and identify a recognized GitHub Copilot chat participant
(legacy `github.copilot.*` or current non-CLI Copilot Agent Host ID). The
Copilot CLI Agent Host shares the native session journal, so its duplicate
VS Code copy is excluded. The scanner never enables OpenTelemetry or reads
debug logs.
VS Code session storage includes chat payloads, but the scanner neither retains
nor reports prompt/response text and never uses those fields for accounting.

Valid CLI request rows remain authoritative. Cumulative shutdown snapshots
become interval deltas. The reader subtracts request rows at or before the
shutdown only when every token bucket reconciles, then adds the uncovered
residual. Later requests remain separate. Conflicting buckets produce a warning
and keep only exact rows. If model identifiers differ, any reconciled residual
has unknown model attribution and stays unpriced. An invalid request row
invalidates its session/model group; a matching shutdown aggregate can replace
that group with a diagnostic.

Shutdown residuals and VS Code totals are aggregates and use base context
pricing. Resets and compaction boundaries disclose possible missing history.
A session with neither usable native source, or an older/changed schema without
complete counters, remains unavailable rather than estimated. Copilot's scalar
cache-write field can be incomplete for a compaction row; the reader retains
its total tokens and reports that split as uncertain.

### Optional Copilot file exports

TokenTelemetry also reads Copilot CLI's existing OpenTelemetry JSON-lines file
when `COPILOT_OTEL_FILE_EXPORTER_PATH` is set. Native scanning remains the
default. To opt into Copilot's local exporter, use the same environment for
Copilot and TokenTelemetry:

```sh
mkdir -p "$HOME/.copilot"
export COPILOT_OTEL_FILE_EXPORTER_PATH="$HOME/.copilot/otel.jsonl"
copilot
tokentelemetry summary --agent copilot
```

The scanner reads completed model-call spans with recorded token counts and a
conversation ID. It ignores metric rollups and message content. If a conversation
already has native usage, the native records win for that entire conversation;
the two sources have no shared per-request identity, so mixing partial records
could double-count calls. Exported spans fill conversations with no native
usage. TokenTelemetry never enables the exporter itself.

The file path and span fields follow the
[Copilot CLI OpenTelemetry reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#opentelemetry-monitoring).

## Grok Build

Grok Build versions that write an atomic per-turn `usage.json` ledger store it
under `~/.grok/sessions/` by default (or an already-configured `GROK_HOME`).
The reader finds that ledger automatically and reads model IDs, end timestamps,
token buckets and session metadata for project/subagent attribution. Per-model
rows are used only when their counters and call counts reconcile with the turn
total; otherwise measured usage stays unknown and unpriced. Multiple-call rows
use aggregate pricing. Missing end times stay undated and unpriced; malformed
nonempty times are rejected. Incomplete rows retain measured counters with a
warning and no assigned cost. `grok usage <session-id>` diagnoses the same ledger.

An explicit fork link and an exactly matching inherited turn preserve the
original request identity. A recorded child completion followed by a complete,
matching parent turn establishes a folded aggregate and counts it once. Without
that evidence, exact child ledgers take precedence over overlapping parent and
ancestor aggregates, with a warning.

Legacy `updates.jsonl` completion counters are read only when `usage.json` is
absent. Repeated prompt IDs replace earlier snapshots. These records stay
undated and unpriced because the source lacks per-turn time and reliable model
attribution. A malformed current ledger never triggers a legacy fallback.
Prompt/response text and debug logs are not used to estimate usage.

The ledger has no endpoint or credential-mode field. Its subscription billing
label is the normal-route default, not proof that a historical turn used
signed-in plan access rather than an API key or custom endpoint.

## Antigravity

The Antigravity reader automatically scans recognized `.db` files below
`~/.gemini/antigravity`, `~/.gemini/antigravity-cli`,
`~/.gemini/antigravity-ide`, `~/.gemini/antigravity-backup`, and
`~/.config/antigravity`, including nested conversation directories. It accepts
only the observed conversation database version, the required `gen_metadata`
table, and, when present, a recognized `steps` table. It opens candidate files
only to inspect their schema, then queries only those metadata tables. For each
metadata blob at most 64 MiB, it extracts direct generation counters and their
same-record response model/ID; `steps` is timestamp-only join data. It never
queries transcript, tool/event or `tokens_cache.json` data. Oversized blobs,
unsupported versions and recognized-but-invalid table layouts produce a scan
warning instead of an estimate; unrelated databases are ignored. Numeric-only
models remain unpriced `antigravity-model-N` values unless a pricing alias maps
them. Antigravity project attribution is not currently available. Its metadata
has no endpoint or credential-mode field, so the subscription billing label is
a normal-route default rather than invoice attribution.

Setting `TT_ANTIGRAVITY_DIR` selects saved stream-json captures instead of the
native databases. Cumulative snapshots count once per conversation; malformed,
conflicting or unfinished captures produce warnings. The two sources are never
combined automatically.

## Hook availability

Cursor's documented [hooks](https://cursor.com/docs/hooks#afteragentresponse)
and [CLI output](https://cursor.com/docs/cli/reference/output-format) do not
provide no per-call token counters. The Cursor reader instead discovers local
IDE databases and uses measured counters where available; missing history stays
explicitly unavailable. SDK stores and optional CSV exports cover separate
workflows. See [Cursor usage](#cursor-usage) for their limits.

[Antigravity hooks](https://antigravity.google/docs/hooks#postinvocation) provide
invocation identifiers without token counters; the local database reader is
the accounting source. Grok's native ledger already persists usage locally.
Its optional [external telemetry](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/24-monitoring-usage.md)
targets a collector or console, so it does not need another local capture hook.

## Source accuracy and reconciliation

Codex `token_usage_record` entries carry response and originating thread IDs.
The reader uses them instead of overlapping `token_count` snapshots in the same
turn, keeping older turns when a session was upgraded mid-history. Copied parent
records are not charged to the child. Their original rollout supplies the original
timestamp. Legacy forks without structured records still use the timing fallback;
reports disclose retained records processed that way. Input counts exclude both
cache reads and cache writes after normalization.

Hermes database rows remain distinct across profile, session, model, billing route,
billing mode and task. Session aggregates are not subject to the per-call token cap.
The reader checks `logs/agent.log` and numeric rotations beside each profile's
`state.db`. It replaces a main-loop aggregate only when all four billable token
buckets and the recorded call count, when available, reconcile with the logs and
the billing route is unambiguous. Partial or conflicting logs never add extra
usage. Auxiliary task rows remain separate aggregates.

Reasoning tokens are already included in output. When logs lack their per-call
split, that metadata remains in a separate aggregate record without adding to the
billable token total. Aggregate records use the database's last-seen timestamp and
base context rates; they are explicitly approximate, not individual API calls.
Local log timestamps cannot recover a timezone that was changed or not recorded.

JSON totals and buckets expose `aggregate_records` and `heuristic_records` when
present; totals also expose `aggregate_tokens`. The terminal shows the same caveats.

The source contracts were checked against Codex `4110342321bb19b0053190750a0a8b76427b13ad`
and Hermes `869228cab4a8276d3b4c78da9d9939670c47bd0f`. Synthetic regression tests
cover response replay, upgrades, cache normalization, task/route separation,
rotated and incomplete logs, and independently calculated daily costs.

Prices refresh through daily CI. Reports check the published dataset at most once
per day, with a three-second timeout and a cached or bundled fallback. Set
`TT_OFFLINE=1` to skip network checks. Your pricing overrides still take priority.
Updates preserve recorded rate history; newly observed changes take effect on
the sync date. Paid-to-zero changes are retained at their previous rates for review.
