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
5. `init.tools` collapses to a fixed representative list — the recorded
   inventory is the owner's machine-specific MCP set and carries no fixture
   meaning. `slash_commands` and `terminal_slash_commands` stay as recorded:
   they are the vendor's own published lists and the input of the slash-list
   feature.
6. Scan the result for the old session id, emails, `sk-`/`gh*`/`AKIA` tokens,
   real UUIDs, the recording path and signature blobs — all must be absent.

Everything else stays as recorded: model names, usage numbers and cost, quota
`resetsAt` epochs, timestamps, event texts and the slash command lists.
