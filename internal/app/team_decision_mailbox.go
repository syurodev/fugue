package app

// The OR-E13 delivery (Q-99, Q-124): on approve the proposing session
// receives the approved plan and the change list, on solo and reject it
// receives the user's note. The message is enqueued in the SAME transaction
// as the decision event (Q-122) and reaches the session through the wake
// pump's mailbox turn; the plan's task text is model output and travels in
// the digest's escaped data blocks (OR-13).

import (
	"encoding/json"
	"fmt"

	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// leadTaskOf returns the ID of the lead task, the mailbox recipient of every
// decision message: the proposing session is bound to it (Q-117).
func leadTaskOf(tasks []vendorstore.TaskRecord) string {
	for i := range tasks {
		if tasks[i].HierarchyRole == vendorstore.RoleLead {
			return tasks[i].TaskID
		}
	}
	return ""
}

// decisionMailboxRows builds the decision message of one §5.3 decision. The
// recipient is the lead task, so the message ID keys to the decision event's
// sequence and enqueue is idempotent with the decision itself (Q-122).
func decisionMailboxRows(leadTaskID, decision, decidedBy, note string, input, edited *teams.ProposalInput, mutations *decisionMutations) []vendorstore.MailboxEnqueueInput {
	if leadTaskID == "" {
		return nil
	}
	plan := input
	if edited != nil {
		plan = edited
	}
	payload, err := json.Marshal(mailboxDecision{
		Decision: decision, DecidedBy: decidedBy, Note: note,
		Changes: decisionChangeLines(input, edited, mutations), Plan: plan,
	})
	if err != nil {
		return nil
	}
	return []vendorstore.MailboxEnqueueInput{{
		RecipientTaskID: leadTaskID, Kind: vendorstore.MailboxKindDecision, Payload: payload,
	}}
}

// soloDecisionMailbox builds the message a solo or reject decision delivers:
// the user's note is Fugue text because the user typed it (OR-E13).
func soloDecisionMailbox(leadTaskID, decision, decidedBy, note string) []vendorstore.MailboxEnqueueInput {
	if leadTaskID == "" {
		return nil
	}
	payload, err := json.Marshal(mailboxDecision{Decision: decision, DecidedBy: decidedBy, Note: note})
	if err != nil {
		return nil
	}
	return []vendorstore.MailboxEnqueueInput{{
		RecipientTaskID: leadTaskID, Kind: vendorstore.MailboxKindDecision, Payload: payload,
	}}
}

// decisionChangeLines lists what the user changed in Fugue's own words
// (OR-E13); a plain approve carries none.
func decisionChangeLines(input, edited *teams.ProposalInput, mutations *decisionMutations) []string {
	if edited == nil {
		return nil
	}
	var lines []string
	for _, id := range mutations.removed {
		lines = append(lines, fmt.Sprintf("Removed task %s.", id))
	}
	for i := range mutations.added {
		lines = append(lines, fmt.Sprintf("Added task %s (role %s).", mutations.added[i].TaskID, mutations.added[i].CatalogRole))
	}
	for i := range mutations.reseals {
		lines = append(lines, fmt.Sprintf("Task %s: the approved edits changed its settings.", mutations.reseals[i].TaskID))
	}
	before := make(map[string]teams.ProposedRole, len(input.Roles))
	for i := range input.Roles {
		before[input.Roles[i].Name] = input.Roles[i]
	}
	for i := range edited.Roles {
		role := &edited.Roles[i]
		was, known := before[role.Name]
		if !known || (was.Model == role.Model && was.Effort == role.Effort) {
			continue
		}
		lines = append(lines, fmt.Sprintf("Role %s runs %s model %s.", role.Name, role.Vendor, role.Model))
	}
	return lines
}

// notifyMailbox asks the wake pump for a delivery pass after a decision
// enqueued mail; nil leaves the mail for the next trigger.
func (s *TeamProposalService) notifyMailbox() {
	if s.onMailbox != nil {
		s.onMailbox()
	}
}
