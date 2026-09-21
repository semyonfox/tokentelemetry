# Warp and DeepSeek Harness reader evidence

This note records the source contracts used by the native Go readers. The
fixtures in the Go tests are synthetic. No local user logs or credentials were
read or copied.

## Warp

Warp is closed source, so its local SQLite database is the native source. The
reader uses only `agent_conversations.conversation_id`, `conversation_data`,
and `last_modified_at`. In `conversation_data`, the measured accounting lives
at `conversation_usage_metadata.token_usage`. Each entry identifies a model
and records `warp_tokens` and `byok_tokens`.

The audit also checked CodeBurn commit
[`4cf18855939512605826cf6b0a47b6486b0c8619`](https://github.com/getagentseal/codeburn/tree/4cf18855939512605826cf6b0a47b6486b0c8619).
Its [`warp.ts`](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/warp.ts)
confirms the database paths, JSON fields, and the lack of a reliable
input/output split. That implementation estimates exchange shares from prompt
length. TokenTelemetry deliberately does not carry that estimate across.

Contract:

- `WARP_DB_PATH` overrides discovery. Otherwise Stable is preferred, then
  Preview, under Warp's macOS group container. The audited sources do not
  publish an equivalent Linux database location, so Linux requires the
  override rather than a guessed path.
- The database opens with SQLite `mode=ro`.
- `warp_tokens + byok_tokens` is preserved once per conversation and model.
  Repeated rows for one model are summed before emission.
- The total is `Usage.Unclassified`. The turn is aggregate and explicitly
  unpriced because Warp supplies no trustworthy input/output/cache split.
- Identity is `(conversation_id, model_id)`. `last_modified_at` supplies the
  aggregate attribution time. Missing or malformed time is diagnosed, but the
  measured unpriced total is retained with an undated timestamp.

The reader does not inspect prompts, commands, blocks, or transcript text.

## DeepSeek Harness

The primary audit used DeepSeek Harness commit
[`c291e7961a515f6d7af9304e7fd1d257929aef26`](https://github.com/deepseek-ai/deepseek-harness/tree/c291e7961a515f6d7af9304e7fd1d257929aef26).

The relevant upstream contracts are:

- [`session-persistence-jsonl/src/zstd.ts`](https://github.com/deepseek-ai/deepseek-harness/blob/c291e7961a515f6d7af9304e7fd1d257929aef26/packages/session/session-persistence-jsonl/src/zstd.ts)
  defines the concatenated independent-frame container and treats an
  incomplete final frame as a torn append.
- [`session/src/types.ts`](https://github.com/deepseek-ai/deepseek-harness/blob/c291e7961a515f6d7af9304e7fd1d257929aef26/packages/core/session/src/types.ts)
  defines format v3 and the `assistant/message` and `assistant/attempt` event
  payloads. Released predecessor formats v0 through v2 are also handled.
- [The TokenUsage contract](https://github.com/deepseek-ai/deepseek-harness/blob/c291e7961a515f6d7af9304e7fd1d257929aef26/docs/subsystems/llm-streaming.md#tokenusage)
  states that input, cache-read, and cache-write counts are disjoint. Reasoning
  is already included in output and must not be added to totals again.

CodeBurn's pinned [`dsh.ts`](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/dsh.ts)
was used as a second implementation of version selection, inherited-history
cuts, model precedence, and retry settlement.

Contract:

- Discovery reads `<DSH_HOME|~/.dsh>/sessions/<encoded-cwd>/<session>/` and
  selects the highest `session[.vN].jsonl[.zstd]` generation in each session
  directory. It never falls back when that generation is unknown or corrupt.
- Supported released versions are v0 through v3. The filename generation must
  match the first JSONL header.
- Every complete zstd frame is scanned and decoded separately. Encoded files,
  decoded files, and individual decoded frames have fixed bounds. A torn final
  frame is ignored before parsing; structural corruption rejects the session.
- v0 and v1 use top-level `assistant/chunk` usage. A following
  `assistant/message` replaces that draft observation. v2 and v3 use embedded
  streams on `assistant/attempt` and `assistant/message`, with message-level
  usage taking precedence.
- `llm/retry-started` opens a new attempt. Identity is
  `(session id, turn, step, attempt)`, so drafts and final messages cannot
  duplicate one attempt while retries remain separate measured calls.
- The served model on `assistant/message` wins. `request/context`, then
  `request/header`, supply fallbacks.
- Fork seed events are excluded using the v0/v1 `parentSession` and
  `seedLength` contract or the v2/v3 inherited `session/end-seed` marker.

The reader supports the default JSONL backend. It does not read the optional
DSH SQLite persistence backend.
