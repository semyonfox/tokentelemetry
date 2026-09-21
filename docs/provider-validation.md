# Provider validation

Validated on 19 September 2026 using synthetic fixtures only.

- Full `make test` passed with a temporary `XDG_CACHE_HOME`.
- `make check`, `make build` and `git diff --check` passed.
- `go test -race ./internal/ingest ./internal/report` passed from `engine/`.
- CLI catalog returned 42 unique entries, including two source-limited entries.
- A paired Hermes/Codex fixture counted 110 tokens once, preserved the excluded Hermes record, and retained Hermes totals when selected alone.
- CLI fixtures verified Kiro credits without token/USD conversion and Grokbot's explicit limitation.
- A combined Grok/Antigravity/Kimi/Kimi Code CLI fixture produced four records and 62,056 tokens. The expected disjoint totals were 30,972 fresh input, 70 output, 30,914 cache reads and 100 cache writes. Cumulative Antigravity snapshots counted once.

The existing pricing-refresh test consults the host cache even when its output fixture is temporary. A newer host cache can make that test fail. The full suite was therefore run with an isolated cache; production pricing behavior was not changed.

Tests establish the documented source contracts and regressions, not compatibility with every past or future provider version. See the provider evidence notes for aggregate, import and unresolved-overlap limits.

## Cursor and overlap follow-up, 20 September 2026

- `make test` passed with an isolated pricing cache; `make check` and `make build` passed.
- Race checks passed for ingest, report and CLI packages.
- Native Cursor SQLite fixtures covered automatic platform discovery, committed WAL updates, streamed counter replacement, malformed counts, missing counters and absent historical metadata. No conversation text or context-window gauge became usage.
- The built CLI automatically discovered 110 measured IDE tokens and disclosed an empty counter record. Configuring a CSV replaced that history with 110 tokens, rather than adding it again.
- Two synthetic copies of a native SDK store produced one run and 1,050 tokens: 100 input, 50 output, 700 cache reads and 200 cache writes. SDK stores were discovered without exports; selecting both Cursor sources was rejected.
- The CLI catalog retained 42 entries and detected both synthetic Cursor sources.
- Hermes/Codex regressions now cover date and agent filtering after a combined scan. Report tests also cover model, project and subagent filters. A wrapper counts when its linked native record is filtered out; a matching included native record counts once.

## Main integration and delivery, 21 September 2026

Integrated on `fbd4092` from `main`, preserving the module rename, current pricing,
T3 project lineage, Copilot VS Code/OTel support, and native Antigravity history.

- Full `make test` passed with an isolated pricing cache.
- `make check`, `make build`, and `git diff --check` passed.
- `go test -race ./internal/ingest ./internal/report ./internal/cli` passed.
- The built CLI returned 42 unique catalog entries with 40 readers/importers and
  explicit Crush/Grokbot limitations. No provider paths were detected in its
  isolated synthetic home.
- A native Cursor SQLite fixture was discovered automatically and produced 110
  measured tokens. Missing counters generated a warning. A configured CSV
  replaced those records and still produced 110 tokens.
- Antigravity tests cover selective decoding above the old 4 MiB limit, the new
  64 MiB boundary, and native versus stream-import source precedence.
- Copilot regressions cover shutdown residuals, later exact requests, invalid
  groups, zero-usage rows, and rejected snapshots followed by valid snapshots.
- Grok tests cover authoritative current ledgers, legacy fallback, explicit fork
  identity, model reconciliation, parent/child precedence and missing timestamps.
- Runtime overlap tests retain filter-sensitive Hermes/Codex handling and verify
  that T3 project lineage does not bypass reconciliation.
