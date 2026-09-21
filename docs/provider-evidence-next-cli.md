# Additional CLI provider evidence

Research snapshot: 19 September 2026. Runtime readers are local-only and use
synthetic fixtures in tests.

## GitHub Copilot

Copilot combines three local sources without enabling telemetry or contacting
GitHub. The CLI reader probes the observed `assistant_usage_events` schema in
`~/.copilot/session-store.db`, reads only usage and identity columns, and opens
SQLite read-only with its WAL visible. Input is cache-inclusive, so cache reads
and writes are subtracted once to produce disjoint buckets. Older stores without
the complete request schema fall back to `session-state/*/events.jsonl` shutdown
usage instead of guessing absent fields.

Shutdown model metrics are cumulative snapshots. Repeated snapshots are turned
into deltas, while explicit compaction resets start a new interval and produce
an incompleteness diagnostic. Request rows remain authoritative. For each
session/model and shutdown cutoff, the reader subtracts exact request rows only
when every known component is no larger than the shutdown total, then emits the
uncovered residual. Mixed-direction disagreements keep only the exact rows and
report the mismatch. When request and shutdown model identifiers differ, only a
session-level residual can be proven; it is retained as unknown-model and
unpriced. A bad request row invalidates that whole session/model group so its
shutdown aggregate can replace it without mixing partial precision.

