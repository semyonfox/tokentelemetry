# Changelog

## Unreleased

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
