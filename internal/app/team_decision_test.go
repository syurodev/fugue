package app

// F11.2b decision evidence: the four §5.3 decisions, the fail-closed rules
// and the auto-approve gate. Every decision flows through the real
// interaction answer path, so the tests drive the same code a client does.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

// Shared literals of the F11.2b decision and amend tests.
const (
	teamTestModelEdited = "edited-model"
	teamTestTaskA       = "t-a"
	teamTestTaskB       = "t-b"
	teamTestTaskC       = "t-c"
	teamTestWriterRole  = "writer"
	teamTestAmendModel  = "amended-model"
)

// answerCard answers the session's pending card of the given tool as the
// local principal, choosing one option of questionID with optional text.
func (f *teamProposalFixture) answerCard(t *testing.T, commandID, tool, questionID, option, text string) error {
	t.Helper()
	cards := f.pendingCardsTool(t, tool)
	if len(cards) != 1 {
		t.Fatalf("pending %s cards = %d, want 1", tool, len(cards))
	}
	_, err := f.rt.AnswerInteraction(t.Context(), &AnswerInteractionCommand{
		CommandID: commandID, Principal: testAppPrincipal,
		Incarnation: f.rt.interactions.Incarnation(),
		RequestID:   cards[0].RequestID, Outcome: OutcomeAnswered,
		Answers: []InteractionAnswer{{QuestionID: questionID, OptionIDs: []string{option}, Text: text}},
	})
	return err
}

// proposeApprovedTeam proposes the valid proposal and approves it through the
// card, returning the team ID of an active team.
func proposeApprovedTeam(t *testing.T, f *teamProposalFixture, commandID string) string {
	t.Helper()
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if answerErr := f.answerCard(t, commandID, teamProposeToolName, teamDecisionQuestionID, decisionApprove, ""); answerErr != nil {
		t.Fatalf("approve the proposal: %v", answerErr)
	}
	return teamID
}

// assertActiveTeam asserts the team is active with every task queued and no
// task ever assigned — approve activates and queues, and nothing starts
// (Q-98, OR-E13's state half).
func assertActiveTeam(t *testing.T, f *teamProposalFixture, teamID string) vendorstore.TeamRecord {
	t.Helper()
	rec, tasks := f.teamSnapshot(t, teamID)
	if rec.State != vendorstore.TeamStateActive {
		t.Fatalf("team state = %s, want active", rec.State)
	}
	const wantTasks = 3
	if len(tasks) != wantTasks {
		t.Fatalf("task count = %d, want %d", len(tasks), wantTasks)
	}
	for i := range tasks {
		if tasks[i].State != vendorstore.TaskStateQueued {
			t.Fatalf("task %s state = %s, want queued", tasks[i].TaskID, tasks[i].State)
		}
	}
	events, err := f.hp.vendorStore.TeamEventsSince(t.Context(), teamID, 0, 100)
	if err != nil {
		t.Fatalf("TeamEventsSince: %v", err)
	}
	for i := range events {
		if events[i].EventType == vendorstore.EventTaskAssigned {
			t.Fatalf("task %s was assigned by the decision; nothing starts before F11.3", events[i].TaskID)
		}
	}
	return rec
}

// assertSessionsCount asserts the host runs exactly want vendor sessions.
func assertSessionsCount(t *testing.T, f *teamProposalFixture, want int) {
	t.Helper()
	sessions, err := f.hp.vendorStore.ListSessions(t.Context())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != want {
		t.Fatalf("sessions = %d, want %d", len(sessions), want)
	}
}

