# Development notes

Run commands from the repository root:

```sh
make test
make check
make build
```

The Go module is under `engine/`. `make packages` builds the local native npm
packages when Node.js is available. It does not publish anything.

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