The VS Code reader remains separate and accepts only known Copilot participant
IDs with producer-recorded `modelTotals`; the Copilot CLI Agent Host copy is
left to the native CLI journal. The optional OpenTelemetry reader is enabled
only by `COPILOT_OTEL_FILE_EXPORTER_PATH` and accepts direct `chat` spans, not
agent rollups, metrics, or logs. Native conversation IDs suppress the entire
overlapping export conversation because those sources do not share request IDs.
GitHub documents the exporter and its opt-in configuration in the
[Copilot CLI OpenTelemetry reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-command-reference#opentelemetry-monitoring).
The SQLite and shutdown schemas are probed observed contracts, not published
stable export formats; diagnostics remain visible when they change.

## Cursor local database

The IDE reader automatically discovers `Cursor/User/globalStorage/state.vscdb`
under the platform's user configuration directory: `~/.config` on Linux,
`~/Library/Application Support` on macOS, and `%APPDATA%` on Windows.
`TT_CURSOR_DB` overrides the file path. It opens SQLite read-only, including
committed WAL updates, and projects accounting fields from `cursorDiskKV`
`bubbleId:<conversation>:<bubble>` rows. It never queries authentication storage
or reads message bodies. The observed field layout is also implemented in
[CodeBurn's reader](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/cursor.ts).

Cursor support explicitly calls `tokenCount` best-effort: the client fetches
counts from the backend after streaming, but timing failures can leave them
empty. The dashboard remains their recommended usage source.
[Cursor's explanation, 27 March 2026](https://forum.cursor.com/t/cursordiskkv-table-records-always-show-0-for-tokencount/155984/5).

Only assistant bubbles with actual nonnegative `inputTokens` and `outputTokens`
are counted. Input stays `Unclassified` because the local contract does not
provide the cache split. Output remains output, and the record is unpriced.
`modelInfo.modelName` and `createdAt` come from the same bubble; absent values
stay unknown or undated. A bubble is labelled aggregate because it is not a
complete per-API-call ledger. Stable native keys prevent repeated scans from
adding streamed updates twice.

Zero or missing assistant counters generate an incomplete-usage diagnostic.
Malformed counters are excluded with a diagnostic. The reader never substitutes
character estimates, conversation context gauges, a current session model, or
file modification times. This provides automatic local coverage where Cursor
recorded counters, not complete historical usage on every Cursor version.

Configuring `TT_CURSOR_CSV` replaces the local database source entirely. A bad
configured CSV reports an error instead of silently falling back to a different
source. The CLI rejects combining `cursor` and `cursor-agent`, which cannot be
reconciled through shared request identities.

## Cursor dashboard CSV

Cursor officially documents that dashboard charts can be downloaded as CSV,
but does not publish a stable row schema:
[Usage Analytics](https://cursor.com/docs/account/teams/analytics). The reader
therefore requires the observed request export headers exactly: `Date`,
`Model`, `Input (w/ Cache Write)`, `Input (w/o Cache Write)`, `Cache Read`,
`Output Tokens`, and `Total Tokens`; optional account/kind/agent columns are
ignored. The current and historical header variants are independently listed
by [tokmesh](https://docs.rs/tokmesh-core/latest/src/tokmesh_core/sessions/cursor.rs.html#7-10).

`TT_CURSOR_CSV` names one replacement snapshot. Users should overwrite that
file with each full export, never append or combine overlapping date windows.
Rows retain their ordinal so identical legitimate requests preserve
multiplicity. The four token columns are accepted only when their sum equals
`Total Tokens`; otherwise only the total is retained as `Unclassified` and the
row is unpriced. The export's `Cost`, `Included`, and `Free` values are not
substituted into list-value accounting because TokenTelemetry has no recorded
charge field. In particular, `Included` is not interpreted as zero value.

## Grok Build

Current Grok Build documents its local session tree and recommends the
structured `grok usage` command instead of parsing conversation files:
[session guide](https://github.com/xai-org/grok-build/blob/4247f661689354b831191f11eeeac8424993fe3d/crates/codegen/xai-grok-pager/docs/user-guide/17-sessions.md).
The command is backed by each session's `usage.json`; its public source defines
`sessionId`, `updatedAt`, and per-turn `turnNumber`, `endedAt`, gross
`inputTokens`, `outputTokens`, `cachedReadTokens`, `cacheCreationTokens`,
`reasoningTokens`, `totalTokens`, `modelCalls`, `primaryModelId`,
`usageIsIncomplete`, and `modelUsage`:
[`usage_file.rs`](https://github.com/xai-org/grok-build/blob/4247f661689354b831191f11eeeac8424993fe3d/crates/codegen/xai-grok-shell/src/session/usage_file.rs).

The reader treats a native `usage.json` as authoritative even when it is
invalid, so a damaged current file cannot silently fall back to an older
overlapping stream. Gross input is split into fresh input and the two cache
subsets. Per-model rows are emitted only when all components, including call
counts, reconcile exactly to the turn total. A mismatched map leaves one
measured unknown-model aggregate unpriced. When the map is absent, the native
`primaryModelId` is used if present; otherwise the measured aggregate remains
unknown and unpriced. `modelCalls` distinguishes one API call from a turn
aggregate. `usageIsIncomplete` is also unpriced because the producer states
that subagent tokens may be missing. A row without `endedAt` remains measured
but undated and unpriced; a malformed non-empty timestamp is rejected. The
server-reported `costUsdTicks` is not substituted into list-value accounting
until TokenTelemetry has a separate recorded-charge field.

Forks copy `usage.json` before adding child turns, then restamp the child session
ID ([persistence source](https://github.com/xai-org/grok-build/blob/4247f661689354b831191f11eeeac8424993fe3d/crates/codegen/xai-grok-shell/src/session/persistence.rs)).
Copied turn keys therefore retain their oldest proven owner only when explicit
parent/fork provenance and the complete turn record match. Completed subagent
usage is folded into the parent ledger. A child is suppressed only when the
parent update stream proves that child's `subagent_finished` event precedes a
complete matching durable turn. Without that proof, exact child ledgers are
retained and the overlapping parent aggregate is omitted with a scanner
diagnostic. Nested ambiguity propagates upward so an ancestor aggregate cannot
reintroduce the same child usage.

Pre-`usage.json` sessions retain a conservative `updates.jsonl` fallback based
on the observed CodeBurn provider at
[`4cf1885`](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/grok.ts).
Repeated completion updates for one prompt are last-write-wins. The fallback
does not infer historical request models from `summary.current_model_id`, does
not assign session-level metadata or file-modification times to individual
turns, and does not estimate stream-only usage. Legacy measured turns therefore
remain undated and unpriced.

## Grok Bot

The official [Settings and notifications](https://docs.x.ai/grok-bot/settings-and-notifications)
page exposes weekly and on-demand usage through the app or Cursor account, but
does not document a local per-turn token export. The Electron client's observed
`sand-client-persistence` mirror contains message text but no token counts,
charge, or real model id. CodeBurn's
[`grokbot.ts`](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/grokbot.ts)
uses character estimates and a synthetic model. TokenTelemetry emits no usage
for this source. The provider catalog lists the limitation, and selecting
`--agent grokbot` explains it before scanning.

## CodeWhale

CodeWhale's session JSON schema is not publicly documented. The reader follows
the reverse-engineered local contract in CodeBurn
[`codewhale.ts`](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/codewhale.ts):
`~/.codewhale/sessions/*.json` and legacy `~/.deepseek/sessions/*.json` carry a
native session id, timestamps, model, workspace, and only one `total_tokens`
counter. Migrated copies deduplicate by native session id. The total is kept as
`Unclassified` and unpriced because no input/output/cache split exists.
Recorded session/subagent USD cost is not imported until the core model has a
separate recorded-charge field.
