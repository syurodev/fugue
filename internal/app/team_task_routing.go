package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	taskQuestionCardID = "answer"
	taskQuestionHeader = "needs you"
)

// taskQuestionResolver validates and applies user answers to task question
// cards created by fugue_task_ask(to=user) or fugue_task_answer(forward_to=user).
type taskQuestionResolver interface {
	validateTaskQuestionAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error
	applyTaskQuestionAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error
}

// SetTaskQuestionResolver wires the task-question card consumer on startup.
func (s *InteractionService) SetTaskQuestionResolver(r taskQuestionResolver) {
	s.taskQuestions = r
}

func isTaskQuestionCard(ticket *interaction.Ticket) bool {
	return ticket.Binding.ToolName == taskAskToolName
}

// Answer responds to an asking child task directly or forwards its question
// up one tier (`parent`) or to the user question card (`user`) (Q-129, OR-E14).
func (s *teamTaskService) Answer(ctx context.Context, sessionID, taskID, answer, forwardTo string) (string, error) {
	trimmed, err := validateAnswerInput(answer, forwardTo)
	if err != nil {
		return "", err
	}
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	target, err := c.requireDelegator(taskID)
	if err != nil {
		return "", err
	}
	if target.State != vendorstore.TaskStateAsking {
		return "", errcode.New(errcode.FailedPrecondition, "task is not waiting for an answer")
	}
	switch forwardTo {
	case "":
		if err := s.resolveQuestionChain(ctx, c.team.TeamID, target.TaskID, trimmed, c.caller.TaskID); err != nil {
			return "", err
		}
		return "answer queued for task " + target.TaskID, nil
	case askTargetParent:
		return s.forwardQuestionToParent(ctx, sessionID, c, target)
	default:
		return s.forwardQuestionToUser(ctx, c, target)
	}
}

func validateAnswerInput(answer, forwardTo string) (string, error) {
	trimmed := strings.TrimSpace(answer)
	if (trimmed == "" && forwardTo == "") || (trimmed != "" && forwardTo != "") {
		return "", errcode.New(errcode.InvalidArgument, "provide either answer or forward_to, not both or neither")
	}
	if len(trimmed) > maxTaskSendBytes {
		return "", errcode.New(errcode.InvalidArgument, "answer exceeds 16 KiB limit")
	}
	if forwardTo != "" && forwardTo != askTargetParent && forwardTo != askTargetUser {
		return "", errcode.New(errcode.InvalidArgument, "forward_to must be parent or user")
	}
	return trimmed, nil
}

func (s *teamTaskService) forwardQuestionToParent(ctx context.Context, sessionID string, c *callerTeamContext, target *vendorstore.TaskRecord) (string, error) {
	if c.role != vendorstore.RoleTeamManager {
		return "", errcode.New(errcode.FailedPrecondition, "only a team manager may forward a question to parent")
	}
	q, _, err := s.readTaskQuestion(ctx, c.team.TeamID, target.TaskID)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(taskQuestionPayload{
		Question: q, To: askTargetParent, ForwardedFromTaskID: target.TaskID,
	})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode forwarded question payload", err)
	}
	var managerID string
	if err := s.appendTeamWithRetry(ctx, sessionID, func(_ context.Context, cur *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		curTarget, delErr := cur.requireDelegator(target.TaskID)
		if delErr != nil {
			return nil, delErr
		}
		if curTarget.State != vendorstore.TaskStateAsking {
			return nil, errcode.New(errcode.FailedPrecondition, "task is not waiting for an answer")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate forward event ID", idErr)
		}
		managerID = cur.caller.TaskID
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: cur.caller.TaskID, EventType: vendorstore.EventTaskQuestionAsked,
			ProtectedPayload: payload,
			Mailbox:          digestSummaryRows(cur.caller.TaskID, cur.caller.ParentTaskID, string(vendorstore.TaskStateAsking), "", q, true),
		}, nil
	}); err != nil {
		return "", err
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	return fmt.Sprintf("task %s forwarded question from %s to parent", managerID, target.TaskID), nil
}

func (s *teamTaskService) forwardQuestionToUser(ctx context.Context, c *callerTeamContext, target *vendorstore.TaskRecord) (string, error) {
	q, askedSeq, err := s.readTaskQuestion(ctx, c.team.TeamID, target.TaskID)
	if err != nil {
		return "", err
	}
	if err := s.ensureUserQuestionCard(c.team.TeamID, c.team.WorkspaceID, target, askedSeq, q); err != nil {
		return "", err
	}
	return "question for task " + target.TaskID + " forwarded to user", nil
}

