# TokenTelemetry

The product is a local Go CLI. Source, tests and native packaging live in
`engine/`. GitHub workflows live in `.github/`.

## Commands

- `make build`: compile `dist/tokentelemetry`.
- `make test`: run Go tests.
- `make check`: run Go vet.
- `make run ARGS='summary --plain'`: run from source.
- `make packages`: generate native npm packages locally, without publishing.

See `.claude/CLAUDE.md` for accounting and privacy rules.
