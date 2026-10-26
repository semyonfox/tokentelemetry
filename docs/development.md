# Development notes

Run commands from the repository root:

```sh
make test
make check
make build
```

The Go module is under `engine/`. `make packages` builds the local native npm
packages when Node.js is available. It does not publish anything.

## Releases

Releases need Go, `tar`, `zip`, `sha256sum` and an authenticated `gh`, not CI.

```sh
make release TAG=v0.2.0   # archives and checksums.txt in dist/release
```

To publish, rename the `## Unreleased` heading in `CHANGELOG.md` to the version,
merge, then tag `main`. The `engine/` tag lets `go install ...@latest` resolve
the module, which lives in a subdirectory.

```sh
git tag v0.2.0 && git tag engine/v0.2.0
git push origin v0.2.0 engine/v0.2.0
make publish TAG=v0.2.0
```

Pushing a `v*` tag also runs the release workflow, which calls the same script.
Whichever runs first publishes, and the other leaves the existing release alone.

| Area | Code to read first |
| --- | --- |
| Command routing | `engine/internal/cli/cli.go` chooses a command and reports errors. |
| Help and flags | `engine/internal/cli/help.go` defines commands and focused help. `options.go` parses and validates report flags. |
| Command work | `report.go` scans and renders reports. `agents.go` lists catalog entries. `price.go` reads rate history. `output.go` selects terminal output behavior. |
| Provider registration and discovery | `engine/internal/ingest/registry.go` owns identifiers, constructors, coverage and source selection. Provider files keep their source-specific parsing and accounting contracts. |
| Scan execution and reconciliation | `engine/internal/ingest/ingest.go` runs readers, removes duplicates and excludes proven runtime overlaps. |
| Scan caching | `engine/internal/ingest/cache.go` stores parsed usage and checks source metadata before reuse. Prices and global reconciliation run again for each report. |
| Reporting | `engine/internal/report/` filters normalized turns and builds report rows. |
| Pricing | `engine/internal/pricing/` loads embedded effective-dated rates and local overrides. |

Readers must use synthetic fixtures. Do not inspect personal logs for tests.
Keep unknown models unpriced, preserve distinct token buckets, and treat API list
value as separate from invoices or subscription costs. The detailed source
contracts and limits are in the [provider evidence notes](README.md).
