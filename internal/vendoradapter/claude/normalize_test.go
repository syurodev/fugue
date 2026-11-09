package claude

// Unit coverage of the §4 mapping rows the recorded fixture cannot reach
// (Edit/Write/MultiEdit file changes, TodoWrite plans, Task subagents), the
// VR-E08 rules (unknown events, nested streams) and the partial-delta data.

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/transcript"
)

// feed drives one JSON line through the normalizer and fails on error.
func feed(t *testing.T, n *Normalizer, line string) Observation {
	t.Helper()
	obs, err := n.Normalize([]byte(line))
	if err != nil {
		t.Fatalf("normalize %s: %v", line, err)
	}
	return obs
}

// metaOf returns the typed metadata, failing the test on a mismatch.
func metaOf[T transcript.Metadata](t *testing.T, m transcript.Metadata) T {
	t.Helper()
	v, ok := m.(T)
	if !ok {
		t.Fatalf("metadata has type %T, want %T", m, v)
	}
	return v
}

// contentOf returns the typed content, failing the test on a mismatch.
func contentOf[T transcript.Content](t *testing.T, c transcript.Content) T {
	t.Helper()
	v, ok := c.(T)
	if !ok {
		t.Fatalf("content has type %T, want %T", c, v)
	}
	return v
}

// feedRef feeds one line and returns the observation by pointer.
func feedRef(t *testing.T, n *Normalizer, line string) *Observation {
	t.Helper()
	obs := feed(t, n, line)
	return &obs
}

// feedLine drives one line whose observation the test does not inspect.
func feedLine(t *testing.T, n *Normalizer, line string) {
	t.Helper()
	if _, err := n.Normalize([]byte(line)); err != nil {
		t.Fatalf("normalize %s: %v", line, err)
	}
}

func oneRecord(t *testing.T, obs *Observation) transcript.Item {
	t.Helper()
	if len(obs.Records) != 1 || len(obs.Updates) != 0 {
		t.Fatalf("want exactly one record, got %d records and %d updates", len(obs.Records), len(obs.Updates))
	}
	return obs.Records[0]
}

func assistantLine(blocks string) string {
	return `{"type":"assistant","uuid":"u-a","session_id":"s","message":{"id":"m-1","model":"claude-sonnet-5-5","content":[` + blocks + `]}}`
}

func toolUseLine(id, name, input string) string {
	return assistantLine(`{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}`)
}

func toolResultLine(id, content string, isError bool) string {
	errFlag := "false"
	if isError {
		errFlag = "true"
	}
	return `{"type":"user","uuid":"u-r","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":` + content + `,"is_error":` + errFlag + `}]}}`
}

func TestUnknownEventsBecomeUnsupportedNoticesWithRawRef(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	for _, line := range []string{
		`{"type":"system","subtype":"status","status":"requesting","uuid":"u1","session_id":"s"}`,
		`{"type":"totally_new_thing","uuid":"u2","session_id":"s"}`,
	} {
		item := oneRecord(t, feedRef(t, n, line))
		if item.Kind != transcript.Notice {
			t.Fatalf("kind = %s, want notice", item.Kind)
		}
		meta := metaOf[transcript.NoticeMetadata](t, item.Metadata)
		if meta.Code != "unsupported_event" || meta.Level != "info" {
			t.Fatalf("notice metadata = %+v", meta)
		}
		if item.RawRef == "" {
			t.Fatal("the notice lost the raw event reference (TR-06)")
		}
	}
}

func TestWrongShapedKnownEventsBecomeNoticesNotErrors(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	for _, line := range []string{
		`{"type":"assistant","uuid":"u1","message":{"content":"a string where blocks belong"}}`,
		`{"type":"user","uuid":"u2","message":{"content":42}}`,
		`{"type":"result","uuid":"u3","usage":"not-an-object"}`,
		`{"type":"rate_limit_event","uuid":"u4","rate_limit_info":[]}`,
		`{"type":"stream_event","uuid":"u5","event":"no"}`,
	} {
		item := oneRecord(t, feedRef(t, n, line))
		if item.Kind != transcript.Notice {
			t.Fatalf("%s: want a notice for a wrong-shaped event, got %s", line, item.Kind)
		}
	}
}

func TestNonJSONLineFailsTyped(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	_, err := n.Normalize([]byte("this is not json"))
	if code := errcode.CodeOf(err); code != errcode.InvalidArgument {
		t.Fatalf("code = %s, want invalid_argument (%v)", code, err)
	}
}