// teamEndedPayload opens the sealed payload of the team's last event.
func teamEndedPayload(t *testing.T, f *teamProposalFixture, teamID string, rec *vendorstore.TeamRecord) cancelDecisionPayload {
	t.Helper()
	payload, err := f.hp.vendorStore.OpenTeamEventPayload(t.Context(), teamID, rec.LastSeq)
	if err != nil {
		t.Fatalf("OpenTeamEventPayload: %v", err)
	}
	var decision cancelDecisionPayload
	if unmarshalErr := json.Unmarshal(payload, &decision); unmarshalErr != nil {
		t.Fatalf("unmarshal the decision payload: %v", unmarshalErr)
	}
	return decision
}

// TestApproveQueuesRootTasksAndStartsNothing pins Q-98: approve makes the
// team active with its tasks queued, records the decision, leaves no pending
// card and starts nothing — the start path is F11.3 and the scheduler F11.6.
func TestApproveQueuesRootTasksAndStartsNothing(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	assertSingleProposalCard(t, f)
	if answerErr := f.answerCard(t, "decide-approve", teamProposeToolName, teamDecisionQuestionID, decisionApprove, ""); answerErr != nil {
		t.Fatalf("approve: %v", answerErr)
	}
	assertActiveTeam(t, f, teamID)
	item := f.proposalItem(t, teamID)
	if item.Metadata.Decision != itemDecisionApproved {
		t.Fatalf("item decision = %s, want approved", item.Metadata.Decision)
	}
	if cards := f.pendingCards(t); len(cards) != 0 {
		t.Fatalf("pending cards after the decision = %d, want 0", len(cards))
	}
	assertSessionsCount(t, f, 1)
}

// TestEditedModelIsTheTaskModel pins OR-E13's edit half: the model the user
// edits on the card is the model the task's sealed spec carries, and the
// decided plan records it for the start path.
func TestEditedModelIsTheTaskModel(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	args := validTeamProposalArgs()
	args.Tasks[1].Profile = &teams.ProfileOverride{Vendor: teams.VendorClaude, Model: "orig-model"}
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, args)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	edit, marshalErr := json.Marshal(teams.EditSet{
		Version: teams.EditSetVersion,
		Roles:   []teams.RoleEdit{{Name: teamTestRole, Model: teamTestModelEdited}},
	})
	if marshalErr != nil {
		t.Fatalf("marshal the edit set: %v", marshalErr)
	}
	if answerErr := f.answerCard(t, "decide-edited", teamProposeToolName, teamDecisionQuestionID, decisionApproveEdited, string(edit)); answerErr != nil {
		t.Fatalf("approve with edits: %v", answerErr)
	}
	rec := assertActiveTeam(t, f, teamID)
	assertEditedSpecs(t, f, teamID)
	assertDecidedPlan(t, f, teamID, &rec)
	if item := f.proposalItem(t, teamID); item.Metadata.Decision != itemDecisionApprovedEdited {
		t.Fatalf("item decision = %s, want approved_edited", item.Metadata.Decision)
	}
}

// assertEditedSpecs asserts every task's sealed spec carries the edited
// model as its profile (OR-E13: the edited model is the task's model).
func assertEditedSpecs(t *testing.T, f *teamProposalFixture, teamID string) {
	t.Helper()
	for _, task := range []string{teamTestLeadTask, teamTestTaskA, teamTestTaskB} {
		raw, specErr := f.hp.vendorStore.OpenTaskSpec(t.Context(), teamID, durableTaskID(teamID, task))
		if specErr != nil {
			t.Fatalf("OpenTaskSpec(%s): %v", task, specErr)
		}
		var content teams.ProtectedTaskContent
		if unmarshalErr := json.Unmarshal(raw, &content); unmarshalErr != nil {
			t.Fatalf("unmarshal the sealed spec of %s: %v", task, unmarshalErr)
		}
		if content.Profile == nil || content.Profile.Model != teamTestModelEdited {
			t.Fatalf("task %s sealed profile = %+v, want model %s", task, content.Profile, teamTestModelEdited)
		}
	}
}

