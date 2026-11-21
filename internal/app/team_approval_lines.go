package app

// F11.10b approval reference lines (§6.7, Q-178 A): when a team task's
// session raises a vendor tool approval, the team event log records
// TaskApprovalRequested; when it resolves, TaskApprovalResolved. The sealed
// payloads carry only the reference — the interaction request, the tool and
// the outcome — never the request or answer content, which stays in the
// approvals inbox (interaction §7, VR-05).

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// ObserveApproval appends the approval reference lines of one interaction
// lifecycle event. It runs off the owner's goroutine (the lifecycle hook must
// be non-blocking), so a failed append only costs the line, never the
// approval: the inbox stays the authority (VR-05).
func (s *teamTaskService) ObserveApproval(ctx context.Context, ev interaction.LifecycleEvent) {
	if s.interactions == nil {
		return
	}
	ticket := ev.Ticket
	if ticket == nil {
		// A resolved event carries only the request ID; the binding comes
		// from the ticket the owner still holds.
		lookedUp, lookupErr := s.interactions.TicketOf(ev.RequestID)
		if lookupErr != nil {
			slog.WarnContext(ctx, "looking up the resolved interaction failed", "error_code", string(errcode.CodeOf(lookupErr)))
			return
		}
		ticket = lookedUp
	}
	teamID, taskID, ok := s.teamTaskOfTicket(ctx, ticket)
	if !ok {
		return
	}
	tool := ticket.Binding.ToolName
	requestID := ev.RequestID
	if requestID == "" {
		requestID = ticket.RequestID
	}
	outcome := ""
	if ev.Kind == interaction.EventResolved {
		outcome = string(ev.Outcome)
	}
	payload, err := json.Marshal(taskApprovalRefPayload{RequestID: requestID, Tool: tool, Outcome: outcome})
	if err != nil {
		slog.WarnContext(ctx, "encoding the approval reference failed", "error_code", string(errcode.CodeOf(err)))
		return
	}
	evID, idErr := s.ids.NewID()
	if idErr != nil {
		slog.WarnContext(ctx, "generating the approval reference event ID failed", "error_code", string(errcode.CodeOf(idErr)))
		return
	}
	eventType := vendorstore.EventTaskApprovalRequested
	if ev.Kind == interaction.EventResolved {
		eventType = vendorstore.EventTaskApprovalResolved
	}
	if _, err := s.store.AppendTeamEvent(ctx, teamID, s.teamSeq(ctx, teamID), &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: taskID, EventType: eventType, ProtectedPayload: payload,
	}); err != nil {
		slog.WarnContext(ctx, "recording the approval reference line failed", "team_id", teamID, "task_id", taskID, "error_code", string(errcode.CodeOf(err)))
	}
}

// teamTaskOfTicket resolves the team task a ticket belongs to: the binding
// names the session that raised it. Non-team sessions record nothing.
func (s *teamTaskService) teamTaskOfTicket(ctx context.Context, ticket *interaction.Ticket) (string, string, bool) {
	if ticket == nil || ticket.Binding.SessionID == "" {
		return "", "", false
	}
	task, found, err := s.store.TaskForSession(ctx, ticket.Binding.SessionID)
	if err != nil || !found {
		return "", "", false
	}
	return task.TeamID, task.TaskID, true
}

// teamSeq reads the team's last sequence for one append; a vanished team
// records nothing.
func (s *teamTaskService) teamSeq(ctx context.Context, teamID string) int64 {
	rec, _, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil || rec.State == vendorstore.TeamStateProposed {
		return 0
	}
	return rec.LastSeq
}
