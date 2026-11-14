package codex

import (
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
)

func feed(t *testing.T, n *Normalizer, line string) Observation {
	t.Helper()
	obs, err := n.Normalize([]byte(line))
	if err != nil {
		t.Fatalf("normalize %s: %v", line, err)
	}
	return obs
}

func oneRecord(t *testing.T, obs *Observation) transcript.Item {
	t.Helper()
	if len(obs.Records) != 1 || len(obs.Updates) != 0 {
		t.Fatalf("want 1 record and 0 updates, got %d records and %d updates", len(obs.Records), len(obs.Updates))
	}
	return obs.Records[0]
}

func metaOf[T transcript.Metadata](t *testing.T, m transcript.Metadata) T {
	t.Helper()
	v, ok := m.(T)
	if !ok {
		t.Fatalf("metadata has type %T, want %T", m, v)
	}
	return v
}

func contentOf[T transcript.Content](t *testing.T, c transcript.Content) T {
	t.Helper()
	v, ok := c.(T)
	if !ok {
		t.Fatalf("content has type %T, want %T", c, v)
	}
	return v
}

func TestCodexUnknownNotificationsBecomeUnsupportedNoticesWithRawRef(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("sess-vr-e08")
	n.BeginTurn("turn-1")

	cases := []string{
		`{"method":"account/login/completed","params":{"loginId":null,"success":true,"error":null}}`,
		`{"method":"skills/changed","params":{}}`,
		`{"method":"item/started","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"webSearch","id":"ws-1","query":"test"}}}`,
		`{"method":"turn/started","params":{"threadId":"th-1","turn":"wrong-shape"}}`,
		`{"id":99,"method":"item/unknown/requestApproval","params":{}}`,
		`not-json-at-all`,
	}

	for _, line := range cases {
		obs := feed(t, n, line)
		item := oneRecord(t, &obs)
		if item.Kind != transcript.Notice {
			t.Fatalf("line %s: kind = %s, want notice", line, item.Kind)
		}
		meta := metaOf[transcript.NoticeMetadata](t, item.Metadata)
		if meta.Code != "unsupported_event" || meta.Level != "info" {
			t.Fatalf("line %s: metadata = %+v, want unsupported_event/info", line, meta)
		}
		if item.RawRef == "" {
			t.Fatalf("line %s: missing RawRef on unsupported_event notice", line)
		}
	}
}

func TestCodexItemsAndRevisionsFollowTranscriptRules(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("sess-items")
	n.BeginTurn("turn-1")

	assertReasoningRevisions(t, n)
	assertCommandRevisions(t, n)
	assertFileChangeAndPlanNormalization(t, n)
	assertErrorAndFailedTurnNormalization(t, n)
}

func assertReasoningRevisions(t *testing.T, n *Normalizer) {
	t.Helper()
	rStartObs := feed(t, n, `{"method":"item/started","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"reasoning","id":"rs-1","summary":[],"content":[]}}}`)
	rStart := oneRecord(t, &rStartObs)
	if rStart.Kind != transcript.Reasoning || rStart.Revision != 0 {
		t.Fatalf("reasoning start = %+v", rStart)
	}
	rDelta := feed(t, n, `{"method":"item/reasoning/summaryTextDelta","params":{"threadId":"th-1","turnId":"t-1","itemId":"rs-1","delta":"thinking step","summaryIndex":0}}`)
	if rDelta.Partial == nil || rDelta.Partial.Fragment != "thinking step" {
		t.Fatalf("reasoning delta = %+v", rDelta.Partial)
	}
	rDone := feed(t, n, `{"method":"item/completed","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"reasoning","id":"rs-1","summary":["thinking step"],"content":[]}}}`)
	if len(rDone.Updates) != 1 || rDone.Updates[0].Before.Revision != 0 || rDone.Updates[0].After.Revision != 1 {
		t.Fatalf("reasoning update = %+v", rDone.Updates)
	}
}

func assertCommandRevisions(t *testing.T, n *Normalizer) {
	t.Helper()
	cStartObs := feed(t, n, `{"method":"item/started","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"commandExecution","id":"cmd-1","command":"/bin/zsh -lc 'echo hi'","cwd":"/tmp","status":"inProgress","commandActions":[]}}}`)
	cStart := oneRecord(t, &cStartObs)
	if cStart.Kind != transcript.ToolCall || metaOf[transcript.ToolCallMetadata](t, cStart.Metadata).Status != toolStatusRunning {
		t.Fatalf("command start = %+v", cStart)
	}
	feed(t, n, `{"method":"item/commandExecution/outputDelta","params":{"threadId":"th-1","turnId":"t-1","itemId":"cmd-1","delta":"hi\n"}}`)
	cDone := feed(t, n, `{"method":"item/completed","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"commandExecution","id":"cmd-1","command":"/bin/zsh -lc 'echo hi'","cwd":"/tmp","status":"completed","commandActions":[],"aggregatedOutput":"hi\n","exitCode":0,"durationMs":45}}}`)
	if len(cDone.Updates) != 1 {
		t.Fatalf("command completed updates = %d, want 1", len(cDone.Updates))
	}
	cmdMeta := metaOf[transcript.ToolCallMetadata](t, cDone.Updates[0].After.Metadata)
	if cmdMeta.Status != toolStatusSucceeded || cmdMeta.ExitCode == nil || *cmdMeta.ExitCode != 0 || cmdMeta.Duration != 45*time.Millisecond {
		t.Fatalf("command metadata = %+v", cmdMeta)
	}
}

