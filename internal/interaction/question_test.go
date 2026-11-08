package interaction

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func TestQuestionAnswersRoundTripThroughTicketAndReceipt(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	ticket := mustEnsure(t, owner, questionSpec("op-q"))
	if diff := cmp.Diff([]Question{testQuestion()}, ticket.Questions); diff != "" {
		t.Fatalf("ticket questions (-want +got):\n%s", diff)
	}
	want := []Answer{{
		QuestionID: "db",
		OptionIDs:  []string{optionDBStage},
		Text:       "please keep it close to production",
	}}
	answered := mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		Answers:     want,
	})
	if answered.Outcome != OutcomeAnswered || answered.ReceiptID == "" {
		t.Fatalf("answered question = %+v", answered)
	}
	if diff := cmp.Diff(want, answered.Answers); diff != "" {
		t.Fatalf("answers round trip (-want +got):\n%s", diff)
	}

	// A question receipt never authorizes execution (checked before consuming it).
	expected := defaultBinding()
	_, err := owner.ClaimReceipt(answered.ReceiptID, &expected)
	wantCode(t, err, errcode.PermissionDenied)

	got, err := owner.ClaimAnswers(answered.ReceiptID, &expected)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("claimed answers (-want +got):\n%s", diff)
	}
	_, err = owner.ClaimAnswers(answered.ReceiptID, &expected)
	wantCode(t, err, errcode.FailedPrecondition)
}

func TestQuestionMultiSelectAndFreeTextAnswers(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	spec := questionSpec("op-q-multi")
	multi := Question{
		ID:          "env",
		Question:    "Which databases should the suite run against?",
		Options:     []Option{{ID: "pg", Label: "Postgres"}, {ID: "sqlite", Label: "SQLite"}},
		MultiSelect: true,
	}
	spec.Questions = []Question{multi, {ID: "notes", Question: "Anything else?"}}
	ticket := mustEnsure(t, owner, spec)
	answered := mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		Answers: []Answer{
			{QuestionID: "env", OptionIDs: []string{"pg", "sqlite"}},
			{QuestionID: "notes", Text: "Watch the connection count"},
		},
	})
	if len(answered.Answers) != 2 || len(answered.Answers[0].OptionIDs) != 2 {
		t.Fatalf("multi-select answers = %+v", answered.Answers)
	}
}

func TestQuestionAnswersRejectUnofferedOptionsAndPartialSets(t *testing.T) {
	t.Parallel()
	owner, _, _, _ := newTestOwner(t)
	ticket := mustEnsure(t, owner, questionSpec("op-q-invalid"))
	requestID := ticket.RequestID

	rejected := []Decision{
		{Outcome: OutcomeAnswered, Answers: nil},
		{Outcome: OutcomeAnswered, Answers: []Answer{{QuestionID: "db", OptionIDs: []string{questionUnofferedID}, Text: "x"}}},
		{Outcome: OutcomeAnswered, Answers: []Answer{{QuestionID: "other", OptionIDs: []string{optionDBLocal}}}},
		{Outcome: OutcomeAnswered, Answers: []Answer{{QuestionID: "db", OptionIDs: []string{optionDBLocal, optionDBStage}}}},
		{Outcome: OutcomeAnswered, Answers: []Answer{{QuestionID: "db"}}},
		{Outcome: OutcomeAnswered, Answers: []Answer{{QuestionID: "db"}, {QuestionID: "db", Text: "again"}}},
		{Outcome: OutcomeDenied},
		{Outcome: OutcomeAllowedOnce, OptionID: optionDBLocal},
		{Outcome: OutcomeCancelled, Answers: []Answer{{QuestionID: "db", Text: "x"}}},
	}
	for i := range rejected {
		decision := rejected[i]
		decision.RequestID = requestID
		if _, err := owner.Answer(&decision); err != nil {
			wantCode(t, err, errcode.InvalidArgument)
			continue
		}
		t.Fatalf("answer %d was accepted", i)
	}
	if ticket.Resolved {
		t.Fatalf("failed answers must leave the request pending: %+v", ticket)
	}

	// Cancelling the whole set is a valid answer without a receipt.
	cancelled := mustAnswer(t, owner, &Decision{
		Incarnation: owner.Incarnation(),
		RequestID:   requestID,
		Outcome:     OutcomeCancelled,
	})
	if cancelled.Outcome != OutcomeCancelled || cancelled.ReceiptID != "" {
		t.Fatalf("cancelled question = %+v", cancelled)
	}
}
