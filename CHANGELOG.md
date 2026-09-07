# Changelog

## Unreleased

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