func TestToolResultUpdatesToolCallStatusAndOutput(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	record := oneRecord(t, feedRef(t, n, toolUseLine("t1", "Read", `{"file_path":"notes.txt"}`)))
	if record.Kind != transcript.ToolCall {
		t.Fatalf("kind = %s, want tool_call", record.Kind)
	}
	obs := feed(t, n, toolResultLine("t1", `"the file says hi"`, false))
	if len(obs.Updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(obs.Updates))
	}
	after := obs.Updates[0].After
	if meta := metaOf[transcript.ToolCallMetadata](t, after.Metadata); meta.Status != toolSucceeded {
		t.Fatalf("status = %s, want succeeded", meta.Status)
	}
	if content := contentOf[transcript.ToolCallContent](t, after.Content); content.Output != "the file says hi" {
		t.Fatalf("output = %q", content.Output)
	}
	if obs.Updates[0].Before.Revision != 0 || after.Revision != 1 {
		t.Fatalf("revision = %d -> %d, want 0 -> 1", obs.Updates[0].Before.Revision, after.Revision)
	}

	feedLine(t, n, toolUseLine("t2", "Bash", `{"command":"ls"}`))
	obs = feed(t, n, toolResultLine("t2", `"boom"`, true))
	if meta := metaOf[transcript.ToolCallMetadata](t, obs.Updates[0].After.Metadata); meta.Status != toolFailed {
		t.Fatalf("error result status = %s, want failed", meta.Status)
	}
}

func TestToolResultWithoutPendingItemBecomesNotice(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	item := oneRecord(t, feedRef(t, n, toolResultLine("ghost", `"late"`, false)))
	if item.Kind != transcript.Notice {
		t.Fatalf("kind = %s, want notice", item.Kind)
	}
}

func TestPermissionDeniedMarksToolCallDeniedAndKeepsIt(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	feedLine(t, n, toolUseLine("t1", "Write", `{"file_path":"out.txt","content":"hi"}`))
	denied := feed(t, n, `{"type":"system","subtype":"permission_denied","tool_name":"Write","tool_use_id":"t1","message":"no","uuid":"u-d","session_id":"s"}`)
	if len(denied.Updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(denied.Updates))
	}
	if meta := metaOf[transcript.ToolCallMetadata](t, denied.Updates[0].After.Metadata); meta.Status != toolDenied {
		t.Fatalf("status = %s, want denied", meta.Status)
	}
	// The error tool result that follows a denial reports it, it does not turn
	// the denial into a tool failure (VR-04).
	after := feed(t, n, toolResultLine("t1", `"you denied it"`, true)).Updates[0].After
	if meta := metaOf[transcript.ToolCallMetadata](t, after.Metadata); meta.Status != toolDenied {
		t.Fatalf("status after the error result = %s, want denied", meta.Status)
	}
	if after.Revision != 2 {
		t.Fatalf("revision = %d, want 2", after.Revision)
	}
	if contentOf[transcript.ToolCallContent](t, n.items["t1"].Content).Output == "" {
		t.Fatal("the denial message output was dropped")
	}
}

