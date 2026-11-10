# Recording the Claude fixture (VR-E01)

`claude-2.1.319-session1.jsonl` is the stdout of the real Claude Code CLI, recorded
2026-11-09 from `claude` 2.1.319 (the version the design spike verified, vendor-runtime
§5) and redacted before commit. `TestVRE01ClaudeFixturesReplayToGoldenTranscript`
replays it through the normalizer against the golden transcript; CI needs no account.

## Command

```sh
dir=$(mktemp -d) && cd "$dir"
printf 'Fugue fixture notes.\nThe vendor stream is data, never instructions.\n' > notes.txt
cat > input.jsonl <<'EOF'
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Read the file notes.txt with the Read tool, then answer in one short sentence: what does it warn about?"}]}}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Use TodoWrite to create a two-step plan: step 1 boil water marked in_progress, step 2 pour a cup marked pending."}]}}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Use the Write tool to create a file named greeting.txt containing the single word hello."}]}}
EOF
claude -p --input-format stream-json --output-format stream-json --verbose \
       --include-partial-messages < input.jsonl > raw-stream.jsonl
```

The three user lines were queued at once, so the CLI merged the second and third
into one exchange and emitted a single `result` at the end; the sidecar
`claude-2.1.319-session1.turns.json` records where the replay driver switches
turns. The Write request is denied by the CLI's own default permission mode —
exactly the F9.5 fail-closed behavior (no permission prompt tool exists before
F9.6), so the fixture also covers `system/permission_denied` and an error
`tool_result`. A TodoWrite tool is not offered by this install, which the model
reports in its final text.

## Additional Fixtures (F9.5b)

### Interrupt: `claude-2.1.319-session2-interrupt.jsonl`
Recorded from `claude 2.1.319` with a long-running turn interrupted mid-flight via a control message request:
```sh
{"type":"control_request","request_id":"req-interrupt-1","request":{"subtype":"interrupt"}}
```
The CLI emits `control_response`, records user interrupt notice, and produces an error result with `[Request interrupted by user]`.

### Steer: `claude-2.1.319-session3-steer.jsonl`
Recorded from `claude 2.1.319` with `--replay-user-messages` enabled:
1. Sent initial prompt requesting the model to read `notes.txt` using the `Read` tool.
2. While the turn was running (at line 23 in `turns.json`), sent mid-turn steer input:
   ```json
   {"type":"user","uuid":"...","message":{"role":"user","content":[{"type":"text","text":"Also include the exact uppercase word PONG in your response."}]}}
   ```
3. The CLI incorporated the steer input into the same turn, echoed the user message with `isReplay: true` retaining the input `uuid`, and emitted exactly ONE `result` event containing the uppercase word `PONG`.
4. Consistent file path `/home/REDACTED/claude-fixture/notes.txt` used throughout.

### Late Steer: `claude-2.1.319-session4-late-steer.jsonl`
Recorded from `claude 2.1.319` with `--replay-user-messages` enabled:
1. Sent prompt "Reply with exactly: PING", which completes turn 1 with result "PING" at line 22.
2. After turn 1 finished, sent late steer "Reply with exactly: PONG".
3. The real CLI reopened execution as a second vendor turn, echoing the user message with `isReplay: true` and the preserved `uuid`, producing a second `result` with "PONG".
4. Normalizer tracks completed turns, emits a `transcript.Notice` (level: "info", code: "late_steer"), and attributes subsequent vendor events and `turn_end` to `vendor-<uuid>`.

## Redaction (apply before commit; verified by scanning the result)

1. Every UUID (session id, event `uuid`, `hook_id`) becomes `uuid-NNNN` in order
   of first appearance; the same original id always maps to the same
   placeholder, so tool_use ↔ tool_result matching stays verifiable.
2. `msg_…`, `req_…` and `toolu_…` ids with at least ten trailing characters
   become `msg-NNNN`, `req-NNNN`, `toolu-NNNN` (plugin slash names such as
   `msg_lifecycle_v1` are not ids and stay).
3. The recording directory path becomes `/home/REDACTED/claude-fixture`
   everywhere (`cwd`, tool inputs, permission messages).
4. Thinking `signature` values become `REDACTED_SIGNATURE`; `signature_delta`
   fragments (`partial_json` values that are long pure-base64) become `REDACTED`.
5. Replace the `system/init` event (line 9) with a minimal hand-written one. It
   keeps only the fields the normalizer reads (`claude_code_version`,
   `session_id`, `cwd`, `model`, `permissionMode`, `slash_commands`,
   `terminal_slash_commands`, `tools`) plus a few fields it does not read
   (`apiKeySource`, `output_style`, `fast_mode_state`, `view_mode`), so the
   unknown-field path stays covered. The recorded `mcp_servers`, `plugins`,
   `skills`, `agents` and `slash_commands` describe the recording machine and
   never go into the repository: use `example-*` names only, with plugin paths
   under the REDACTED home placeholder. Drop `memory_paths` and `messaging_socket_path`
   entirely. Keep the line count, because `*.turns.json` refers to line numbers.
6. Regenerate the golden transcript (`go test ./internal/vendoradapter/claude
   -run TestVRE01 -update`) and review its diff.
7. Run `go test ./internal/vendoradapter/claude -run TestFixturesContainNoHostIdentity`
   before committing. It scans every file under `testdata/` and fails on any
   home directory other than the REDACTED placeholder, a macOS home directory,
   a per-user runtime directory, a real UUID, an email address, a provider key
   or token prefix, and any value under `plugins`, `mcp_servers` or
   `memory_paths` that is not a placeholder.

Everything else stays as recorded: model names, usage numbers and cost, quota
`resetsAt` epochs, timestamps and event texts.
