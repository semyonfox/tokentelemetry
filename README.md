# TokenTelemetry

Local token and cost reports for AI coding agents, built around a Go accounting
engine. Reads existing logs and SQLite databases. Reporting makes no network
calls and needs no Python runtime, web server or browser.

Supported readers: Claude Code, Codex CLI, Gemini CLI, OpenCode, Hermes and Pi.

## Run from source

Requires Go 1.26 or newer.

```sh
make build
./dist/tokentelemetry
./dist/tokentelemetry summary --since 2026-09-01
./dist/tokentelemetry daily --group-by agent,model
./dist/tokentelemetry model --json
```

The default command is `summary`, covering the last 30 local calendar days.
It shows accounting totals, daily token bars, and models and agents ranked by
API list cost. Each chart shows up to seven rows. `--limit N` changes that
number without changing the headline totals. Daily bars show the latest recorded
days; missing days are not presented as verified zero usage. Each chart scales
its bars independently.

Use `--plain` to omit bars, or `--json` for the complete report without display
limits. Bars also disappear when `COLUMNS` is set below 72. `--no-color` and
`NO_COLOR` disable colours. Reports print once and exit; there is no interactive
terminal UI to manage.

Explicit date filters replace the summary's default window. Detailed commands
such as `daily` and `model` include all history unless filtered.

## Accounting

Each reader normalises usage into fresh input, output, cache reads and cache
writes. Reports use the same pricing and aggregation code for every command.
Rates are embedded, effective-dated and include supported context tiers and cache
rates. Unknown models keep their token counts but contribute no invented cost.

The dollar total is API list value, not a provider invoice. Subscription and
local usage are classified separately. Optional plan costs in
`~/.tokentelemetry/plans.json` are prorated over the matched activity window.
They do not establish actual invoiced spend or allocate a plan's cost to a
particular model or project.

Accuracy has limits:

- Codex JSONL readers prefer structured per-response usage identities. Legacy
  turns without those records retain timing-based fork replay detection, disclosed
  in the report.
- Hermes uses per-call log timestamps only when usage fully reconciles with an
  unambiguous database row. Otherwise it preserves the aggregate, labels its
  dates/cost as approximate, and uses base context rates. Auxiliary tasks,
  ambiguous routes and incomplete logs remain aggregates. Log timestamps use
  the local timezone.
- Usage older than available price history uses the earliest known rate, with
  the affected cost disclosed.
- Missing, unreadable or changed source logs can make reports incomplete.
  Compressed Codex rollouts are not currently read.
- Tests cover specific storage formats and accounting cases, not every provider
  version or billing arrangement.

See [the command reference](engine/README.md) for filters, plans and pricing.

## Development

```sh
make test
make check
make build
make packages  # optional: needs Node.js; builds six native npm packages + launcher
```

The Go module remains under `engine/`. Its historical module path is retained
for source compatibility. Generated npm packages live in `engine/dist/npm`;
this checkout does not establish that they have been published. Use the local
binary above to run this version.

## Project origins

This project began from Hemanth Vasi's TokenTelemetry application. This version
focuses on the Go CLI and removes the inherited Python backend, Next.js dashboard,
website, plugins and deployment tooling. The Git history retains that work and
its attribution. The original MIT notice remains in [LICENSE](LICENSE).

No existing user logs, configuration, databases or running services are migrated
or removed by this source-code change. The CLI reads original agent logs; it
does not import the old dashboard's retained history database.