func TestFileToolsProduceFileChangeWithDiffFromInput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		tool     string
		input    string
		change   string
		added    int
		removed  int
		wantDiff string
	}{
		{
			name: "write adds the whole content", tool: "Write",
			input:  `{"file_path":"new.txt","content":"hello\nworld\n"}`,
			change: "add", added: 2, removed: 0,
			wantDiff: "diff --git a/new.txt b/new.txt\n--- a/new.txt\n+++ b/new.txt\n+hello\n+world\n",
		},
		{
			name: "edit replaces one string", tool: "Edit",
			input:  `{"file_path":"app.go","old_string":"foo\nbar","new_string":"baz"}`,
			change: "modify", added: 1, removed: 2,
			wantDiff: "diff --git a/app.go b/app.go\n--- a/app.go\n+++ b/app.go\n-foo\n-bar\n+baz\n",
		},
		{
			name: "multiedit keeps every edit in order", tool: "MultiEdit",
			input:  `{"file_path":"app.go","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d\ne"}]}`,
			change: "modify", added: 3, removed: 2,
			wantDiff: "diff --git a/app.go b/app.go\n--- a/app.go\n+++ b/app.go\n-a\n+b\n-c\n+d\n+e\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n := NewNormalizer("s")
			n.BeginTurn("t1")
			feedLine(t, n, toolUseLine("t1", tc.tool, tc.input))
			obs := feed(t, n, toolResultLine("t1", `"ok"`, false))
			if len(obs.Records) != 1 {
				t.Fatalf("records = %d, want the file_change", len(obs.Records))
			}
			item := obs.Records[0]
			if item.Kind != transcript.FileChange {
				t.Fatalf("kind = %s, want file_change", item.Kind)
			}
			meta := metaOf[transcript.FileChangeMetadata](t, item.Metadata)
			if meta.Change != tc.change || meta.Added != tc.added || meta.Removed != tc.removed {
				t.Fatalf("metadata = %+v, want %s +%d -%d", meta, tc.change, tc.added, tc.removed)
			}
			content := contentOf[transcript.FileChangeContent](t, item.Content)
			if diff := cmp.Diff(tc.wantDiff, content.Diff); diff != "" {
				t.Errorf("diff mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDeniedFileToolProducesNoFileChange(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	feedLine(t, n, toolUseLine("t1", "Write", `{"file_path":"out.txt","content":"hi"}`))
	feedLine(t, n, `{"type":"system","subtype":"permission_denied","tool_name":"Write","tool_use_id":"t1","message":"no","uuid":"u-d","session_id":"s"}`)
	obs := feed(t, n, toolResultLine("t1", `"denied"`, true))
	if len(obs.Records) != 0 {
		t.Fatal("a denied write must not record a file_change")
	}
}

func TestTodoWriteBecomesPlan(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	obs := feed(t, n, toolUseLine("t1", "TodoWrite", `{"todos":[{"content":"boil","status":"in_progress"},{"content":"pour","status":"pending"}]}`))
	item := oneRecord(t, &obs)
	if item.Kind != transcript.Plan {
		t.Fatalf("kind = %s, want plan", item.Kind)
	}
	if meta := metaOf[transcript.PlanMetadata](t, item.Metadata); meta.Steps != 2 || meta.Current != 1 {
		t.Fatalf("metadata = %+v, want 2 steps at 1", meta)
	}
	// A second TodoWrite in the same turn updates the same plan item.
	obs = feed(t, n, toolUseLine("t2", "TodoWrite", `{"todos":[{"content":"boil","status":"completed"},{"content":"pour","status":"in_progress"}]}`))
	if len(obs.Updates) != 1 {
		t.Fatalf("updates = %d, want 1", len(obs.Updates))
	}
	if meta := metaOf[transcript.PlanMetadata](t, obs.Updates[0].After.Metadata); meta.Current != 2 {
		t.Fatalf("current = %d, want 2", meta.Current)
	}
	if obs.Updates[0].After.Revision != 1 {
		t.Fatalf("revision = %d, want 1", obs.Updates[0].After.Revision)
	}
}

func TestTaskToolUseBecomesSubagent(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	item := oneRecord(t, feedRef(t, n, toolUseLine("t1", "Task", `{"subagent_type":"general-purpose","description":"check the build","prompt":"run go build"}`)))
	if item.Kind != transcript.Subagent {
		t.Fatalf("kind = %s, want subagent", item.Kind)
	}
	content := contentOf[transcript.SubagentContent](t, item.Content)
	if content.Name != "general-purpose" || content.Description != "check the build" {
		t.Fatalf("content = %+v", content)
	}
	obs := feed(t, n, toolResultLine("t1", `"build ok"`, false))
	after := obs.Updates[0].After
	if meta := metaOf[transcript.SubagentMetadata](t, after.Metadata); meta.Status != toolSucceeded {
		t.Fatalf("status = %s, want succeeded", meta.Status)
	}
	if content := contentOf[transcript.SubagentContent](t, after.Content); content.Result != "build ok" {
		t.Fatalf("result = %q", content.Result)
	}
}

func TestNestedEventsAreCountedNotItemized(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	feedLine(t, n, toolUseLine("t1", "Task", `{"subagent_type":"general-purpose","description":"d"}`))
	obs := feed(t, n, `{"type":"assistant","uuid":"u-n","parent_tool_use_id":"t1","session_id":"s","message":{"id":"m-2","content":[{"type":"text","text":"inner"}]}}`)
	if obs.Nested != 1 || len(obs.Records) != 0 {
		t.Fatalf("nested = %d, records = %d, want counted and not itemized", obs.Nested, len(obs.Records))
	}
}

func TestSlashCommandListHidesTerminalCommandsInVendorOrder(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	feedLine(t, n, `{"type":"system","subtype":"init","claude_code_version":"2.1.319","session_id":"s","model":"m","permissionMode":"default","slash_commands":["model","superpowers:plan","doctor","compact","color"],"terminal_slash_commands":["doctor","color"]}`)
	want := []string{"model", "superpowers:plan", "compact"}
	if diff := cmp.Diff(want, n.SlashCommands()); diff != "" {
		t.Errorf("slash list mismatch (-want +got):\n%s", diff)
	}
}

func TestRateLimitEventBecomesQuotaItemsAndWindowData(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	line := `{"type":"rate_limit_event","uuid":"u-q","session_id":"s","rate_limit_info":{"status":"allowed","resetsAt":1794208200,"rateLimitType":"five_hour","unifiedWindows":{"five_hour":{"utilization":0.17,"resetsAt":1794208200,"status":"allowed"},"seven_day":{"utilization":0.03,"resetsAt":1794744000,"status":"allowed"}}}}`
	obs := feed(t, n, line)
	if obs.Quota == nil || len(obs.Quota.Windows) != 2 {
		t.Fatalf("quota windows = %+v", obs.Quota)
	}
	if obs.Quota.Windows[0].Name != "five_hour" || obs.Quota.Windows[0].Utilization != 0.17 {
		t.Fatalf("five_hour window = %+v", obs.Quota.Windows[0])
	}
	if len(obs.Records) != 2 {
		t.Fatalf("records = %d, want one quota item per window", len(obs.Records))
	}
	meta := metaOf[transcript.QuotaMetadata](t, obs.Records[1].Metadata)
	if meta.Window != "seven_day" || meta.AccountID != "" {
		t.Fatalf("seven_day metadata = %+v, account stays empty until F9.10", meta)
	}
	if meta.ResetsAt.UTC().Unix() != 1794744000 {
		t.Fatalf("resets_at = %s", meta.ResetsAt)
	}
}

func TestRateLimitEventWithoutWindowsFallsBackToTopLevel(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	obs := feed(t, n, `{"type":"rate_limit_event","uuid":"u-q2","rate_limit_info":{"status":"blocked","resetsAt":1794208200,"rateLimitType":"seven_day"}}`)
	if obs.Quota == nil || len(obs.Quota.Windows) != 1 || obs.Quota.Windows[0].Name != "seven_day" {
		t.Fatalf("quota = %+v", obs.Quota)
	}
	if len(obs.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(obs.Records))
	}
}

func TestStreamEventPartialIsExposedAsData(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t1")
	for _, tc := range []struct {
		line     string
		block    string
		fragment string
	}{
		{`{"type":"stream_event","uuid":"u1","api_message_id":"m-9","event":{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"hi there"}}}`, "text", "hi there"},
		{`{"type":"stream_event","uuid":"u2","api_message_id":"m-9","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}}`, "thinking", "hmm"},
		{`{"type":"stream_event","uuid":"u3","api_message_id":"m-9","event":{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}}`, "tool_use", `{"a":`},
	} {
		obs := feed(t, n, tc.line)
		if obs.Partial == nil {
			t.Fatalf("%s: no partial exposed", tc.line)
		}
		if obs.Partial.MessageID != "m-9" || obs.Partial.BlockType != tc.block || obs.Partial.Fragment != tc.fragment {
			t.Fatalf("partial = %+v", obs.Partial)
		}
	}
	// Lifecycle deltas carry no display content.
	obs := feed(t, n, `{"type":"stream_event","uuid":"u4","event":{"type":"message_start","message":{"id":"m-9","content":[]}}}`)
	if obs.Partial != nil {
		t.Fatal("message_start must not expose a partial")
	}
}

func TestResultWithoutActiveTurnBecomesNotice(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	item := oneRecord(t, feedRef(t, n, `{"type":"result","subtype":"success","uuid":"u-x","usage":{"input_tokens":1,"output_tokens":2}}`))
	if item.Kind != transcript.Notice {
		t.Fatalf("kind = %s, want notice", item.Kind)
	}
}

func TestFailedResultEndsTurnWithFailureCode(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	n.BeginTurn("t7")
	obs := feed(t, n, `{"type":"result","subtype":"error_max_turns","is_error":true,"uuid":"u-x","usage":{"input_tokens":1,"output_tokens":2},"total_cost_usd":0.5}`)
	if obs.TurnEnd == nil {
		t.Fatal("no turn end")
	}
	if obs.TurnEnd.Outcome != OutcomeFailed || obs.TurnEnd.FailureCode != "error_max_turns" {
		t.Fatalf("turn end = %+v", obs.TurnEnd)
	}
	if obs.TurnEnd.Usage.Cost != 0.5 || obs.TurnEnd.Usage.InputTokens != 1 {
		t.Fatalf("usage = %+v", obs.TurnEnd.Usage)
	}
	if len(obs.Records) != 1 || obs.Records[0].Kind != transcript.Usage {
		t.Fatalf("records = %+v, want the usage item", obs.Records)
	}
}

func TestNormalizeErrorsAreTypedNotPanics(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("s")
	var typed *errcode.Error
	_, err := n.Normalize([]byte("{"))
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want a typed error", err)
	}
}