func assertFileChangeAndPlanNormalization(t *testing.T, n *Normalizer) {
	t.Helper()
	fDone := feed(t, n, `{"method":"item/completed","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"fileChange","id":"fc-1","status":"completed","changes":[{"path":"/tmp/a.txt","kind":{"type":"add"},"diff":"@@ -0,0 +1,2 @@\n+one\n+two\n"},{"path":"/tmp/b.txt","kind":{"type":"update","move_path":"/tmp/old_b.txt"},"diff":"@@ -1 +1 @@\n-old\n+new\n"}]}}}`)
	if len(fDone.Records) != 2 {
		t.Fatalf("fileChange records = %d, want 2", len(fDone.Records))
	}
	fc0 := metaOf[transcript.FileChangeMetadata](t, fDone.Records[0].Metadata)
	if fc0.Change != "add" || fc0.Added != 2 || fc0.Removed != 0 {
		t.Fatalf("fc0 metadata = %+v", fc0)
	}
	fc1 := metaOf[transcript.FileChangeMetadata](t, fDone.Records[1].Metadata)
	fc1Content := contentOf[transcript.FileChangeContent](t, fDone.Records[1].Content)
	if fc1.Change != "rename" || fc1Content.OldPath != "/tmp/old_b.txt" {
		t.Fatalf("fc1 = %+v content=%+v", fc1, fc1Content)
	}

	planObs := feed(t, n, `{"method":"turn/plan/updated","params":{"threadId":"th-1","turnId":"t-1","plan":[{"step":"step one","status":"completed"},{"step":"step two","status":"inProgress"}]}}`)
	planItem := oneRecord(t, &planObs)
	if planItem.Kind != transcript.Plan || metaOf[transcript.PlanMetadata](t, planItem.Metadata).Current != 2 {
		t.Fatalf("plan item = %+v", planItem)
	}
	feed(t, n, `{"method":"item/plan/delta","params":{"threadId":"th-1","turnId":"t-1","itemId":"plan-item-1","delta":"first step\nsecond step"}}`)
	planItemObs := feed(t, n, `{"method":"item/completed","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"plan","id":"plan-item-1","text":""}}}`)
	if oneRecord(t, &planItemObs).Kind != transcript.Plan {
		t.Fatalf("plan item record = %+v", planItemObs)
	}
	mcpStartObs := feed(t, n, `{"method":"item/started","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"mcpToolCall","id":"mcp-1","server":"docs","tool":"search","status":"inProgress","arguments":{"q":"codex"}}}}`)
	mcpStart := oneRecord(t, &mcpStartObs)
	if mcpStart.Kind != transcript.ToolCall || metaOf[transcript.ToolCallMetadata](t, mcpStart.Metadata).ToolName != "docs/search" {
		t.Fatalf("mcp start = %+v", mcpStart)
	}
	feed(t, n, `{"method":"item/completed","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"mcpToolCall","id":"mcp-1","server":"docs","tool":"search","status":"completed","durationMs":12,"arguments":{"q":"codex"},"result":{"hits":1}}}}`)
	assertCommandModeAndLifecycleItems(t, n)
}

func assertCommandModeAndLifecycleItems(t *testing.T, n *Normalizer) {
	t.Helper()
	compactObs := feed(t, n, `{"method":"item/started","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"contextCompaction","id":"cmp-1"}}}`)
	if oneRecord(t, &compactObs).Kind != transcript.Command {
		t.Fatalf("contextCompaction item = %+v", compactObs)
	}
	feed(t, n, `{"method":"item/completed","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"contextCompaction","id":"cmp-1"}}}`)
	revStart := feed(t, n, `{"method":"item/started","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"enteredReviewMode","id":"rev-1","review":"base main"}}}`)
	if oneRecord(t, &revStart).Kind != transcript.Command {
		t.Fatalf("enteredReviewMode item = %+v", revStart)
	}
	revExit := feed(t, n, `{"method":"item/completed","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"exitedReviewMode","id":"rev-2","review":"Looks good"}}}`)
	if oneRecord(t, &revExit).Kind != transcript.AssistantMessage {
		t.Fatalf("exitedReviewMode item = %+v", revExit)
	}
	feed(t, n, `{"method":"thread/name/updated","params":{"threadId":"th-1","threadName":"named"}}`)
	feed(t, n, `{"method":"serverRequest/resolved","params":{"threadId":"th-1","requestId":0}}`)
	feed(t, n, `{"method":"item/reasoning/textDelta","params":{"threadId":"th-1","turnId":"t-1","itemId":"rs-1","delta":"raw thought","contentIndex":0}}`)
}

