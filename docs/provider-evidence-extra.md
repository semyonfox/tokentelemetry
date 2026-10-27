# Additional provider evidence

## Amp

Amp keeps one JSON file per thread beneath `~/.local/share/amp/threads`
(`$XDG_DATA_HOME/amp` when set). `AMP_DATA_DIR` replaces that with one or more
comma-separated roots, matching [ccusage's Amp adapter](https://github.com/ryoppippi/ccusage/tree/main/rust/adapters/amp),
which is MIT licensed and was the reference for the file layout and field names.

Assistant messages carry Anthropic-shaped usage. `inputTokens` is net of cache:
the ccusage fixture records `totalInputTokens` 12,368 for input 10, cache
writes 986 and cache reads 11,372. Those messages are the measured per-call
source.

A thread can also hold `usageLedger.events`. An event whose `toMessageId`
matches an assistant message only adds its credits, because the message
already counts the tokens. ccusage instead prefers the ledger and copies cache
counts back from the message, which gives the same tokens for matched events.
An unmatched event is a separate call, often another model, and counts on its
own. The ledger records no cache split, so whether its `input` is gross could
not be confirmed from first-party sources. A message or event that records
only `totalTokens` stays unclassified and unpriced rather than being moved into
output. Credits stay in their own unit.

The message id within a thread forms the identity, so archive copies of a
thread across several roots are counted once. Whether Amp's thread forks copy
earlier messages was not verified.

## Kiro CLI

Kiro stores CLI sessions beneath `~/.kiro/sessions/cli`. A session's companion
JSON metadata contains `session_id`, `cwd`, routed model state, and
`conversation_metadata.user_turn_metadatas`. Each completed user-turn metadata
entry can carry `metering_usage` values whose unit is `credit`.

The reader imports only finite, positive values explicitly labelled `credit`.
It emits no token counts and performs no conversion to dollars because Kiro
does not persist a token breakdown or a stable per-credit dollar value there.
Empty arrays are in-progress/unmetered turns and are ignored. The session and
turn index form the stable identity, so rewritten cumulative metadata snapshots
replace the same turn instead of being added again.

Kiro IDE v1/v2 also has credit summaries, but those stores use different event
formats. They remain unsupported until each current schema can be verified
against primary evidence; the CLI reader does not guess across those formats.
