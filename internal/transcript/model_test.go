package transcript

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/platform/errcode"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestStorageFitKeepsHeadAndTailWithinLimit(t *testing.T) {
	t.Parallel()
	in := "αβγδεζηθ"
	got, truncated, size := FitStorage([]byte(in), 9)
	if !truncated || size != len(in) || len(got) > 9 || !utf8.Valid(got) || !strings.HasPrefix(in, string(got[:4])) || !strings.HasSuffix(in, string(got[len(got)-4:])) {
		t.Fatal("storage fit did not retain UTF-8 head and tail")
	}
}

func TestOversizedItemsShowTruncatedViewsAndKeepFullContent(t *testing.T) {
	t.Parallel()
	original := strings.Repeat("a", 70000) + "tail"
	got := BuildView(ViewText, original, maskingRedactor{})
	if !got.Truncated || len(got.Text) != 65536 || original[len(original)-4:] != "tail" {
		t.Fatal("text view or source incorrect")
	}
	output := BuildView(ViewToolOutput, original, maskingRedactor{})
	if !output.Truncated || len(output.Text) != 65536 || !strings.HasSuffix(output.Text, "tail") {
		t.Fatal("tool view incorrect")
	}
}

type maskingRedactor struct{}

func (maskingRedactor) Redact(s string) string {
	return strings.ReplaceAll(s, "cross-secret", "[masked]")
}

func TestViewsRedactBeforeTruncating(t *testing.T) {
	t.Parallel()
	content := strings.Repeat("x", 65530) + "cross-secret" + "end"
	got := BuildView(ViewText, content, maskingRedactor{})
	if strings.Contains(got.Text, "cross-") {
		t.Fatal("secret exposed across cutoff")
	}
}

func TestUnsupportedEventBecomesNoticeWithRawRef(t *testing.T) {
	t.Parallel()
	item := UnsupportedEvent("item", "session", "turn", "raw")
	metadata, ok := item.Metadata.(NoticeMetadata)
	if !ok || item.Kind != Notice || item.RawRef != "raw" || metadata.Code != "unsupported_event" {
		t.Fatal("unsupported event lost")
	}
}

func TestItemUpdatesKeepIdentityAndIncreaseRevision(t *testing.T) {
	t.Parallel()
	old := Item{ID: "i", SessionID: "s", TurnID: "t", Kind: Notice, Revision: 1, Metadata: NoticeMetadata{Level: "info"}}
	next := old
	next.Revision++
	if err := ValidateUpdate(&old, &next); err != nil {
		t.Fatal(err)
	}
	next.ID = "other"
	if err := ValidateUpdate(&old, &next); err == nil {
		t.Fatal("changed identity accepted")
	}
	next = old
	if err := ValidateUpdate(&old, &next); err == nil {
		t.Fatal("stale revision accepted")
	}
}

func TestInvalidKindsAndStatusesFailTyped(t *testing.T) {
	t.Parallel()
	for _, item := range []Item{{Kind: "unknown"}, {Kind: ToolCall, Metadata: ToolCallMetadata{Status: "bad"}}} {
		if err := item.Validate(); errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Fatalf("invalid item returned %v", err)
		}
	}
}

func TestMetadataTypesHoldNoContentStrings(t *testing.T) {
	t.Parallel()
	metadata := []any{UserMessageMetadata{}, AssistantMessageMetadata{}, ReasoningMetadata{}, ToolCallMetadata{}, FileChangeMetadata{}, ApprovalMetadata{}, PlanMetadata{}, SubagentMetadata{}, CommandMetadata{}, TeamProposalMetadata{}, TaskLinkMetadata{}, UsageMetadata{}, QuotaMetadata{}, NoticeMetadata{}}
	allowed := map[string]bool{"Origin": true, "ModelID": true, "Status": true, "ToolName": true, "Change": true, "RequestType": true, "Decision": true, "Decider": true, "Receipt": true, "Source": true, "ProposalID": true, "TaskID": true, "AgentID": true, "AccountID": true, "Window": true, "Level": true, "Code": true}
	for _, value := range metadata {
		typ := reflect.TypeOf(value)
		for i := range typ.NumField() {
			field := typ.Field(i)
			if field.Type.Kind() == reflect.String && !allowed[field.Name] {
				t.Errorf("%s.%s is not metadata", typ.Name(), field.Name)
			}
		}
	}
}

func TestItemMetadataKinds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind     Kind
		metadata Metadata
	}{
		{UserMessage, UserMessageMetadata{Origin: "user"}},
		{AssistantMessage, AssistantMessageMetadata{ModelID: "model"}},
		{Reasoning, ReasoningMetadata{Status: "plain"}},
		{ToolCall, ToolCallMetadata{Status: "running"}},
		{FileChange, FileChangeMetadata{Change: "rename"}},
		{Approval, ApprovalMetadata{Decider: "user"}},
		{Plan, PlanMetadata{}},
		{Subagent, SubagentMetadata{Status: "running"}},
		{Command, CommandMetadata{Source: "vendor"}},
		{TeamProposal, TeamProposalMetadata{Decision: "pending"}},
		{TaskLink, TaskLinkMetadata{Status: "checking"}},
		{Usage, UsageMetadata{}},
		{Quota, QuotaMetadata{}},
		{Notice, NoticeMetadata{Level: "warning"}},
	}
	for _, tc := range cases {
		item := Item{Kind: tc.kind, Metadata: tc.metadata}
		if err := item.Validate(); err != nil {
			t.Errorf("%s: %v", tc.kind, err)
		}
	}
}

func TestDisplayDiffAndUTF8Limits(t *testing.T) {
	t.Parallel()
	content := "diff --git a/a b/a\n" + strings.Repeat("+one\n-two\n", 1001)
	view := BuildView(ViewDiff, content, maskingRedactor{})
	if !view.Truncated || len(view.Files) != 1 || view.Files[0].Added != 1001 || view.Files[0].Removed != 1001 {
		t.Fatal("incorrect diff folding")
	}
	text := BuildView(ViewText, strings.Repeat("é", 40000), maskingRedactor{})
	if !utf8.ValidString(text.Text) || len(text.Text) > 65536 {
		t.Fatal("invalid UTF-8 view")
	}
	if got := BuildView(ViewText, "a\tb", maskingRedactor{}).Text; got != "a    b" {
		t.Fatal("tabs remain")
	}
}

func TestContentKindsMatchItems(t *testing.T) {
	t.Parallel()
	contents := []Content{UserMessageContent{}, AssistantMessageContent{}, ReasoningContent{}, ToolCallContent{}, FileChangeContent{}, ApprovalContent{}, PlanContent{}, SubagentContent{}, CommandContent{}, TeamProposalContent{}, TaskLinkContent{}, UsageContent{}, QuotaContent{}, NoticeContent{}}
	for _, content := range contents {
		if content.transcriptContent() == "" {
			t.Fatal("missing content kind")
		}
	}
	item := Item{Kind: Notice, Metadata: NoticeMetadata{Level: "info"}, Content: ToolCallContent{}}
	if err := item.Validate(); err == nil {
		t.Fatal("mismatched content accepted")
	}
}
