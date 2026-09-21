# Shared runtimes and overlapping histories

A model or subscription is not a request identity. Independent Hermes, OpenCode and Codex calls to the same model remain separate usage.

Runtime overlap is resolved within the report's filtered records. A linked wrapper record is excluded only when native usage for the exact executor session also passes the current agent, model, project, date and subagent filters. Selecting or filtering down to the wrapper therefore keeps its source usage.

## Hermes driving Codex

Primary source checked at [Hermes 8a92051](https://github.com/NousResearch/hermes-agent/tree/8a92051f20e6b371c4ff1a46a5bcec7138cc4e8c). The [runtime](https://github.com/NousResearch/hermes-agent/blob/8a92051f20e6b371c4ff1a46a5bcec7138cc4e8c/agent/codex_runtime.py) distinguishes direct Responses API calls from the optional Codex app-server subprocess. The latter mirrors native usage into Hermes accounting and persists `sessions.model_config.codex_thread_id` after a durable turn. The [session transport](https://github.com/NousResearch/hermes-agent/blob/8a92051f20e6b371c4ff1a46a5bcec7138cc4e8c/agent/transports/codex_app_server_session.py) launches/resumes that native Codex thread.

The reader extracts only the stored thread ID from the SQLite JSON column. It does not read the configuration blob or credentials. A main OpenAI aggregate with that link carries `runtime_agent` and `runtime_session_id`. If native Codex usage for that exact thread is also scanned, the wrapper aggregate is excluded from combined totals and preserved in JSON `excluded_overlaps`, with its original usage and an `excluded_reason`. Terminal reports disclose the exclusion. Native Codex is counted under Codex.

The aggregate may mix direct calls, earlier runtime threads and mirrored usage. No token-by-token residual is invented. Consequently the combined report can be incomplete for that mixed session; the excluded record remains available to inspect. `--agent hermes` alone shows Hermes's source totals because no native Codex ledger is being combined with them. Auxiliary task rows and fully reconciled normal-loop API calls remain independent and count normally.

Older Hermes versions without the persisted link, deleted Codex rollouts and custom wrappers without shared identities cannot be reconciled automatically. Model names, timestamps, matching token counts and eight-character log prefixes are not used as proof of duplicate requests. These limits prevent a universal claim that arbitrary wrapper imports cannot overlap.

Synthetic regression tests cover the explicit link, no link, different native thread, auxiliary calls, normal-loop direct calls and selecting Hermes alone. They also prove that equal token counts and model names across Hermes/OpenCode/Codex are retained as independent calls.

## OpenClaw driving Codex

Primary source checked at [OpenClaw a5fa4fee](https://github.com/openclaw/openclaw/tree/a5fa4fee550363a7a3ef2f8494a4c27a1428d31f). The [Codex harness documentation](https://github.com/openclaw/openclaw/blob/a5fa4fee550363a7a3ef2f8494a4c27a1428d31f/docs/plugins/codex-harness.md) says Codex app-server owns native thread execution while OpenClaw keeps the visible transcript mirror. OpenClaw's [failure-recovery test](https://github.com/openclaw/openclaw/blob/a5fa4fee550363a7a3ef2f8494a4c27a1428d31f/src/gateway/server.codex-failure-recovery.test.ts#L374-L393) verifies that the Codex plugin persists an active `sessionId` to native `binding.threadId` mapping in its `app-server-thread-bindings` namespace. The per-agent [session window schema](https://github.com/openclaw/openclaw/blob/a5fa4fee550363a7a3ef2f8494a4c27a1428d31f/src/state/openclaw-agent-schema.sql#L125-L151) separately records `agent_harness_id`.

The reader queries only those three binding fields from the shared plugin-state database. It attaches a Codex runtime link only when the binding is active, its OpenClaw session ID exactly matches the transcript window, and that window records the `codex` harness. Expired, malformed, conflicting, inactive and unmatched bindings are ignored. The link does not make a turn an aggregate. Combined accounting excludes linked OpenClaw usage only when native Codex history for the exact thread is also scanned; otherwise the OpenClaw turn remains counted.

`appServer.homeScope: "agent"` keeps the native Codex store isolated per OpenClaw agent by default. The documented [`"user"` scope](https://github.com/openclaw/openclaw/blob/a5fa4fee550363a7a3ef2f8494a4c27a1428d31f/docs/plugins/codex-harness/config-fields.md#L28-L36) explicitly shares `$CODEX_HOME` or `~/.codex`, which is the ordinary combined-scan overlap. The identity rule also handles another configured Codex home if both ledgers are deliberately scanned.

The window marker describes the persisted session window, not the runtime identity of every historical message. A window that switched harnesses may therefore contain older embedded calls mixed with its current Codex mirror. TokenTelemetry does not split that history using timestamps, models or token similarity. Excluded OpenClaw records retain their original per-message usage in `excluded_overlaps` so this limitation remains visible and auditable.

## Direct Codex API integrations

OpenCode's [native Codex plugin](https://github.com/anomalyco/opencode/blob/83abc64a5c4e0e0a5157f2c4435d34131009a404/packages/opencode/src/plugin/openai/codex.ts) sends requests directly to the Responses endpoint. It does not spawn the Codex CLI or write native rollouts. Sharing a subscription/backend therefore does not create duplicated Codex records.

Kilo's [corresponding plugin](https://github.com/Kilo-Org/kilocode/blob/010f511d731df2649bb3f9b660e309ea7e869ac8/packages/opencode/src/plugin/openai/codex.ts), OpenClaude's [Codex shim](https://github.com/Gitlawb/openclaude/blob/d16318a47f48a7e6c674b1df9ccb4f3a873886d2/src/services/api/codexShim.ts), and OMP's [provider](https://github.com/can1357/oh-my-pi/blob/836048d81e088b4cddcd023780d6d769920e8525/packages/ai/src/providers/openai-codex-responses.ts) likewise use direct API calls. They remain independent accounting sources. Kilo and OMP's native Codex history importers store imported assistant usage as zero; subsequent requests are new usage.

## Pi and Oh My Pi sharing a directory

OMP's [directory contract](https://github.com/can1357/oh-my-pi/blob/836048d81e088b4cddcd023780d6d769920e8525/packages/utils/src/dirs.ts) accepts `PI_CODING_AGENT_DIR`. If this points at Pi's default directory, both readers could otherwise consume the same files under different agent namespaces. When both scanners resolve to the same physical session directory, the OMP superset parser scans it once and a diagnostic explains why Pi was skipped. Separate directories remain separate. Explicitly selecting only Pi still uses the Pi reader.
