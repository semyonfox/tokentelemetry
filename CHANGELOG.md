# Changelog

## Unreleased

- Cache parsed usage locally to avoid repeatedly decoding unchanged histories;
  add `--no-cache` and cache diagnostics in `--verbose` reports.

- Add `--all-time` and `-a` to report commands, including `tokentelemetry -a`
  for an all-time summary. Reject combinations with explicit date bounds.

- Expand the provider catalog to 42 entries with 40 readers/importers. Mark
  Crush and Grokbot as source-limited instead of estimating their usage.
- Discover Cursor IDE and SDK SQLite stores locally, disclose missing counters,
  and keep optional exports separate from native history.
- Exclude explicitly linked Hermes/OpenClaw runtime mirrors from combined
  totals while retaining their records and respecting report filters.
- Preserve measured unclassified tokens and provider credits without inventing
  dollar values or historical model attribution.
- Read Antigravity metadata up to 64 MiB while extracting only accounting fields,
  and support optional stream-json captures as a replacement source.

- Add native local readers for GitHub Copilot's session store and session
  journals and VS Code storage, Grok Build's persisted usage ledger, and
  recognized Antigravity conversation metadata.
- Read optional Copilot CLI OpenTelemetry file exports for conversations
  without native usage, excluding overlapping records and metric rollups.
- Prefer Codex per-response usage records and preserve legacy turns in upgraded sessions.
- Reconcile complete Hermes call logs against database rows for daily attribution.
- Preserve distinct Hermes task/route rows and large session aggregates; disclose
  aggregate pricing and legacy replay assumptions in terminal and JSON reports.

- Make the Go CLI the primary application; remove the inherited web stack,
  dashboard plugins, proxy and deployment scripts.
- Add the default `summary` command with terminal charts and complete JSON output.
- Count sessions separately across agents when their session IDs coincide.
- Reject reversed date ranges, negative limits and unexpected report arguments.

Earlier application development remains available in Git history.
