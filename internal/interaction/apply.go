package interaction

import (
	"slices"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// applyDecisionLocked applies a validated user decision to a pending ticket.
func (o *Owner) applyDecisionLocked(ticket *Ticket, decision *Decision, now time.Time) error {
	if !knownOutcome(decision.Outcome) {
		return errcode.New(errcode.InvalidArgument, "decision outcome is invalid")
	}
	if ticket.Relayed {
		return applyRelayedLocked(ticket, decision, now)
	}
	switch ticket.Kind {
	case KindConfirm:
		return o.applyConfirmLocked(ticket, decision, now)
	case KindSelect:
		return o.applySelectLocked(ticket, decision, now)
	case KindQuestion:
		return o.applyQuestionLocked(ticket, decision, now)
	}
	return errcode.New(errcode.InvalidArgument, "interaction kind is invalid")
}

func (o *Owner) applyConfirmLocked(ticket *Ticket, decision *Decision, now time.Time) error {
	if decision.OptionID != "" {
		return errcode.New(errcode.InvalidArgument, "confirm decision must not specify an option ID")
	}
	if len(decision.Answers) > 0 {
		return errcode.New(errcode.InvalidArgument, "confirm decision must not carry question answers")
	}
	switch decision.Outcome {
	case OutcomeAllowedOnce:
		if !slices.Contains(ticket.Offers, OutcomeAllowedOnce) {
			return errcode.New(errcode.InvalidArgument, "single-use approval is not offered for this request")
		}
		receipt, err := o.issueReceiptLocked(ticket, "", nil, now)
		if err != nil {
			return err
		}
		ticket.ReceiptID = receipt.ReceiptID
	case OutcomeAllowedSession:
		if !slices.Contains(ticket.Offers, OutcomeAllowedSession) {
			return errcode.New(errcode.InvalidArgument, "session grant is not offered for this request")
		}
		receipt, err := o.issueReceiptLocked(ticket, "", nil, now)
		if err != nil {
			return err
		}
		o.grants[grantKeyFor(&ticket.Binding)] = struct{}{}
		ticket.ReceiptID = receipt.ReceiptID
	case OutcomeDenied:
		if err := checkTextLimit(decision.Text, MaxNoteBytes, "deny note"); err != nil {
			return err
		}
		ticket.Text = decision.Text
	case OutcomeCancelled, OutcomeUnavailable, OutcomeExpired, OutcomeAnswered:
		return errcode.New(errcode.InvalidArgument, "confirm decision outcome is not a valid user answer")
	}
	ticket.Resolved = true
	ticket.ResolvedAt = now
	ticket.Outcome = decision.Outcome
	return nil
}

func (o *Owner) applySelectLocked(ticket *Ticket, decision *Decision, now time.Time) error {
	if decision.Text != "" {
		return errcode.New(errcode.InvalidArgument, "select decision carries no text")
	}
	if len(decision.Answers) > 0 {
		return errcode.New(errcode.InvalidArgument, "select decision must not carry question answers")
	}
	switch decision.Outcome {
	case OutcomeAllowedOnce:
		if !hasOption(ticket.Options, decision.OptionID) {
			return errcode.New(errcode.InvalidArgument, "selected option was not offered")
		}
		receipt, err := o.issueReceiptLocked(ticket, decision.OptionID, nil, now)
		if err != nil {
			return err
		}
		ticket.SelectedOptionID = decision.OptionID
		ticket.ReceiptID = receipt.ReceiptID
	case OutcomeDenied:
		if decision.OptionID != "" {
			return errcode.New(errcode.InvalidArgument, "denied selection must not specify an option ID")
		}
	case OutcomeAllowedSession, OutcomeCancelled, OutcomeUnavailable, OutcomeExpired, OutcomeAnswered:
		return errcode.New(errcode.InvalidArgument, "selection decision outcome is not a valid user answer")
	}
	ticket.Resolved = true
	ticket.ResolvedAt = now
	ticket.Outcome = decision.Outcome
	return nil
}

// applyQuestionLocked records the answers of a KindQuestion request. An
// answered question issues a receipt that carries the answers for Execute and
// never authorizes execution (interaction §4.1).
func (o *Owner) applyQuestionLocked(ticket *Ticket, decision *Decision, now time.Time) error {
	if decision.OptionID != "" {
		return errcode.New(errcode.InvalidArgument, "question decision must not specify a top-level option ID")
	}
	if decision.Text != "" {
		return errcode.New(errcode.InvalidArgument, "question decision carries text only inside answers")
	}
	switch decision.Outcome {
	case OutcomeAnswered:
		answers, err := normalizeAnswers(ticket.Questions, decision.Answers)
		if err != nil {
			return err
		}
		receipt, err := o.issueReceiptLocked(ticket, "", answers, now)
		if err != nil {
			return err
		}
		ticket.Answers = answers
		ticket.ReceiptID = receipt.ReceiptID
	case OutcomeCancelled:
		if len(decision.Answers) > 0 {
			return errcode.New(errcode.InvalidArgument, "cancelled question decision carries no answers")
		}
	case OutcomeAllowedOnce, OutcomeAllowedSession, OutcomeDenied, OutcomeUnavailable, OutcomeExpired:
		return errcode.New(errcode.InvalidArgument, "question decision outcome is not a valid user answer")
	}
	ticket.Resolved = true
	ticket.ResolvedAt = now
	ticket.Outcome = decision.Outcome
	return nil
}

// applyRelayedLocked records the user's verbatim answer to a relayed vendor
// request (vendor-runtime VR-05). It never issues a receipt or a grant: the
// vendor's own permission system protects the vendor's tools, and the
// decision is delivered back to the caller once via TakeRelayed. Choosing one
// of the vendor's options — including the vendor's own reject — resolves the
// request as answered with the choice recorded; denied stays the Fugue-level
// deny with an optional note.
func applyRelayedLocked(ticket *Ticket, decision *Decision, now time.Time) error {
	switch decision.Outcome {
	case OutcomeAnswered:
		if err := applyRelayedChoiceLocked(ticket, decision); err != nil {
			return err
		}
	case OutcomeDenied:
		if decision.OptionID != "" {
			return errcode.New(errcode.InvalidArgument, "relayed denial must not specify an option ID")
		}
		if len(decision.Answers) > 0 {
			return errcode.New(errcode.InvalidArgument, "relayed decision must not carry question answers")
		}
		if err := checkTextLimit(decision.Text, MaxNoteBytes, "deny note"); err != nil {
			return err
		}
		ticket.Text = decision.Text
	case OutcomeAllowedOnce, OutcomeAllowedSession:
		return errcode.New(errcode.InvalidArgument, "relayed requests never create a Fugue approval")
	case OutcomeCancelled, OutcomeUnavailable, OutcomeExpired:
		return errcode.New(errcode.InvalidArgument, "relayed decision outcome is not a valid user answer")
	}
	ticket.Resolved = true
	ticket.ResolvedAt = now
	ticket.Outcome = decision.Outcome
	return nil
}

// applyRelayedChoiceLocked records a relayed choice: an option of a relayed
// confirm, or the validated answers of a relayed question, whose answers live
// on the resolved ticket only — no receipt is issued.
func applyRelayedChoiceLocked(ticket *Ticket, decision *Decision) error {
	switch ticket.Kind {
	case KindConfirm:
		if decision.OptionID == "" {
			return errcode.New(errcode.InvalidArgument, "relayed approval must choose one of the vendor options")
		}
		if !hasOption(ticket.Options, decision.OptionID) {
			return errcode.New(errcode.InvalidArgument, "relayed option was not offered")
		}
		if len(decision.Answers) > 0 {
			return errcode.New(errcode.InvalidArgument, "relayed decision must not carry question answers")
		}
		if err := checkTextLimit(decision.Text, MaxAnswerBytes, "answer text"); err != nil {
			return err
		}
		ticket.SelectedOptionID = decision.OptionID
		ticket.Text = decision.Text
	case KindQuestion:
		if decision.OptionID != "" {
			return errcode.New(errcode.InvalidArgument, "question decision must not specify a top-level option ID")
		}
		if decision.Text != "" {
			return errcode.New(errcode.InvalidArgument, "question decision carries text only inside answers")
		}
		answers, err := normalizeAnswers(ticket.Questions, decision.Answers)
		if err != nil {
			return err
		}
		ticket.Answers = answers
	case KindSelect:
		return errcode.New(errcode.InvalidArgument, "relayed requests are confirm or question interactions")
	}
	return nil
}

// normalizeAnswers validates a question decision against the asked questions
// (ask-plan §4): every question answered exactly once, options only from the
// offered set, at most one option unless the question allows multiple, and
// every answer carrying an option or text.
func normalizeAnswers(questions []Question, answers []Answer) ([]Answer, error) {
	if len(answers) != len(questions) {
		return nil, errcode.New(errcode.InvalidArgument, "question decision must answer every question exactly once")
	}
	asked := make(map[string]Question, len(questions))
	for _, q := range questions {
		asked[q.ID] = q
	}
	out := make([]Answer, 0, len(answers))
	seen := make(map[string]struct{}, len(answers))
	for _, ans := range answers {
		q, err := answerFor(asked, seen, ans)
		if err != nil {
			return nil, err
		}
		if err := checkAnswer(&q, ans); err != nil {
			return nil, err
		}
		seen[ans.QuestionID] = struct{}{}
		out = append(out, Answer{QuestionID: ans.QuestionID, OptionIDs: slices.Clone(ans.OptionIDs), Text: ans.Text})
	}
	return out, nil
}

// answerFor resolves the asked question of one answer, rejecting unknown and
// repeated question IDs.
func answerFor(asked map[string]Question, seen map[string]struct{}, ans Answer) (Question, error) {
	q, ok := asked[ans.QuestionID]
	if !ok {
		return Question{}, errcode.New(errcode.InvalidArgument, "answer references an unknown question")
	}
	if _, dup := seen[ans.QuestionID]; dup {
		return Question{}, errcode.New(errcode.InvalidArgument, "question answered more than once")
	}
	return q, nil
}

// checkAnswer validates one answer against its question (ask-plan §4).
func checkAnswer(q *Question, ans Answer) error {
	if err := checkTextLimit(ans.Text, MaxAnswerBytes, "answer text"); err != nil {
		return err
	}
	if len(ans.OptionIDs) > 1 && !q.MultiSelect {
		return errcode.New(errcode.InvalidArgument, "question without multiple selection accepts at most one option")
	}
	for _, id := range ans.OptionIDs {
		if !hasOption(q.Options, id) {
			return errcode.New(errcode.InvalidArgument, "answered option was not offered")
		}
	}
	if len(ans.OptionIDs) == 0 && ans.Text == "" {
		return errcode.New(errcode.InvalidArgument, "answer requires an option or text")
	}
	return nil
}