// assertDecidedPlan asserts the sealed TeamStarted payload records the
// approve_edited decision and the edited role model.
func assertDecidedPlan(t *testing.T, f *teamProposalFixture, teamID string, rec *vendorstore.TeamRecord) {
	t.Helper()
	payload, payloadErr := f.hp.vendorStore.OpenTeamEventPayload(t.Context(), teamID, rec.LastSeq)
	if payloadErr != nil {
		t.Fatalf("OpenTeamEventPayload: %v", payloadErr)
	}
	var decision teamDecisionPayload
	if unmarshalErr := json.Unmarshal(payload, &decision); unmarshalErr != nil {
		t.Fatalf("unmarshal the decision payload: %v", unmarshalErr)
	}
	if decision.Decision != decisionApproveEdited || decision.DecidedBy != decidedByUser {
		t.Fatalf("decision payload = %+v, want approve_edited by the user", decision)
	}
	role := findDecidedRole(decision.Roles, teamTestRole)
	if role == nil || role.Model != teamTestModelEdited {
		t.Fatalf("decided role = %+v, want model %s", role, teamTestModelEdited)
	}
}

func findDecidedRole(roles []decidedRole, name string) *decidedRole {
	for i := range roles {
		if roles[i].Name == name {
			return &roles[i]
		}
	}
	return nil
}

// TestSoloKeepsTheSessionSolo pins §5.3's solo decision: the team ends as
// cancelled with the solo decision recorded, and the session can propose
// again later.
func TestSoloKeepsTheSessionSolo(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if answerErr := f.answerCard(t, "decide-solo", teamProposeToolName, teamDecisionQuestionID, decisionSolo, "too small for a team"); answerErr != nil {
		t.Fatalf("solo: %v", answerErr)
	}
	rec, tasks := f.teamSnapshot(t, teamID)
	if rec.State != vendorstore.TeamStateCancelled {
		t.Fatalf("team state = %s, want cancelled", rec.State)
	}
	for i := range tasks {
		if tasks[i].State != vendorstore.TaskStateCancelled {
			t.Fatalf("task %s state = %s, want cancelled", tasks[i].TaskID, tasks[i].State)
		}
	}
	if item := f.proposalItem(t, teamID); item.Metadata.Decision != itemDecisionSolo {
		t.Fatalf("item decision = %s, want solo", item.Metadata.Decision)
	}
	if decision := teamEndedPayload(t, f, teamID, &rec); decision.Decision != decisionSolo || decision.Note != "too small for a team" {
		t.Fatalf("solo payload = %+v, want solo with the user note", decision)
	}
	nextID, proposeErr := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if proposeErr != nil {
		t.Fatalf("Propose again after solo: %v", proposeErr)
	}
	if nextID == teamID {
		t.Fatal("the soloed proposal was reused for the new proposal")
	}
	if cards := f.pendingCards(t); len(cards) != 1 {
		t.Fatalf("pending cards after the new proposal = %d, want 1", len(cards))
	}
}

// TestSessionPrincipalCannotDecideAProposal pins the decide-path principal
// rule: a session principal (the bridge token) can never approve, edit,
// choose solo or reject, whatever tool or argument it uses — the interaction
// answer path refuses it before any decision code runs.
func TestSessionPrincipalCannotDecideAProposal(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	cards := f.pendingCards(t)
	if len(cards) != 1 {
		t.Fatalf("pending cards = %d, want 1", len(cards))
	}
	_, answerErr := f.rt.AnswerInteraction(t.Context(), &AnswerInteractionCommand{
		CommandID: "decide-as-session", Principal: string(ClientKind("session")),
		Incarnation: f.rt.interactions.Incarnation(),
		RequestID:   cards[0].RequestID, Outcome: OutcomeAnswered,
		Answers: []InteractionAnswer{{QuestionID: teamDecisionQuestionID, OptionIDs: []string{decisionApprove}}},
	})
	if errcode.CodeOf(answerErr) != errcode.PermissionDenied {
		t.Fatalf("session principal answer error = %v, want permission denied", answerErr)
	}
	assertProposedTeam(t, f, teamID, 3)
	if cards := f.pendingCards(t); len(cards) != 1 {
		t.Fatalf("pending cards after the refused answer = %d, want 1", len(cards))
	}
}

