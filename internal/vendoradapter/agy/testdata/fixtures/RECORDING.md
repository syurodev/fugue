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
5. `agy-1.5.9-session5-permission-reject.jsonl` (and `.turns.json`):
   - `initialize`, `session/new`, `available_commands_update`, and Turn 1 emitting `session/request_permission` (`id: "1"`). Answered `reject_once` (`optionId: "deny"`), producing `tool_call_update` with `status: "failed"` (`permission denied for tool run_command`) and `end_turn`.
6. `agy-1.5.9-session6-permission-cancel.jsonl` (and `.turns.json`):
   - `initialize`, `session/new`, `available_commands_update`, and Turn 1 emitting `session/request_permission` (`id: "1"`). Cancelled via `session/cancel` (`{"outcome":"cancelled"}`), returning `stopReason: "cancelled"`.

## Recording a Fugue tool-call fixture (F9.11)

7. `agy-1.5.10-session7-fugue-tool.jsonl` (and `.turns.json`):
   - `initialize`, `session/new` (the `mcpServers` entry carries the fugue bridge), the model switch by `session/set_config_option` (`currentValue` `gemini-4-hydrogen`), and one turn in which the model calls `fugue_test_echo` twice through the real `fugue mcp-bridge` (`tool_call` named `mcp_fugue_fugue_test_echo`, `session/request_permission` answered `allow`, `completed`), then answers `DONE` and ends with `end_turn`.
   - Recorded 2026-11-17 from `agy 1.5.10`, the version installed that day, not 1.5.9. The `models.currentModelId` in the `session/new` result still names the account default (Gemini 4 Argon); the model the turn ran on is the one in the `config_option_update`.

`go test -tags fugue_record -run TestRecordF911Fixtures ./internal/hostapi/` with `FUGUE_RECORD_AGY=1`
records a real `agy acp` session whose `session/new` carries the fugue bridge in `mcpServers` (no env
pairs; the bridge inherits agy's environment) and prompts the model to call `fugue_test_echo`.

- Model: the recorder selects the model with ACP `session/set_config_option` (`configId: "model"`, the call
  the agy adapter makes), taken from `FUGUE_RECORD_AGY_MODEL`, default `gemini-4-hydrogen`. Any value
  containing `argon` is refused: the Argon models were out of weekly quota on the recording account
  (2026-11-17), and a quota error is never recorded. This fixture was recorded with `gemini-4-hydrogen`.
- Permission answer: agy 1.5.10 refuses the flat `{"outcome":"selected","optionId":...}` form with
  `cannot unmarshal string into ... outcome of type AllowedOutcome` and fails the tool call before it
  reaches the bridge, so the recorder answers the nested `{"outcome":{"outcome":"selected","optionId":"allow"}}`.
  The agy adapter still sends the flat form (measured on 1.5.9).
- Generate the golden with `go test -run TestVRE03AgyFixturesReplayToGoldenTranscript ./internal/vendoradapter/agy -update`.

## Redaction (applied before commit; verified by `TestAgyFixturesContainNoHostIdentity`)

1. Every UUID becomes `session-uuid-NNNN` in order of first appearance within the file.
2. Every tool call identifier (`call_…`) becomes `call-NNNN`.
3. Throwaway temporary directories and host home paths become `/home/REDACTED/agy-fixture` and `/home/REDACTED`.
