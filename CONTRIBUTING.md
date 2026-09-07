# Contributing

The product is a local Go CLI. Implementation and tests live under `engine/`.

Run `make test`, `make check` and `make build` before proposing changes. For
accounting changes, add a small synthetic fixture with independently calculated
token counts and costs. Never commit personal transcripts, machine identifiers
or credentials.

Keep provider-specific parsing in `internal/ingest`, pricing in
`internal/pricing` and `internal/cost`, aggregation in `internal/report`, and
terminal output in `internal/cli`. Unknown prices must remain explicit.

Native npm packages are generated with `make packages`; publishing is a separate
maintainer action. Preserve the MIT licence notice in distributed packages.