func assertErrorAndFailedTurnNormalization(t *testing.T, n *Normalizer) {
	t.Helper()
	n.BeginTurn("turn-2")
	if n.CurrentTurnID() != "turn-2" {
		t.Fatalf("CurrentTurnID = %q, want turn-2", n.CurrentTurnID())
	}
	n.RollbackTurn()
	errObs := feed(t, n, `{"method":"error","params":{"error":{"message":"rate limit reached","codexErrorInfo":"usageLimitExceeded"},"willRetry":false,"threadId":"th-1","turnId":"t-1"}}`)
	errItem := oneRecord(t, &errObs)
	if errItem.Kind != transcript.Notice || metaOf[transcript.NoticeMetadata](t, errItem.Metadata).Level != MethodError {
		t.Fatalf("error item = %+v", errItem)
	}
	turnFail := feed(t, n, `{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"t-1","status":"failed","error":{"message":"failed","codexErrorInfo":{"httpConnectionFailed":{"status":502}}}}}}`)
	if turnFail.TurnEnd == nil || turnFail.TurnEnd.Outcome != OutcomeFailed || turnFail.TurnEnd.FailureCode != "httpConnectionFailed" {
		t.Fatalf("turnFail = %+v", turnFail.TurnEnd)
	}
}

func TestCodexServerRequestsNormalizeWithBoundedSummaryAndFullDetail(t *testing.T) {
	t.Parallel()
	n := NewNormalizer("sess-approvals")
	n.BeginTurn("turn-1")

	assertApprovalRequestsNormalize(t, n)
	assertUserInputRequestNormalizes(t, n)
}

func assertApprovalRequestsNormalize(t *testing.T, n *Normalizer) {
	t.Helper()
	longCmd := strings.Repeat("echo hello; ", 250)
	cmdObs := feed(t, n, `{"id":10,"method":"item/commandExecution/requestApproval","params":{"threadId":"th-1","turnId":"t-1","itemId":"cmd-1","command":"`+longCmd+`","cwd":"/tmp","reason":"needs shell"}}`)
	if cmdObs.ServerRequest == nil {
		t.Fatal("expected ServerRequest for command approval")
	}
	if len(cmdObs.ServerRequest.Summary) > maxSummaryBytes || !strings.HasSuffix(cmdObs.ServerRequest.Summary, truncationSuffix) {
		t.Fatalf("summary not truncated at 2 KiB: len=%d", len(cmdObs.ServerRequest.Summary))
	}
	if !strings.Contains(cmdObs.ServerRequest.Detail, longCmd) {
		t.Fatal("detail must preserve the full untruncated command")
	}

	feed(t, n, `{"method":"item/started","params":{"threadId":"th-1","turnId":"t-1","item":{"type":"fileChange","id":"patch-1","status":"inProgress","changes":[{"path":"/tmp/x.go","kind":{"type":"add"},"diff":"+package main\n"}]}}}`)
	fcObs := feed(t, n, `{"id":11,"method":"item/fileChange/requestApproval","params":{"threadId":"th-1","turnId":"t-1","itemId":"patch-1","reason":"write outside root"}}`)
	if fcObs.ServerRequest == nil || !strings.Contains(fcObs.ServerRequest.Detail, "/tmp/x.go") {
		t.Fatalf("fileChange approval detail missing file changes: %+v", fcObs.ServerRequest)
	}

	permObs := feed(t, n, `{"id":12,"method":"item/permissions/requestApproval","params":{"threadId":"th-1","turnId":"t-1","itemId":"perm-1","cwd":"/tmp","permissions":{"network":{"enabled":true}}}}`)
	if permObs.ServerRequest == nil || permObs.ServerRequest.Kind != vendor.ServerRequestConfirm {
		t.Fatalf("permissions approval = %+v", permObs.ServerRequest)
	}
}

func assertUserInputRequestNormalizes(t *testing.T, n *Normalizer) {
	t.Helper()
	qObs := feed(t, n, `{"id":13,"method":"item/tool/requestUserInput","params":{"threadId":"th-1","turnId":"t-1","itemId":"q-1","isBlocking":true,"questions":[{"id":"env","header":"Env","question":"Which environment?","isOther":true,"isSecret":false,"options":[{"label":"staging","description":"Staging cluster"},{"label":"prod","description":"Production cluster"}]}]}}`)
	if qObs.ServerRequest == nil || qObs.ServerRequest.Kind != vendor.ServerRequestQuestion || len(qObs.ServerRequest.QuestionOptions) != 2 || !qObs.ServerRequest.AllowFreeText {
		t.Fatalf("user input request = %+v", qObs.ServerRequest)
	}
}
