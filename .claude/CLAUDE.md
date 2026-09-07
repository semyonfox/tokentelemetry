# TokenTelemetry project rules

TokenTelemetry is a local Go CLI. See root AGENTS.md for collaboration rules.

- Source: `engine/`; default command: `summary`.
- Verify with `make test`, `make check`, and `make build`.
- Preserve original licence notices and project attribution.
- Never commit secrets, personal logs, machine identifiers or session URLs.
- Use synthetic fixtures. Independently recompute expected accounting totals.
- Bucket dates in the user's local timezone.
- Unknown models remain explicitly unpriced. List-rate value is not a bill.
- Do not read personal agent logs for tests; use temporary fixture directories.
- Do not commit, push, publish, or operate running services without explicit
  user authorization.

The old Python backend, web dashboard and UPDATE.json release feed have been
retired. Their runtime conventions do not apply to the CLI.
