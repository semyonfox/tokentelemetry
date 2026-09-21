# CLI provider evidence

Research snapshot: 19 September 2026. These readers use only local files and
do not read credentials or call provider APIs. Tests construct synthetic stores.

## Qwen Code

- Contract inspected: Qwen Code
  [`ChatRecord`](https://github.com/QwenLM/qwen-code/blob/c1c00cbaab57177d6ea7c7876bd95d23bc9d2453/packages/core/src/services/chatRecordingService.ts),
  which records `sessionId`, `uuid`, `model`, `usageMetadata`, `cwd`, sidechain
  attribution and `forkedFrom` identity in the append-only chat JSONL.
- Source: `~/.qwen/projects/*/chats/**/*.jsonl`; `QWEN_HOME` and
  `QWEN_DATA_DIR` relocation are supported.
- Accounting: `promptTokenCount` is gross input, so
  `cachedContentTokenCount` is subtracted into the cache-read bucket.
  `thoughtsTokenCount` is included in output and retained as its reasoning
  subset. Fork copies deduplicate by their recorded origin.
- Limits: records without usage are ignored. A missing model is retained as
  `unknown` and explicitly unpriced. No text-derived token estimates are used.

## Kimi CLI (legacy Python client)

- Contract inspected: Kimi CLI
  [`StatusUpdate`](https://github.com/MoonshotAI/kimi-cli/blob/86f136422a0aae6b217ea49e7ea1d2e8a1defcd2/src/kimi_cli/wire/types.py#L164-L178),
  whose `token_usage` is the measured usage for the current step, and its
  [`WireFile`](https://github.com/MoonshotAI/kimi-cli/blob/86f136422a0aae6b217ea49e7ea1d2e8a1defcd2/src/kimi_cli/wire/file.py#L27-L40),
  whose [append path](https://github.com/MoonshotAI/kimi-cli/blob/86f136422a0aae6b217ea49e7ea1d2e8a1defcd2/src/kimi_cli/wire/file.py#L117-L121)
  persists fractional `time.time()` seconds.
- Source: `~/.kimi/sessions/*/*/wire.jsonl` and each session's
  `subagents/*/wire.jsonl`; `KIMI_SHARE_DIR` is supported.
- Accounting: `input_other`, `output`, `input_cache_read`, and
  `input_cache_creation` are already disjoint and map directly to
  TokenTelemetry's four buckets. Timestamps accept the native fractional Unix
  seconds as well as older string and integer forms.
- Limits: the wire protocol normally does not identify the historical request
  model. The reader uses a model only when the same event records it; otherwise
  the turn is `unknown` and unpriced. It does not apply today's configured
  default to old calls.

## Kimi Code (TypeScript client)

- Contracts inspected at `99eaa993bad28e2074fcf29f8b76c0ccf0f02b65`:
  [`usage.record` wire manifest](https://github.com/MoonshotAI/kimi-code/blob/99eaa993bad28e2074fcf29f8b76c0ccf0f02b65/packages/agent-core-v2/docs/wire-manifest.d.ts#L878-L890)
  plus the [usage recorder](https://github.com/MoonshotAI/kimi-code/blob/99eaa993bad28e2074fcf29f8b76c0ccf0f02b65/packages/agent-core-v2/src/session/usage/usageAgentModel.ts#L28-L48),
  and the provider-normalized
  [`TokenUsage`](https://github.com/MoonshotAI/kimi-code/blob/99eaa993bad28e2074fcf29f8b76c0ccf0f02b65/packages/kosong/src/usage.ts).
- Source: `~/.kimi-code/sessions/*/*/agents/*/wire.jsonl`, with relocation by
  `KIMI_CODE_HOME`.
- Accounting: every `usage.record` is one generation and is paired with a
  preceding `llm.request` from the same agent wire. `usageScope` identifies
  whether the generation belongs to a turn or session-level work such as
  compaction; it is not a cumulative counter. The request supplies the actual
  model; the usage fields are the same four disjoint buckets as legacy Kimi.
- Limits: a crashed or stalled request can have `llm.request` without a later
  usage record and is therefore absent. Both event types carry `agentId`.
  Foreign mirrored events are ignored before correlation, so a child event
  cannot replace or consume an outstanding parent request.

## Mistral Vibe

- Contracts inspected at `c069ffa1e12fb5f2487b489217c40ab97721d553`:
  [`AgentStats`](https://github.com/mistralai/mistral-vibe/blob/c069ffa1e12fb5f2487b489217c40ab97721d553/vibe/core/types.py#L51-L68)
  records cumulative session prompt, completion, and cached tokens. The
  [official README](https://github.com/mistralai/mistral-vibe/blob/c069ffa1e12fb5f2487b489217c40ab97721d553/README.md)
  documents `VIBE_HOME` and session logging.
- Source: `$VIBE_HOME/logs/session/**/meta.json`, defaulting to
  `~/.vibe/logs/session`.
- Accounting: one `Aggregate` turn is emitted for each session or saved
  subagent. Current metadata splits gross prompt tokens into fresh input and
  cache reads. Historical metadata without `session_cached_tokens` keeps the
  prompt count in `Unclassified` and excludes it from pricing.
- Limits: Vibe does not persist request-level usage in `messages.jsonl`, so the
  aggregate's timestamp is the session end (or start) and per-call/day/model
  detail cannot be reconstructed. All Vibe aggregates remain unpriced because the final model configuration cannot
  establish historical model switches. Recorded `session_cost` is not mapped
  because TokenTelemetry does not yet have a recorded-charge field.

The implementation also compared these contracts with CodeBurn 0.9.24 at
`4cf18855939512605826cf6b0a47b6486b0c8619`. That parser was treated as
secondary evidence: its guessed model fallbacks, per-message allocation of Vibe
session totals, and character-derived estimates were not ported.