// TestRejectSettlesTasksBeforeTeamEnded pins Q-84's order for the user's
// reject: every task cancels first, then TeamEnded appends with the rejected
// decision in the sealed payload.
func TestRejectSettlesTasksBeforeTeamEnded(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if answerErr := f.answerCard(t, "decide-reject", teamProposeToolName, teamDecisionQuestionID, decisionReject, ""); answerErr != nil {
		t.Fatalf("reject: %v", answerErr)
	}
	rec, tasks := f.teamSnapshot(t, teamID)
	if rec.State != vendorstore.TeamStateCancelled {
		t.Fatalf("team state = %s, want cancelled", rec.State)
	}
	assertSettleOrder(t, f, teamID, len(tasks))
	if decision := teamEndedPayload(t, f, teamID, &rec); decision.Decision != decisionRejectedItem || decision.DecidedBy != decidedByUser {
		t.Fatalf("reject payload = %+v, want rejected by the user", decision)
	}
	if item := f.proposalItem(t, teamID); item.Metadata.Decision != decisionRejectedItem {
		t.Fatalf("item decision = %s, want rejected", item.Metadata.Decision)
	}
}

// assertSettleOrder pins Q-84's order: one TaskEnded per task, every one of
// them before TeamEnded.
func assertSettleOrder(t *testing.T, f *teamProposalFixture, teamID string, wantEnded int) {
	t.Helper()
	events, err := f.hp.vendorStore.TeamEventsSince(t.Context(), teamID, 0, 100)
	if err != nil {
		t.Fatalf("TeamEventsSince: %v", err)
	}
	ended := make(map[string]int64, wantEnded)
	teamEnd := int64(0)
	for i := range events {
		if events[i].EventType == vendorstore.EventTaskEnded {
			ended[events[i].TaskID] = events[i].Sequence
		}
		if events[i].EventType == vendorstore.EventTeamEnded {
			teamEnd = events[i].Sequence
		}
	}
	if teamEnd == 0 || len(ended) != wantEnded {
		t.Fatalf("events = %+v, want one TaskEnded per task before TeamEnded", events)
	}
	for taskID, seq := range ended {
		if seq > teamEnd {
			t.Fatalf("task %s cancelled at %d after TeamEnded at %d", taskID, seq, teamEnd)
		}
	}
}

// TestDecisionAfterSupersedeOrSessionEndFailsClosed pins the fail-closed
// rules: an answer to a superseded card, a second answer to the same card and
// an answer after the session ended all fail typed and flip nothing.
func TestDecisionAfterSupersedeOrSessionEndFailsClosed(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	superseded, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	supersededCard := f.pendingCards(t)[0].RequestID
	replacement, proposeErr := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if proposeErr != nil {
		t.Fatalf("Propose the replacement: %v", proposeErr)
	}
	if rec, _ := f.teamSnapshot(t, superseded); rec.State != vendorstore.TeamStateCancelled {
		t.Fatalf("superseded team = %s, want cancelled", rec.State)
	}
	refuseAnswer(t, f, "decide-superseded", supersededCard, decisionApprove)
	// A second answer to the same card fails closed after the first applied.
	firstCard := f.pendingCards(t)[0].RequestID
	if answerErr := f.answerCard(t, "decide-first", teamProposeToolName, teamDecisionQuestionID, decisionApprove, ""); answerErr != nil {
		t.Fatalf("first answer: %v", answerErr)
	}
	assertActiveTeam(t, f, replacement)
	refuseAnswer(t, f, "decide-second", firstCard, decisionSolo)
	// An answer after the session ended fails closed: the pending proposal
	// is cancelled (Q-104) and its card dies with the session.
	last, proposeErr := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if proposeErr != nil {
		t.Fatalf("Propose the last proposal: %v", proposeErr)
	}
	lastCard := f.pendingCards(t)[0].RequestID
	if _, closeErr := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "team-close", SessionID: f.session.ID}); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	testutil.Poll(t, "the session-end cancel settles the last proposal", func() bool {
		rec, _, snapErr := f.hp.vendorStore.TeamSnapshot(t.Context(), last)
		return snapErr == nil && rec.State == vendorstore.TeamStateCancelled
	})
	refuseAnswer(t, f, "decide-after-end", lastCard, decisionApprove)
	if rec, _ := f.teamSnapshot(t, last); rec.State != vendorstore.TeamStateCancelled {
		t.Fatalf("last team after the refused answer = %s, want cancelled", rec.State)
	}
}

