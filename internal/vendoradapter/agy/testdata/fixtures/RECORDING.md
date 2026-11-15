# Recording the Antigravity fixtures (VR-E03)

The fixtures under `internal/vendoradapter/agy/testdata/fixtures/` are the
stdout of the real Antigravity CLI (`agy acp`), recorded 2026-11-15 from
`agy 1.5.9` in throwaway temporary working directories and redacted before
commit. `TestVRE03AgyFixturesReplayToGoldenTranscript` replays them through the
codec and normalizer against the golden transcripts; CI needs no account and
no installed CLI.

## Fixtures

1. `agy-1.5.9-session1.jsonl` (and `.turns.json`):
   - `initialize` handshake (protocolVersion 1, §5.3 capabilities, `_meta.antigravity`) and `session/new` (models, modes, configOptions, `_meta.antigravity`).
   - `session/set_config_option` switching to `gemini-3.8-flash` (`config_option_update`), `available_commands_update`, `usage_update`.
   - Turn 1: prompt creating `note.txt` and running `printf hello-agy` with `user_message_chunk`, `session_info_update`, `agent_thought_chunk`, `tool_call` (`run_command`, `kind: execute`), `tool_call_update` (`pending` -> `in_progress` -> `completed` with output), `session/request_permission` (during recording answered `allow`), `agent_message_chunk` stream, `usage_update`, and `end_turn` result.
2. `agy-1.5.9-session2-cancel.jsonl` (and `.turns.json`):
   - `initialize`, `session/new`, and Turn 1 running `sleep 5`, cancelled mid-thought via `session/cancel`, returning `stopReason: "cancelled"`.
3. `agy-1.5.9-session3-permission.jsonl` (and `.turns.json`):
   - `initialize`, `session/new`, and Turn 1 emitting `session/request_permission` (`id: "1"`). During recording the harness returned JSON-RPC error `-32601` (`permission relay is not supported yet`), producing `tool_call_update` with `status: "failed"` and `end_turn`.
4. `agy-1.5.9-session4-load.jsonl` (and `.turns.json`):
   - `initialize` and `session/load` of the cancelled session, replaying `user_message_chunk` and `agent_thought_chunk` notifications plus the `session/load` result and `session_info_update`.

## Redaction (applied before commit; verified by `TestAgyFixturesContainNoHostIdentity`)

1. Every UUID becomes `session-uuid-NNNN` in order of first appearance within the file.
2. Every tool call identifier (`call_…`) becomes `call-NNNN`.
3. Throwaway temporary directories and host home paths become `/home/REDACTED/agy-fixture` and `/home/REDACTED`.
