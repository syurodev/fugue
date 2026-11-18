package app

// The settle path of a still-proposed team: supersede, the session-end
// cancel (F11.2a) and the solo and reject decisions (F11.2b) all settle
// every non-terminal task first, then append TeamEnded (Q-84) with the
// decision in the sealed payload, and flip the transcript item through the
// actor's single-writer journal (Q-99).

import (
	"context"
	"encoding/json"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// supersedePending rejects the session's pending proposal before a new one is
// created (Q-103).
func (s *TeamProposalService) supersedePending(ctx context.Context, sessionID string) error {
	pending, ok, err := s.store.PendingTeamForSession(ctx, sessionID)
	if err != nil {
		return boundaryError("read the pending proposal", err)
	}
	if !ok {
		return nil
	}
	return s.cancelPendingProposal(ctx, &pending, "superseded")
}

// CancelForSession cancels the session's pending proposal because its session
// ended (Q-104). It is idempotent: a proposal that is already decided or
// cancelled is left alone, and only non-terminal tasks are cancelled. The
// item flip goes through the actor, which holds its close until this cancel
// settles; a failed flip leaves the whole proposal pending, because expiry or
// closing is never an approval.
func (s *TeamProposalService) CancelForSession(ctx context.Context, sessionID string) error {
	// The same per-session lock as Propose (Q-103, Q-104): whichever runs
	// first, a proposal the cancel misses is refused by the liveness
	// re-check, and one it sees is cancelled.
	unlock := s.locks.lock(sessionID)
	defer unlock()
	// A pending amend card of the ending session never applies: its
	// invalidation is best-effort, because a stale incarnation answer fails
	// closed at the owner anyway (F11.2b). It runs whether or not the
	// session also holds a pending proposal.
	s.pendingAmends.cancelForSession(sessionID, s.interactions.owner)
	pending, ok, err := s.store.PendingTeamForSession(ctx, sessionID)
	if err != nil {
		return boundaryError("read the pending proposal", err)
	}
	if !ok {
		return nil
	}
	// Fail closed: the item flip comes first and a failure leaves the
	// proposal fully pending (team, tasks and card untouched), never a
	// cancelled team behind a still-actionable card. Only a transient
	// unavailable is retried; the flip is idempotent.
	var markErr error
	for range sessionEndFlipAttempts {
		if markErr = s.markItemRejected(ctx, pending.SessionID, pending.TeamID); markErr == nil || errcode.CodeOf(markErr) != errcode.Unavailable {
			break
		}
	}
	if markErr != nil {
		return markErr
	}
	return s.cancelPendingTeam(ctx, &pending, "session ended")
}

// cancelPendingProposal settles tasks first, then appends TeamEnded (Q-84).
// Supersede updates the transcript item synchronously before any store
// append, so a failure there leaves the durable state untouched; the steps
// after it are each idempotent, so a retried cancel finishes a partially
// cancelled proposal.
func (s *TeamProposalService) cancelPendingProposal(ctx context.Context, pending *vendorstore.TeamRecord, note string) error {
	if pending.State != vendorstore.TeamStateProposed {
		return nil
	}
	if markErr := s.markItemRejected(ctx, pending.SessionID, pending.TeamID); markErr != nil {
		return markErr
	}
	return s.cancelPendingTeam(ctx, pending, note)
}

// cancelPendingTeam invalidates the card and settles the durable team: the
// card first, so supersede's own card finds the session's single pending
// slot free (Q-103), then each non-terminal task, then TeamEnded.
func (s *TeamProposalService) cancelPendingTeam(ctx context.Context, pending *vendorstore.TeamRecord, note string) error {
	// The card goes first: a cancelled proposal must not leave a pending
	// card behind.
	operation, err := interaction.NativeOperationKey(pending.TeamID, pending.TeamID, 1)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "key the proposal card", err)
	}
	if _, invalErr := s.interactions.owner.InvalidateOperation(operation); invalErr != nil {
		return boundaryError("invalidate the proposal card", invalErr)
	}
	payload, err := json.Marshal(cancelDecisionPayload{Decision: decisionRejectedItem, Note: note})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal the cancel decision", err)
	}
	return s.settleTeamDecision(ctx, pending, payload)
}