// refuseAnswer answers a card request that must not decide anything and
// asserts the typed refusal.
func refuseAnswer(t *testing.T, f *teamProposalFixture, commandID, requestID, option string) {
	t.Helper()
	_, err := f.rt.AnswerInteraction(t.Context(), &AnswerInteractionCommand{
		CommandID: commandID, Principal: testAppPrincipal,
		Incarnation: f.rt.interactions.Incarnation(),
		RequestID:   requestID, Outcome: OutcomeAnswered,
		Answers: []InteractionAnswer{{QuestionID: teamDecisionQuestionID, OptionIDs: []string{option}}},
	})
	if errcode.CodeOf(err) != errcode.InteractionNotFound && errcode.CodeOf(err) != errcode.InteractionAlreadyAnswered {
		t.Fatalf("refused answer error = %v, want not found or already answered", err)
	}
}

// TestInvalidEditLeavesTheProposalPending pins the pre-resolution rule: an
// invalid edit document is refused with a typed error and leaves the
// proposal pending with its card still answerable; a valid edit then applies.
func TestInvalidEditLeavesTheProposalPending(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	args := validTeamProposalArgs()
	args.Tasks[2].DependsOn = []string{teamTestTaskA}
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, args)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if cards := f.pendingCards(t); len(cards) != 1 {
		t.Fatalf("pending cards = %d, want 1", len(cards))
	}
	notJSON := `{"version":1,"roles":[{"name":` + "\x00"
	if answerErr := f.answerCard(t, "decide-bad-json", teamProposeToolName, teamDecisionQuestionID, decisionApproveEdited, notJSON); errcode.CodeOf(answerErr) != errcode.InvalidArgument {
		t.Fatalf("invalid document error = %v, want invalid argument", answerErr)
	}
	if rec, _ := f.teamSnapshot(t, teamID); rec.State != vendorstore.TeamStateProposed {
		t.Fatalf("team state after the invalid edit = %s, want proposed", rec.State)
	}
	if cards := f.pendingCards(t); len(cards) != 1 {
		t.Fatalf("pending cards after the invalid edit = %d, want 1", len(cards))
	}
	orphaning, marshalErr := json.Marshal(teams.EditSet{Version: teams.EditSetVersion, RemoveTasks: []string{"t-a"}})
	if marshalErr != nil {
		t.Fatalf("marshal the orphaning edit: %v", marshalErr)
	}
	if answerErr := f.answerCard(t, "decide-orphan", teamProposeToolName, teamDecisionQuestionID, decisionApproveEdited, string(orphaning)); errcode.CodeOf(answerErr) != errcode.InvalidArgument {
		t.Fatalf("orphaning edit error = %v, want invalid argument", answerErr)
	}
	if rec, _ := f.teamSnapshot(t, teamID); rec.State != vendorstore.TeamStateProposed {
		t.Fatalf("team state after the orphaning edit = %s, want proposed", rec.State)
	}
	valid, marshalErr := json.Marshal(teams.EditSet{Version: teams.EditSetVersion, Roles: []teams.RoleEdit{{Name: teamTestRole, Model: teamTestModelEdited}}})
	if marshalErr != nil {
		t.Fatalf("marshal the valid edit: %v", marshalErr)
	}
	if answerErr := f.answerCard(t, "decide-valid", teamProposeToolName, teamDecisionQuestionID, decisionApproveEdited, string(valid)); answerErr != nil {
		t.Fatalf("valid edit: %v", answerErr)
	}
	assertActiveTeam(t, f, teamID)
}

