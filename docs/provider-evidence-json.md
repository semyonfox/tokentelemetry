# Provider evidence: JSON and SQLite readers

Sources reviewed 19 September 2026. Tests use synthetic records; the readers do
not launch providers or inspect credentials.

## OpenClaude

Official source was reviewed at
[`d16318a`](https://github.com/Gitlawb/openclaude/tree/d16318a47f48a7e6c674b1df9ccb4f3a873886d2).
[`envUtils.ts`](https://github.com/Gitlawb/openclaude/blob/d16318a47f48a7e6c674b1df9ccb4f3a873886d2/src/utils/envUtils.ts)
resolves `OPENCLAUDE_CONFIG_DIR` and its `projects` directory, while
[`sessionStorage.ts`](https://github.com/Gitlawb/openclaude/blob/d16318a47f48a7e6c674b1df9ccb4f3a873886d2/src/utils/sessionStorage.ts)
writes project session JSONL. The generated SDK usage type records disjoint
input, output, cache-creation and cache-read buckets. Native message IDs
deduplicate copied transcripts.

The reader also accepts `actualModel`/`actualmodel` route metadata seen in
compatible transcripts. The current official source does not promise those
fields as a stable persisted contract, so their support is compatibility code.
Absent model metadata remains unknown and unpriced.

## OpenClaw

Official source was reviewed at
[`a5fa4fe`](https://github.com/openclaw/openclaw/tree/a5fa4fee550363a7a3ef2f8494a4c27a1428d31f).
The [state-location documentation](https://github.com/openclaw/openclaw/blob/a5fa4fee550363a7a3ef2f8494a4c27a1428d31f/docs/help/faq/where-things-live-on-disk.md)
defines `OPENCLAW_STATE_DIR`, current per-agent `openclaw-agent.sqlite`, and
legacy session artifacts. The
[agent schema](https://github.com/openclaw/openclaw/blob/a5fa4fee550363a7a3ef2f8494a4c27a1428d31f/src/state/openclaw-agent-schema.sql)
defines `session_windows` and ordered `transcript_events`.

The reader handles current SQLite events and materialized or legacy JSONL.
Native entry or provider response IDs deduplicate overlapping representations.
Compressed cold archives that have not been materialized are outside this
reader.

## Oh My Pi

Official source was reviewed at
[`836048d`](https://github.com/can1357/oh-my-pi/tree/836048d81e088b4cddcd023780d6d769920e8525).
[`dirs.ts`](https://github.com/can1357/oh-my-pi/blob/836048d81e088b4cddcd023780d6d769920e8525/packages/utils/src/dirs.ts)
defines `PI_CODING_AGENT_DIR` and the `sessions` directory.
[`session-entries.ts`](https://github.com/can1357/oh-my-pi/blob/836048d81e088b4cddcd023780d6d769920e8525/packages/coding-agent/src/session/session-entries.ts)
defines session headers, assistant messages, explicit `model_usage` entries and
their measured usage. [`session-manager.ts`](https://github.com/can1357/oh-my-pi/blob/836048d81e088b4cddcd023780d6d769920e8525/packages/coding-agent/src/session/session-manager.ts)
shows that forks copy history and record the parent session.

The reader preserves provider/model changes and uses native entry or response
IDs across copied and forked storage. Older rows without native IDs fall back to
session-local ordering. It does not add inherited usage to a parent session.

## Droid

Factory's [settings documentation](https://docs.factory.ai/droid-cli/settings)
defines the `~/.factory` state root and model setting, but it does not publish
the persisted session token schema. The local `.settings.json` discovery and
`tokenUsage` shape therefore follow CodeBurn's
[`droid.ts` at `4cf1885`](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/droid.ts)
as a compatibility fallback.

Droid stores session totals, so the reader emits one aggregate instead of
spreading the total over messages. The recorded final model is retained, but
the aggregate is unpriced because `/model` can switch models between turns.
Aggregate sanitization allows legitimate totals above a single-call limit while
still rejecting corrupt counters.

## IBM Bob

IBM's [chat documentation](https://bob.ibm.com/docs/ide/features/chat-interface)
confirms that task history is local and subject to retention. IBM's community
[history-export thread](https://community.ibm.com/community/user/discussion/history-export)
documents the `IBM Bob`/`Bob-IDE` `globalStorage/ibm.bob-code/tasks` locations
and `ui_messages.json`. The persisted counter shape is not a provider-owned
public contract; compatibility follows the classic Cline task format also used
by CodeBurn's
[`ibm-bob.ts` at `4cf1885`](https://github.com/getagentseal/codeburn/blob/4cf18855939512605826cf6b0a47b6486b0c8619/src/providers/ibm-bob.ts).

Bob task metadata preserves only task-level configured model state. Measured
counters are retained, but every record is explicitly unpriced because a task
can switch models and the original per-call attribution cannot be recovered.