// settleTeamDecision settles the durable team of a decision (Q-84): every
// non-terminal task cancels first, then TeamEnded appends with the decision
// in the sealed payload. The caller flipped or resolves the card separately.
func (s *TeamProposalService) settleTeamDecision(ctx context.Context, pending *vendorstore.TeamRecord, payload []byte) error {
	_, tasks, err := s.store.TeamSnapshot(ctx, pending.TeamID)
	if err != nil {
		return boundaryError("read the proposal's tasks", err)
	}
	next := pending.LastSeq
	for i := range tasks {
		if taskStateTerminal(tasks[i].State) {
			continue
		}
		eventID, idErr := s.ids.NewID()
		if idErr != nil {
			return errcode.Wrap(errcode.Internal, "generate the task cancel event ID", idErr)
		}
		if next, err = s.store.AppendTeamEvent(ctx, pending.TeamID, next, &vendorstore.AppendTeamEventInput{
			EventID: eventID, TaskID: tasks[i].TaskID, EventType: vendorstore.EventTaskEnded,
			Outcome: string(vendorstore.TeamStateCancelled),
		}); err != nil {
			return boundaryError("cancel a proposal task", err)
		}
	}
	endID, err := s.ids.NewID()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "generate the team end event ID", err)
	}
	_, err = s.store.AppendTeamEvent(ctx, pending.TeamID, next, &vendorstore.AppendTeamEventInput{
		EventID: endID, EventType: vendorstore.EventTeamEnded,
		Outcome: string(vendorstore.TeamStateCancelled), ProtectedPayload: payload,
	})
	return boundaryError("end the decided proposal", err)
}

// cancelDecisionPayload is the sealed payload of a TeamEnded append that
// ends a still-proposed team: supersede and the session-end cancel (F11.2a),
// and the user's solo and reject decisions (F11.2b).
type cancelDecisionPayload struct {
	Decision  string `json:"decision"`
	DecidedBy string `json:"decided_by,omitempty"`
	Note      string `json:"note,omitempty"`
}

// taskStateTerminal reports the terminal task states (§3.1); unknown is not
// terminal (Q-84).
func taskStateTerminal(state vendorstore.TaskState) bool {
	switch state {
	case vendorstore.TaskStateDone, vendorstore.TaskStateFailed,
		vendorstore.TaskStateStopped, vendorstore.TaskStateCancelled:
		return true
	case vendorstore.TaskStateQueued, vendorstore.TaskStateStarting,
		vendorstore.TaskStateRunning, vendorstore.TaskStateAsking,
		vendorstore.TaskStateChecking, vendorstore.TaskStateReview,
		vendorstore.TaskStateIntegrating, vendorstore.TaskStateUnknown:
		return false
	}
	return false
}

// markItemRejected flips the session's team_proposal item to rejected
// through the actor's single-writer journal (F11.2a supersede and cancel).
func (s *TeamProposalService) markItemRejected(ctx context.Context, sessionID, teamID string) error {
	return s.markItemDecision(ctx, sessionID, teamID, decisionRejectedItem)
}

// markItemDecision flips the session's team_proposal item to decision through
// the actor's single-writer journal (Q-99). A missing item is skipped: the
// durable team state stays authoritative.
func (s *TeamProposalService) markItemDecision(ctx context.Context, sessionID, teamID, decision string) error {
	itemID, itemErr := s.findProposalItem(ctx, sessionID, teamID)
	if itemErr != nil || itemID == "" {
		return itemErr
	}
	stored, content, readErr := s.store.ReadItem(ctx, sessionID, itemID)
	if readErr != nil {
		return boundaryError("read the proposal item", readErr)
	}
	metadata := stored.Metadata
	metadata.Decision = decision
	recordErr := s.recorder.RecordEvent(ctx, sessionID, &vendorstore.Event{
		Type: vendorstore.TranscriptItemUpdated, TurnID: stored.TurnID,
		Item: &vendorstore.Item{
			ID: stored.ID, TurnID: stored.TurnID, Kind: stored.Kind,
			Revision: stored.Revision, Metadata: metadata, Content: content,
		},
	})
	return boundaryError("record the decided proposal item", recordErr)
}

// findProposalItem looks the proposal item up by its proposal ID through the
// transcript metadata index (0011), so a proposal stays flippable for the
// whole life of a long session (F11.2a review).
func (s *TeamProposalService) findProposalItem(ctx context.Context, sessionID, teamID string) (string, error) {
	itemID, ok, err := s.store.ProposalItemID(ctx, sessionID, teamID)
	if err != nil {
		return "", boundaryError("look up the proposal item", err)
	}
	if !ok {
		return "", nil
	}
	return itemID, nil
}