func (s *teamTaskService) readTaskQuestion(ctx context.Context, teamID, taskID string) (string, int64, error) {
	raw, seq, found, err := s.store.OpenLatestTaskEventPayloadWithSeq(ctx, teamID, taskID, vendorstore.EventTaskQuestionAsked)
	if err != nil {
		return "", 0, boundaryError("open task question", err)
	}
	if !found || seq == 0 {
		return "", 0, errcode.New(errcode.NotFound, "task question not found")
	}
	var qp taskQuestionPayload
	if unmErr := json.Unmarshal(raw, &qp); unmErr != nil || qp.Question == "" {
		return "", 0, errcode.New(errcode.FailedPrecondition, "task question payload is not readable")
	}
	return qp.Question, seq, nil
}

func (s *teamTaskService) ensureUserQuestionCard(teamID, workspaceID string, target *vendorstore.TaskRecord, askedSeq int64, question string) error {
	if s.interactions == nil {
		return errcode.New(errcode.FailedPrecondition, "interaction service is not configured")
	}
	if askedSeq <= 0 {
		return errcode.New(errcode.InvalidArgument, "question sequence must be positive")
	}
	opID, err := interaction.NativeOperationKey(teamID, target.TaskID, uint64(askedSeq))
	if err != nil {
		return errcode.Wrap(errcode.Internal, "key user question card", err)
	}
	sessID := target.SessionID
	if sessID == "" {
		sessID = target.TaskID
	}
	spec := &interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindQuestion,
		Summary:     truncateCardText("Task "+target.TaskID+" needs your input", interaction.MaxSummaryBytes),
		Body:        truncateCardText("Team: "+teamID+"\nTask: "+target.TaskID+" ("+target.CatalogRole+")", interaction.MaxPromptBytes),
		Questions: []interaction.Question{{
			ID:       taskQuestionCardID,
			Question: truncateCardText(question, interaction.MaxPromptBytes),
			Header:   taskQuestionHeader,
		}},
		Binding: interaction.Binding{
			SessionID:       sessID,
			WorkspaceID:     workspaceID,
			InvocationID:    teamID,
			AttemptID:       target.TaskID,
			CheckGeneration: uint64(askedSeq),
			ToolName:        taskAskToolName,
		},
	}
	if _, err := s.interactions.Ensure(spec); err != nil {
		return err
	}
	return nil
}

func (s *teamTaskService) validateTaskQuestionAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	if decision.Outcome == interaction.OutcomeAnswered {
		if _, err := taskQuestionAnswerText(decision); err != nil {
			return err
		}
	}
	rec, tasks, err := s.store.TeamSnapshot(ctx, ticket.Binding.InvocationID)
	if err != nil {
		return boundaryError("read the asking team", err)
	}
	if rec.State != vendorstore.TeamStateActive {
		return errcode.New(errcode.FailedPrecondition, "the team is no longer active")
	}
	task, ok := findTask(tasks, ticket.Binding.AttemptID)
	if !ok || task.State != vendorstore.TaskStateAsking {
		return errcode.New(errcode.FailedPrecondition, "the asking task is no longer waiting for an answer")
	}
	return nil
}

func (s *teamTaskService) applyTaskQuestionAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	answerText := "The user cancelled the question without providing an answer."
	if decision.Outcome == interaction.OutcomeAnswered {
		text, err := taskQuestionAnswerText(decision)
		if err != nil {
			return err
		}
		answerText = text
	}
	return s.resolveQuestionChain(ctx, ticket.Binding.InvocationID, ticket.Binding.AttemptID, answerText, "user")
}

func taskQuestionAnswerText(decision *interaction.Decision) (string, error) {
	for i := range decision.Answers {
		a := &decision.Answers[i]
		if a.QuestionID != taskQuestionCardID {
			continue
		}
		if len(a.OptionIDs) > 0 {
			return "", errcode.New(errcode.InvalidArgument, "task question answer takes free text only")
		}
		trimmed := strings.TrimSpace(a.Text)
		if trimmed == "" {
			return "", errcode.New(errcode.InvalidArgument, "task question answer text is required")
		}
		return trimmed, nil
	}
	return "", errcode.New(errcode.InvalidArgument, "answer for task question is required")
}