// TestAutoApproveOnlyForCatalogReadRolesUnderTheLimit pins Q-102 and the
// §5.3 conditions: with the workspace setting on, a proposal of at most
// three agents using only catalog read roles is decided by auto with no
// pending card; a bigger proposal or a write role waits for the user.
func TestAutoApproveOnlyForCatalogReadRolesUnderTheLimit(t *testing.T) {
	t.Parallel()
	extra := "[session]\nteams_auto_approve = true\n" +
		"[[roles]]\nname = \"writer\"\nvendor = \"codex\"\npermission = \"write\"\n"
	f := newTeamProposalFixture(t, extra)
	assertAutoApproved(t, f)
	assertWaitsForUser(t, f, bigProposalArgs())
	assertWaitsForUser(t, f, writeRoleProposalArgs())
}

// assertAutoApproved proposes the valid proposal and asserts the workspace
// setting decided it automatically with no pending card.
func assertAutoApproved(t *testing.T, f *teamProposalFixture) {
	t.Helper()
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	rec := assertActiveTeam(t, f, teamID)
	if item := f.proposalItem(t, teamID); item.Metadata.Decision != itemDecisionApproved {
		t.Fatalf("item decision = %s, want approved", item.Metadata.Decision)
	}
	assertDecidedByAuto(t, f, teamID, &rec)
	if cards := f.pendingCards(t); len(cards) != 0 {
		t.Fatalf("pending cards after the auto-approval = %d, want 0", len(cards))
	}
}

// assertDecidedByAuto opens the sealed TeamStarted payload and asserts the
// decision is an auto-approval (Q-102).
func assertDecidedByAuto(t *testing.T, f *teamProposalFixture, teamID string, rec *vendorstore.TeamRecord) {
	t.Helper()
	payload, payloadErr := f.hp.vendorStore.OpenTeamEventPayload(t.Context(), teamID, rec.LastSeq)
	if payloadErr != nil {
		t.Fatalf("OpenTeamEventPayload: %v", payloadErr)
	}
	var decision teamDecisionPayload
	if unmarshalErr := json.Unmarshal(payload, &decision); unmarshalErr != nil {
		t.Fatalf("unmarshal the decision payload: %v", unmarshalErr)
	}
	if decision.DecidedBy != decidedByAuto || decision.Decision != decisionApprove {
		t.Fatalf("decision payload = %+v, want approve decided by auto", decision)
	}
}

// assertWaitsForUser proposes one variant and asserts it stays pending with
// a card: anything beyond the §5.3 conditions waits for the user.
func assertWaitsForUser(t *testing.T, f *teamProposalFixture, args *ProposeArgs) {
	t.Helper()
	teamID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, args)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if rec, _ := f.teamSnapshot(t, teamID); rec.State != vendorstore.TeamStateProposed {
		t.Fatalf("proposal state = %s, want proposed", rec.State)
	}
	if cards := f.pendingCards(t); len(cards) != 1 {
		t.Fatalf("pending cards = %d, want 1", len(cards))
	}
}

// bigProposalArgs is the valid proposal with two more member tasks: five
// agents, past the Q-102 limit of three.
func bigProposalArgs() *ProposeArgs {
	args := validTeamProposalArgs()
	for _, id := range []string{teamTestTaskC, "t-d"} {
		args.Tasks = append(args.Tasks, teams.ProposedTask{
			ID: id, ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole,
			Goal: "Read the " + id, WriteScope: []string{"docs/" + id + ".md"}, Deliverable: teams.DeliverableReport,
		})
	}
	return args
}

