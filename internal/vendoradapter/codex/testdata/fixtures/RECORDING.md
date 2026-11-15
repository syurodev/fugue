# Recording the Codex fixtures (VR-E02)

The fixtures under `internal/vendoradapter/codex/testdata/fixtures/` are the
stdout of the real Codex CLI (`codex app-server`), recorded 2026-11-14 from
`codex-cli 0.167.0` in a throwaway temporary directory with `ephemeral: true`
and redacted before commit. `TestVRE02CodexFixturesReplayToGoldenTranscript`
replays them through the codec and normalizer against the golden transcripts;
CI needs no account and no installed CLI.

## Fixtures

1. `codex-0.167.0-session1.jsonl` (and `.turns.json`):
   - `initialize` handshake and `account/rateLimits/read` rate-limit read.
   - `thread/start` with default approval policy (`on-request`).
   - Turn 1: plain assistant answer (`Reply with only the single word PONG.` -> `PONG`) with `item/agentMessage/delta`, `thread/tokenUsage/updated`, and `account/rateLimits/updated`.
   - Turn 2: shell command execution (`printf hello-codex` -> `commandExecution` `item/started` and `item/completed` with `exitCode: 0`, `durationMs: 0`, `aggregatedOutput: "hello-codex"`).
   - Turn 3: file edit (`apply_patch` creating `/home/REDACTED/codex-fixture/note.txt` with `alpha\n` -> `fileChange` `item/started` and `item/completed`).
2. `codex-0.167.0-session2-interrupt.jsonl` (and `.turns.json`):
   - `initialize`, `thread/start`, and Turn 1 running `sleep 5`, interrupted mid-turn via `turn/interrupt` -> `turn/completed` with `status: "interrupted"`.
3. `codex-0.167.0-session3-approval.jsonl` (and `.turns.json`):
   - `initialize`, `thread/start`, and Turn 1 requesting an escalated command (`printf approved`), which emits the server request `item/commandExecution/requestApproval` (`id: 0`).
   - In the recording only, the request was declined by hand (`{"id":0,"result":{"decision":"cancel"}}`), producing `serverRequest/resolved`, `item/completed` with `status: "declined"`, and `turn/completed`.

## Redaction (applied before commit; verified by `TestCodexFixturesContainNoHostIdentity`)

1. Every UUID becomes `uuid-NNNN` in order of first appearance within the file.
2. Long item/message/call identifiers (`msg_…`, `item_…`, `call_…`) become `msg-NNNN`, `item-NNNN`, `call-NNNN`.
3. Throwaway working and config directories become `/home/REDACTED/codex-fixture` and `/home/REDACTED/.codex`.
4. Host/machine names (`serverName`, `userAgent` OS strings, MCP server names) become placeholders (`host.example`, `codex_cli_rs/0.167.0 (Linux; x86_64) terminal`, `example-mcp`).
