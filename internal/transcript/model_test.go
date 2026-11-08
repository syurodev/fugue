package transcript

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/platform/errcode"

	"go.uber.org/goleak"
)

const testInfo = "info"

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestStorageFitKeepsHeadAndTailWithinLimit(t *testing.T) {
	t.Parallel()
	in := "αβγδεζηθ"
	got, truncated, size := FitStorage([]byte(in), 9)
	if !truncated || size != len(in) || len(got) > 9 || !utf8.Valid(got) || !strings.HasPrefix(in, string(got[:4])) || !strings.HasSuffix(in, string(got[len(got)-4:])) {
		t.Fatal("storage fit did not retain UTF-8 head and tail")
	}
	checkStorageEdges(t)
}

func checkStorageEdges(t *testing.T) {
	t.Helper()
	raw := []byte("short")
	copyResult, cut, original := FitStorage(raw, 20)
	if cut || original != len(raw) || string(copyResult) != "short" {
		t.Fatal("short input changed")
	}
	copyResult[0] = 'X'
	if string(raw) != "short" {
		t.Fatal("storage fit aliases input")
	}
	empty, cut, original := FitStorage(raw, 0)
	if !cut || original != len(raw) || len(empty) != 0 {
		t.Fatal("zero limit")
	}
	boundary := []byte("aéxxxxézzz")
	fitted, cut, _ := FitStorage(boundary, 5)
	if !cut || !utf8.Valid(fitted) || len(fitted) > 5 || string(fitted) != "azzz" {
		t.Fatalf("invalid boundary fit: %q", fitted)
	}
}

func TestOversizedItemsShowTruncatedViewsAndKeepFullContent(t *testing.T) {
	t.Parallel()
	original := strings.Repeat("a", 70000) + "tail"
	got := BuildView(ViewText, original, maskingRedactor{})
	if !got.Truncated || got.CutAt != 65536 || got.OmittedBytes != len(original)-65536 || len(got.Text) != 65536 || original[len(original)-4:] != "tail" {
		t.Fatal("text view or source incorrect")
	}
	output := BuildView(ViewToolOutput, original, maskingRedactor{})
	if !output.Truncated || output.CutAt != 32768 || output.OmittedBytes != len(original)-65536 || len(output.Text) != 65536 || !strings.HasSuffix(output.Text, "tail") {
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
	tool := strings.Repeat("a", 40000) + "cross-secret" + strings.Repeat("z", 32762)
	output := BuildView(ViewToolOutput, tool, maskingRedactor{})
	if strings.Contains(output.Text, "secret") || strings.Contains(output.Text, "cross-") {
		t.Fatal("secret leaked at tail cutoff")
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
	old := Item{ID: "i", SessionID: "s", TurnID: "t", Kind: Notice, Revision: 1, Metadata: NoticeMetadata{Level: testInfo}}
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
		item := Item{ID: "i", SessionID: "s", Kind: tc.kind, Metadata: tc.metadata}
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
	item := Item{ID: "i", SessionID: "s", Kind: Notice, Metadata: NoticeMetadata{Level: testInfo}, Content: ToolCallContent{}}
	if err := item.Validate(); err == nil {
		t.Fatal("mismatched content accepted")
	}
}

func TestItemRejectsInvalidIdentityAndTruncation(t *testing.T) {
	t.Parallel()
	validItem := Item{ID: "i", SessionID: "s", Kind: Notice, Metadata: NoticeMetadata{Level: testInfo}}
	cases := []Item{validItem, validItem, validItem, validItem, validItem, validItem}
	cases[0].ID = ""
	cases[1].SessionID = ""
	cases[2].ContentOriginalSize = -1
	cases[3].RawOriginalSize = -1
	cases[4].ContentTruncated = true
	cases[5].RawOriginalSize = 1
	for _, item := range cases {
		if errcode.CodeOf(item.Validate()) != errcode.InvalidArgument {
			t.Fatal("invalid item accepted")
		}
	}
}