// writeRoleProposalArgs moves the last member task onto the workspace's
// write role: under the agent limit, but not read-only.
func writeRoleProposalArgs() *ProposeArgs {
	args := validTeamProposalArgs()
	args.Tasks[2].Role = teamTestWriterRole
	args.Tasks[2].WriteScope = []string{"docs/parser.md"}
	return args
}

// TestAutoApproveIsOffByDefaultAndWideningNeedsConfirmation pins Q-111 and
// Q-64: [session].teams_auto_approve defaults to false, so every proposal
// gets a card; an out-of-band widening of the key waits for user
// confirmation and keeps the accepted value, and only the confirmed setting
// auto-approves.
func TestAutoApproveIsOffByDefaultAndWideningNeedsConfirmation(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	eff, _, err := f.rt.workspaces.domain.GetEffectiveConfig(t.Context(), f.wsID, nil)
	if err != nil {
		t.Fatalf("GetEffectiveConfig: %v", err)
	}
	if eff.Session.TeamsAutoApprove {
		t.Fatal("teams_auto_approve defaults to true; it must default to false")
	}
	assertWaitsForUser(t, f, validTeamProposalArgs())
	// The out-of-band widening edits fugue.toml behind the API: the change
	// waits for confirmation and the accepted value keeps deciding.
	view := widenOutOfBand(t, f)
	secondID, proposeErr := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if proposeErr != nil {
		t.Fatalf("Propose under the unconfirmed widening: %v", proposeErr)
	}
	if rec, _ := f.teamSnapshot(t, secondID); rec.State != vendorstore.TeamStateProposed {
		t.Fatalf("proposal state under the unconfirmed widening = %s, want proposed", rec.State)
	}
	if _, confirmErr := f.rt.workspaces.domain.ConfirmWorkspaceConfig(t.Context(), f.wsID, view.ConfigHash); confirmErr != nil {
		t.Fatalf("Confirm the widening: %v", confirmErr)
	}
	thirdID, proposeErr := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if proposeErr != nil {
		t.Fatalf("Propose under the confirmed setting: %v", proposeErr)
	}
	assertActiveTeam(t, f, thirdID)
	if cards := f.pendingCards(t); len(cards) != 0 {
		t.Fatalf("pending cards under the confirmed setting = %d, want 0", len(cards))
	}
}

// widenOutOfBand edits fugue.toml behind the API and asserts the change
// waits for confirmation of the widened key (Q-64).
func widenOutOfBand(t *testing.T, f *teamProposalFixture) *workspace.Workspace {
	t.Helper()
	ws, wsErr := f.rt.workspaces.domain.GetWorkspace(t.Context(), f.wsID)
	if wsErr != nil {
		t.Fatalf("GetWorkspace: %v", wsErr)
	}
	widened := teamWorkspaceTOML(t, "[session]\nteams_auto_approve = true\n")
	if writeErr := os.WriteFile(filepath.Join(ws.Dir, "fugue.toml"), widened, 0o600); writeErr != nil {
		t.Fatalf("write the widened fugue.toml: %v", writeErr)
	}
	view, viewErr := f.rt.workspaces.domain.GetWorkspace(t.Context(), f.wsID)
	if viewErr != nil {
		t.Fatalf("GetWorkspace after the widening: %v", viewErr)
	}
	if !view.PendingPolicyConfirmation || !slices.ContainsFunc(view.PolicyDiff, func(e workspace.PolicyDiffEntry) bool { return e.Key == "session.teams_auto_approve" }) {
		t.Fatalf("widening view = pending %v diffs %+v, want a pending confirmation of session.teams_auto_approve", view.PendingPolicyConfirmation, view.PolicyDiff)
	}
	return view
}
