# TokenTelemetry CLI reference

The Go engine is the primary application. For installation, project origins and
accuracy limits, see the [project README](../README.md).

## Commands

```
summary                      overview with terminal charts; default command
 daily | weekly | monthly    usage over time
session | model | project    usage by dimension
price <model>                a model's rate history
agents                       detected agents and log paths
version                      build version
```

`summary` defaults to the last 30 local calendar days, including today. An explicit
`--since` or `--until` replaces that default. Other report commands default to all
available history. Usage logs stay local.

## Filters and output

```
--since / --until DATE   inclusive local-day bounds, YYYY-MM-DD
--agent NAME            repeatable, or comma-separated
--model NAME            repeatable, or comma-separated
--project NAME          full path or trailing folder name; repeatable
--subagents MODE        include (default) | only | exclude
--group-by DIMS         nested detailed reports: day, week, month, agent,
                        model, provider, project, session; "none" for flat
--compact               abbreviated counts in detailed tables
--verbose               scan and deduplication diagnostics
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

```sh
./dist/tokentelemetry summary --agent claude,codex
./dist/tokentelemetry summary --since 2026-08-01 --until 2026-08-31 --json
./dist/tokentelemetry daily --group-by agent,model
./dist/tokentelemetry session --project tokentelemetry --limit 10
```

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

## Development and packaging

From the repository root, run `make test`, `make check`, and `make build`.
`make packages` additionally requires Node.js and cross-compiles six native
packages plus the npm launcher into `engine/dist/npm`. Publishing is separate;
use the local binary to test this checkout. Distributed packages include the MIT
licence notice.

Readers currently cover Claude Code, Codex CLI, Gemini CLI, OpenCode, Hermes and Pi.
The scanner interface and registration live in `internal/ingest/ingest.go`.

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