func (s *teamTaskService) resolveQuestionChain(ctx context.Context, teamID, topTaskID, answerText, answeredBy string) error {
	for attempt := range maxAppendRetries {
		evs, err := s.buildQuestionResolutionEvents(ctx, teamID, topTaskID, answerText, answeredBy)
		if err != nil {
			return err
		}
		rec, _, snapErr := s.store.TeamSnapshot(ctx, teamID)
		if snapErr != nil {
			return boundaryError("read the team", snapErr)
		}
		if _, appendErr := s.store.AppendTeamEvents(ctx, teamID, rec.LastSeq, evs); appendErr == nil {
			s.invalidateResolvedCards(ctx, teamID, evs, answeredBy)
			if s.wake != nil {
				s.wake.Notify()
			}
			return nil
		} else if !errors.Is(appendErr, vendorstore.ErrSequenceConflict) || attempt+1 == maxAppendRetries {
			return boundaryError("record task answer", appendErr)
		}
	}
	return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

func (s *teamTaskService) buildQuestionResolutionEvents(ctx context.Context, teamID, topTaskID, answerText, answeredBy string) ([]*vendorstore.AppendTeamEventInput, error) {
	rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return nil, boundaryError("read the team", err)
	}
	if rec.State != vendorstore.TeamStateActive {
		return nil, errcode.New(errcode.FailedPrecondition, "the team is not active")
	}
	top, ok := findTask(tasks, topTaskID)
	if !ok || top.State != vendorstore.TaskStateAsking {
		return nil, errcode.New(errcode.FailedPrecondition, "task is not waiting for an answer")
	}
	payload, err := json.Marshal(struct {
		Answer     string `json:"answer"`
		AnsweredBy string `json:"answered_by"`
	}{Answer: answerText, AnsweredBy: answeredBy})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "encode answer payload", err)
	}
	mailPayload, err := json.Marshal(mailboxDigestItem{TaskID: answeredBy, State: "answer", Summary: answerText})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "encode answer mailbox payload", err)
	}
	return s.collectChainAnswerEvents(ctx, teamID, topTaskID, tasks, payload, mailPayload)
}

func (s *teamTaskService) collectChainAnswerEvents(
	ctx context.Context,
	teamID, topTaskID string,
	tasks []vendorstore.TaskRecord,
	payload, mailPayload []byte,
) ([]*vendorstore.AppendTeamEventInput, error) {
	seen := make(map[string]bool, 4)
	var evs []*vendorstore.AppendTeamEventInput
	curID := topTaskID
	for curID != "" && !seen[curID] {
		seen[curID] = true
		cur, ok := findTask(tasks, curID)
		if !ok || cur.State != vendorstore.TaskStateAsking {
			break
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate answer event ID", idErr)
		}
		evs = append(evs, &vendorstore.AppendTeamEventInput{
			EventID:          evID,
			TaskID:           curID,
			EventType:        vendorstore.EventTaskQuestionAnswered,
			ProtectedPayload: payload,
			Mailbox: []vendorstore.MailboxEnqueueInput{{
				RecipientTaskID: curID,
				Kind:            vendorstore.MailboxKindAnswer,
				Payload:         mailPayload,
			}},
		})
		curID = s.forwardedFromTaskID(ctx, teamID, curID)
	}
	return evs, nil
}

func (s *teamTaskService) forwardedFromTaskID(ctx context.Context, teamID, taskID string) string {
	raw, _, found, err := s.store.OpenLatestTaskEventPayloadWithSeq(ctx, teamID, taskID, vendorstore.EventTaskQuestionAsked)
	if err != nil || !found {
		return ""
	}
	var qp taskQuestionPayload
	if json.Unmarshal(raw, &qp) != nil {
		return ""
	}
	return qp.ForwardedFromTaskID
}

func (s *teamTaskService) invalidateResolvedCards(ctx context.Context, teamID string, evs []*vendorstore.AppendTeamEventInput, answeredBy string) {
	if answeredBy == decidedByUser || s.interactions == nil || s.interactions.owner == nil {
		return
	}
	for _, ev := range evs {
		_, seq, found, err := s.store.OpenLatestTaskEventPayloadWithSeq(ctx, teamID, ev.TaskID, vendorstore.EventTaskQuestionAsked)
		if err != nil || !found || seq <= 0 {
			continue
		}
		opID, opErr := interaction.NativeOperationKey(teamID, ev.TaskID, uint64(seq))
		if opErr != nil {
			continue
		}
		if _, invErr := s.interactions.owner.InvalidateOperation(opID); invErr != nil {
			slog.WarnContext(ctx, "invalidating resolved task question card failed", "task_id", ev.TaskID, "error_code", string(errcode.CodeOf(invErr)))
		}
	}
}
