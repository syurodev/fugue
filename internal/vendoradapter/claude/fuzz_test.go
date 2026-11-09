package claude

// Fuzz the normalizer: vendor output is untrusted data, so no line — nested,
// malformed or alien — may panic it (vendor-runtime §12), and any item it
// produces must still satisfy the transcript model.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/transcript"
)

func FuzzNormalizeLine(f *testing.F) {
	seedFuzzCorpus(f)
	f.Fuzz(func(t *testing.T, line string) {
		n := NewNormalizer("fuzz-session")
		n.BeginTurn("fuzz-turn")
		obs, err := n.Normalize([]byte(line))
		if err != nil {
			return
		}
		for i := range obs.Records {
			if vErr := obs.Records[i].Validate(); vErr != nil {
				t.Fatalf("an item the normalizer produced is invalid: %v", vErr)
			}
		}
		for i := range obs.Updates {
			if uErr := transcript.ValidateUpdate(&obs.Updates[i].Before, &obs.Updates[i].After); uErr != nil {
				t.Fatalf("an update the normalizer produced is invalid: %v", uErr)
			}
		}
	})
}

func seedFuzzCorpus(f *testing.F) {
	f.Helper()
	f.Add(`{"type":"system","subtype":"init","claude_code_version":"2.1.319","slash_commands":["a"],"terminal_slash_commands":["a"]}`)
	f.Add(`{"type":"assistant","uuid":"u","message":{"id":"m","model":"x","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"t","name":"Read","input":{"file_path":"a"}},{"type":"thinking","thinking":"t","signature":"s"}]}}`)
	f.Add(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"ok","is_error":false}]}}`)
	f.Add(`{"type":"result","subtype":"success","usage":{"input_tokens":1,"output_tokens":2},"total_cost_usd":0.1,"credential_expires_at":"2026-12-08T01:59:13.398Z"}`)
	f.Add(`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"five_hour":{"utilization":0.5,"resetsAt":1794208200}}}}`)
	f.Add(`{"type":"stream_event","api_message_id":"m","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}}`)
	f.Add(`{"type":"system","subtype":"permission_denied","tool_name":"Write","tool_use_id":"t"}`)
	f.Add(`{"type":"assistant","parent_tool_use_id":"t"}`)
	f.Add(`not json at all`)
	f.Add(`{"type":"assistant","message":{"content":[{` + string(make([]byte, 4096)) + `]}}`)
	f.Add(`{"type":"user","message":{"content":"` + string(make([]byte, 1<<20)) + `"}}`)
	f.Add(`{"deep":[{"a":[{"b":[{"c":[[[[[[[[[[]]]]]]]]]]}]}]}]}`)
	f.Add(`{"type":"result","usage":{"input_tokens":9999999999999999999999}}`)
	f.Add(`{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"\u0000\uFFFF"}}}`)
	// Every fixture line seeds the corpus too.
	raw, err := os.ReadFile(filepath.Join("testdata", "fixtures", "claude-2.1.319-session1.jsonl"))
	if err != nil {
		f.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			f.Add(line)
		}
	}
}
