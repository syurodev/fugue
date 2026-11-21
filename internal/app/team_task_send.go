package app

// The send paths of the team task tools (§6.2, §6.7, Q-177 A, Q-178 A): the
// delegator message to a direct child rides the mailbox unchanged, while a
// sibling send appends one TaskMessaged event whose transaction delivers the
// target's row and the delegator's informational mention, projects the
// channel `send` line, and enforces the 6/30-per-10-minutes caps.

import (
	"context"
	"encoding/json"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// taskMessagePayload is the sealed payload of EventTaskMessaged (§6.7,
// Q-177 A): the sibling send's target and its informational message.
type taskMessagePayload struct {
	ToTaskID string `json:"to_task_id"`
	Message  string `json:"message"`
}

// Send delivers a follow-up message: to a running or asking direct child
// task (§6.2, lead or team manager), or — for a team manager or member — as
// an informational sibling send to a task of the same direct delegator (§6.7,
// Q-177 A), capped at 6 per agent and 30 per team per 10 minutes.
func (s *teamTaskService) Send(ctx context.Context, sessionID, taskID, message string) (string, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if err := teams.ValidateDurableTaskID("task_id", taskID); err != nil {
		return "", errcode.New(errcode.InvalidArgument, err.Error())
	}
	target, ok := findTask(c.tasks, taskID)
	if !ok {
		return "", errcode.New(errcode.NotFound, "team task not found")
	}
	byID, retryChild := resolveTaskChain(c.tasks)
	if sameAttemptChain(target.ParentTaskID, c.caller.TaskID, byID, retryChild) || c.leadTakeoverOf(target, byID, retryChild) {
		return s.sendToChild(ctx, c, taskID, message)
	}
	return s.sendToSibling(ctx, c, target, message)
}

// sendToChild is the §6.2 delegator message: it rides the mailbox directly
// and stays event-less (Q-177 A: the existing delegator→child send is
// unchanged; only sibling sends become events).
func (s *teamTaskService) sendToChild(ctx context.Context, c *callerTeamContext, taskID, message string) (string, error) {
	target, err := c.requireDelegator(taskID)
	if err != nil {
		return "", err
	}
	if target.State != vendorstore.TaskStateRunning && target.State != vendorstore.TaskStateAsking {
		return "", errcode.New(errcode.FailedPrecondition, "task is not accepting messages in its current state")
	}
	trimmed, err := validateDigestMessage("message", message)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(mailboxDigestItem{TaskID: c.caller.TaskID, State: "message", Summary: trimmed})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode task send payload", err)
	}
	uid, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate message ID", err)
	}
	if err := s.store.EnqueueDirectMailbox(ctx, c.team.TeamID, target.TaskID, vendorstore.MailboxKindSend, uid, payload); err != nil {
		return "", boundaryError("enqueue task message", err)
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	return "message queued for task " + target.TaskID, nil
}

// sendToSibling appends one EventTaskMessaged (Q-178 A) whose producing
// transaction enqueues the target's delivery row and the delegator's
// informational mention (§6.7: the delegator sees sibling sends in its next
// digest, no separate wake), and projects the channel's `send` line.
func (s *teamTaskService) sendToSibling(ctx context.Context, c *callerTeamContext, target *vendorstore.TaskRecord, message string) (string, error) {
	if c.role != vendorstore.RoleTeamManager && c.role != vendorstore.RoleMember {
		return "", errcode.New(errcode.PermissionDenied, "sibling sends are a team manager or member tool (§6.7)")
	}
	if c.caller == nil || c.caller.State != vendorstore.TaskStateRunning {
		return "", errcode.New(errcode.FailedPrecondition, "caller task is not running")
	}
	if target.TaskID == c.caller.TaskID {
		return "", errcode.New(errcode.FailedPrecondition, "cannot send to your own task")
	}
	byID, retryChild := resolveTaskChain(c.tasks)
	if !sameAttemptChain(c.caller.ParentTaskID, target.ParentTaskID, byID, retryChild) {
		return "", errcode.New(errcode.PermissionDenied, "sibling sends only reach tasks of the same direct delegator")
	}
	if target.State != vendorstore.TaskStateRunning && target.State != vendorstore.TaskStateAsking {
		return "", errcode.New(errcode.FailedPrecondition, "task is not accepting messages in its current state")
	}
	trimmed, err := validateDigestMessage("message", message)
	if err != nil {
		return "", err
	}
	targetID := target.TaskID
	if err := s.appendTeamWithRetry(ctx, c.sessionID, func(ctx context.Context, cur *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		return s.buildSiblingSendEvent(ctx, cur, targetID, trimmed)
	}); err != nil {
		return "", err
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	return "message queued for sibling task " + targetID, nil
}

// buildSiblingSendEvent re-checks the sliding caps and the target state on
// the fresh snapshot inside every append attempt (§6.7, Q-177 A).
func (s *teamTaskService) buildSiblingSendEvent(ctx context.Context, c *callerTeamContext, targetID, trimmed string) (*vendorstore.AppendTeamEventInput, error) {
	t, ok := findTask(c.tasks, targetID)
	if !ok || (t.State != vendorstore.TaskStateRunning && t.State != vendorstore.TaskStateAsking) {
		return nil, errcode.New(errcode.FailedPrecondition, "task is not accepting messages in its current state")
	}
	byAgent, team, err := s.store.CountTaskMessages(ctx, c.team.TeamID, c.caller.TaskID, s.now().Add(-siblingSendWindow).UnixMilli())
	if err != nil {
		return nil, boundaryError("count the sibling sends", err)
	}
	if byAgent >= siblingSendsPerAgent {
		return nil, errcode.New(errcode.TasksSendRateLimited, "sibling send limit reached for this agent (6 per 10 minutes)")
	}
	if team >= siblingSendsPerTeam {
		return nil, errcode.New(errcode.TasksSendRateLimited, "sibling send limit reached for the team (30 per 10 minutes)")
	}
	payload, err := json.Marshal(taskMessagePayload{ToTaskID: t.TaskID, Message: trimmed})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "encode sibling send payload", err)
	}
	item, err := json.Marshal(mailboxDigestItem{TaskID: c.caller.TaskID, State: "message", Summary: trimmed})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "encode sibling send mail", err)
	}
	evID, err := s.ids.NewID()
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate sibling send event ID", err)
	}
	return &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: c.caller.TaskID, EventType: vendorstore.EventTaskMessaged,
		ProtectedPayload: payload,
		Mailbox: []vendorstore.MailboxEnqueueInput{
			{RecipientTaskID: t.TaskID, Kind: vendorstore.MailboxKindSend, Payload: item},
			{RecipientTaskID: resolvedParentID(c.caller.ParentTaskID, c.tasks), Kind: vendorstore.MailboxKindSend, Payload: item, Informational: true},
		},
	}, nil
}
