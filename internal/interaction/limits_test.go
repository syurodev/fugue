package interaction

import (
	"fmt"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func oversizedSpecCases(t *testing.T) []struct {
	name string
	spec *Spec
} {
	t.Helper()
	long := func(n int) string { return strings.Repeat("a", n) }
	manyOptions := func(n int) []Option {
		opts := make([]Option, n)
		for i := range opts {
			opts[i] = Option{ID: fmt.Sprintf("opt-%d", i), Label: genericOptionLabel}
		}
		return opts
	}
	bigSummary := confirmSpec("op-big-summary")
	bigSummary.Summary = long(MaxSummaryBytes + 1)
	bigBody := questionSpec("op-big-body")
	bigBody.Body = long(MaxPromptBytes + 1)
	bigQuestion := questionSpec("op-big-question")
	bigQuestion.Questions[0].Question = long(MaxPromptBytes + 1)
	bigSelect := selectSpec("op-big-select")
	bigSelect.Options = manyOptions(MaxOptions + 1)
	bigRelay := relayedSpec(t, "turn-big", "req-1")
	bigRelay.Options = manyOptions(MaxOptions + 1)
	bigRelayDetail := relayedSpec(t, "turn-big-detail", "req-2")
	bigRelayDetail.Detail = long(MaxRelayedDetailBytes + 1)
	nativeConfirmWithDetail := confirmSpec("op-native-confirm-detail")
	nativeConfirmWithDetail.Detail = "{}"
	nativeSelectWithDetail := selectSpec("op-native-select-detail")
	nativeSelectWithDetail.Detail = "{}"
	nativeQuestionWithDetail := questionSpec("op-native-question-detail")
	nativeQuestionWithDetail.Detail = "{}"
	relayedQuestionWithDetail := &Spec{
		OperationID: mustVendorKey(t, "turn-relayed-q", "req-q"),
		Kind:        KindQuestion,
		Summary:     "question?",
		Detail:      "{}",
		Relayed:     true,
		Questions:   []Question{{ID: "q1", Question: "pick one"}},
		Binding:     vendorBinding("turn-relayed-q", "req-q"),
	}
	bigQuestionOptions := questionSpec("op-big-question-options")
	bigQuestionOptions.Questions[0].Options = manyOptions(MaxOptions + 1)
	return []struct {
		name string
		spec *Spec
	}{
		{name: "summary over 2 KiB", spec: bigSummary},
		{name: "body over 8 KiB", spec: bigBody},
		{name: "question text over 8 KiB", spec: bigQuestion},
		{name: "select with 21 options", spec: bigSelect},
		{name: "relayed with 21 options", spec: bigRelay},
		{name: "relayed confirm detail over 256 KiB", spec: bigRelayDetail},
		{name: "detail on non-relayed confirm", spec: nativeConfirmWithDetail},
		{name: "detail on non-relayed select", spec: nativeSelectWithDetail},
		{name: "detail on non-relayed question", spec: nativeQuestionWithDetail},
		{name: "detail on relayed question", spec: relayedQuestionWithDetail},
		{name: "question with 21 options", spec: bigQuestionOptions},
	}
}

func TestOversizedRequestContentFailsTyped(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	for _, item := range oversizedSpecCases(t) {
		if _, _, err := owner.Ensure(item.spec); err != nil {
			wantCode(t, err, errcode.InvalidArgument)
			continue
		}
		t.Fatalf("%s was accepted", item.name)
	}
}

func TestOversizedAnswerContentFailsTyped(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	long := func(n int) string { return strings.Repeat("a", n) }

	confirmTicket := mustEnsure(t, owner, confirmSpec("op-big-note"))
	_, err := owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   confirmTicket.RequestID,
		Outcome:     OutcomeDenied,
		Text:        long(MaxNoteBytes + 1),
	})
	wantCode(t, err, errcode.InvalidArgument)

	relayTicket := mustEnsure(t, owner, relayedSpec(t, "turn-note", "req-1"))
	_, err = owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   relayTicket.RequestID,
		Outcome:     OutcomeDenied,
		Text:        long(MaxNoteBytes + 1),
	})
	wantCode(t, err, errcode.InvalidArgument)
	_, err = owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   relayTicket.RequestID,
		Outcome:     OutcomeAnswered,
		OptionID:    vendorOptionAllow,
		Text:        long(MaxAnswerBytes + 1),
	})
	wantCode(t, err, errcode.InvalidArgument)

	qSpec := questionSpec("op-big-answer")
	qSpec.Binding.SessionID = session2
	questionTicket := mustEnsure(t, owner, qSpec)
	_, err = owner.Answer(&Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   questionTicket.RequestID,
		Outcome:     OutcomeAnswered,
		Answers:     []Answer{{QuestionID: "db", Text: long(MaxAnswerBytes + 1)}},
	})
	wantCode(t, err, errcode.InvalidArgument)

	// The requests stay answerable within the limits after the rejections.
	relayed := mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   relayTicket.RequestID,
		Outcome:     OutcomeAnswered,
		OptionID:    vendorOptionAllow,
		Text:        long(MaxAnswerBytes),
	})
	if relayed.SelectedOptionID != vendorOptionAllow {
		t.Fatalf("relayed answer = %+v", relayed)
	}
}

func TestPendingRequestLimitsPerSessionAndHost(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)

	// At most one pending native request per session.
	first := mustEnsure(t, owner, confirmSpec("op-front-1"))
	_, _, err := owner.Ensure(confirmSpec("op-front-2"))
	wantCode(t, err, errcode.FailedPrecondition)

	// Relayed vendor requests bypass the per-session frontier.
	relay := mustEnsure(t, owner, relayedSpec(t, "turn-front", "req-1"))
	if relay.Resolved {
		t.Fatalf("relayed request = %+v, want pending", relay)
	}

	// Other sessions are unaffected; every pending request counts toward the
	// host limit of 64.
	second := confirmSpec("op-front-sess2")
	second.Binding.SessionID = session2
	mustEnsure(t, owner, second)
	for i := len(owner.Pending()); i < MaxPendingPerHost; i++ {
		spec := selectSpec(fmt.Sprintf("op-cap-%d", i))
		spec.Binding.SessionID = fmt.Sprintf("sess-cap-%d", i)
		mustEnsure(t, owner, spec)
	}
	overCap := confirmSpec("op-cap-over")
	overCap.Binding.SessionID = "sess-cap-over"
	_, _, err = owner.Ensure(overCap)
	wantCode(t, err, errcode.FailedPrecondition)

	// Answering the pending native request frees the session frontier.
	mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   first.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	mustEnsure(t, owner, confirmSpec("op-front-3"))
}
