# Additional provider evidence

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
