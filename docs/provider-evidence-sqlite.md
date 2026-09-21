# SQLite provider evidence

The readers use only measured values. They open databases read-only with a
three-second busy timeout so live WAL databases remain readable without
modifying provider state.

## Forge

Source checked at `tailcallhq/forgecode` commit
`259967395f9a7f02743e56c2c9b70273bb6ad49a`:

- `crates/forge_config/src/reader.rs` defines `FORGE_CONFIG`, `~/forge`, and
  `~/.forge` base-directory selection.
- `crates/forge_domain/src/env.rs` places the database at `.forge.db`.
- `crates/forge_repo/src/database/migrations/2025-09-12-065405_create_conversations_table/up.sql`
  defines the conversation row.
- `crates/forge_repo/src/conversation/conversation_record.rs` defines message usage as
  prompt, completion, total, and cached token-count records. The official
  conversation fixture confirms the serialized shape and role/model fields.

Only `actual` token counts are imported. `approx` counts are estimates and are
discarded. Prompt tokens include cached tokens in Forge's provider adapters,
so cached tokens are subtracted to produce net input. Forge stores one update
time for the conversation rather than a time for each message, so imported records are labeled aggregates. Project grouping uses the opaque workspace ID; conversation titles are not project paths.

## Goose

Source checked at `block/goose` commit
`2090ad1c65ddb39497601a936a9fe17d66254bfe`:

- `crates/goose/src/config/paths.rs` defines `GOOSE_PATH_ROOT` and the platform
  data-directory rules.
- `crates/goose/src/session/session_manager.rs` defines `sessions.db` and the
  accumulated token columns.
- `crates/goose-provider-types/src/conversation/token_usage.rs` documents
  `input_tokens` as the total input including cache read and cache write.
- `crates/goose-provider-types/src/model.rs` defines serialized `model_name`.

Goose persists session totals, so each row is marked as an aggregate. When
both cache columns are present, the reader subtracts them from gross input.
When either is null, measured input is retained as unclassified and the turn
is explicitly unpriced; assigning it to fresh input would overstate cost. All Goose session costs remain excluded because the saved model configuration can change during a session.

## Zed

Source checked at `zed-industries/zed` commit
`526c95d474b2bf800e95aab30222fa0341f08e19`:

- `crates/agent/src/db.rs` defines `threads.db`, the `json` and `zstd` data
  types, `request_token_usage`, `cumulative_token_usage`, and model identity.
- `crates/language_model_core/src/language_model_core.rs` defines the four
  disjoint token fields: input, output, cache creation, and cache read.

Zstd frames and decoded JSON are capped at 64 MiB. Request records have no
individual timestamp; they use the thread update time and are marked as
aggregates. The model tag belongs to the current thread rather than each historical request, so cost is excluded. A cumulative remainder is emitted only after every token bucket
reconciles with the request map. Folder-path serialization is intentionally
not treated as a project path without a stable upstream contract.

## ZCode

ZCode's runtime is not published as source. The official `zai-org/feedback`
issue 564 confirms the current `~/.zcode/cli/db/db.sqlite` location and
`model_usage` table. The remaining column contract follows the observed 0.14.8 schema in
[CodeBurn's ZCode adapter](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/zcode.ts).
Tests use synthetic databases, not an installed client's personal logs. The reader requires all
of those columns and returns an unsupported-schema error on drift.

ZCode records gross input plus cache creation/read subsets. The reader
subtracts both subsets for net input. The database does not record a provider
identifier for these rows, so provider remains empty rather than inferred.

## Crush limitation

Crush's [session type](https://github.com/charmbracelet/crush/blob/main/internal/session/session.go) and [agent accounting](https://github.com/charmbracelet/crush/blob/main/internal/agent/agent.go) distinguish the latest prompt/context token counts from accumulated cost. Its persisted messages do not retain a measured per-call token ledger. Treating the session counters as total historical usage would undercount; deriving tokens from text would invent measurements. `agents` therefore lists Crush as source-limited, and selecting it explains why no token reader is registered.

## Kilo Code

The current Kilo SQLite reader uses per-message model/provider and token buckets, with modern `session_message` rows masking frozen legacy `message` rows of the same ID. The [comparison's Kilo analysis](provider-port-plan.md) records the primary Kilo source and schema references. Native request IDs preserve older unmigrated history without counting migrated messages twice.
