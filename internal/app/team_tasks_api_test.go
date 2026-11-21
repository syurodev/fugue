package app

// F11.10b evidence: the §6.7 user posts route by to_task_id, by their thread,
// and to the lead in the general channel, delivered as immediate origin=user
// turns with the delegator's mention riding its next digest (HA §5.3,
// orchestration §6.1–6.2); and the approval reference lines carry only a
// reference — the request and answer content never leaves the inbox
// (interaction §7, VR-05).

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	testPostMentionCmd  = "post-mention"
	testPostMentionText = "focus on tests"
	testToolShell       = "shell"
)

// sentTurn records one delivered turn with its origin.
type sentTurn struct {
	sessionID string
	text      string
	origin    vendor.InputOrigin
}

// originRecordingSessions wraps the fixture's fake task sessions and records
// every delivered turn, so a test can assert the routing and the origin.
type originRecordingSessions struct {
	fakeTaskSessions

	mu   sync.Mutex
	sent []sentTurn
}

func (s *originRecordingSessions) Send(ctx context.Context, sessionID string, input vendor.Input) error {
	s.mu.Lock()
	s.sent = append(s.sent, sentTurn{sessionID: sessionID, text: input.Text, origin: input.Origin})
	s.mu.Unlock()
	return nil
}

func (s *originRecordingSessions) SendAsync(ctx context.Context, sessionID string, input vendor.Input, onDone func(error)) error {
	if err := s.Send(ctx, sessionID, input); err != nil {
		return err
	}
	onDone(nil)
	return nil
}

func (s *originRecordingSessions) Steer(ctx context.Context, sessionID string, input vendor.Input) error {
	return s.Send(ctx, sessionID, input)
}

func (s *originRecordingSessions) turnsTo(sessionID string) []sentTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []sentTurn
	for _, t := range s.sent {
		if t.sessionID == sessionID {
			out = append(out, t)
		}
	}
	return out
}

// newOriginRecorder builds a tasks service sessions port that records every
// delivered turn with its origin.
func newOriginRecorder() *originRecordingSessions {
	return &originRecordingSessions{}
}

// tasksServiceWith builds a TasksService over the given sessions port.
func tasksServiceWith(t *testing.T, f *channelFixture, sessions taskSessions) *TasksService {
	t.Helper()
	return NewTasksService(f.hp.vendorStore, sessions, f.hp.Vault(), f.clk, ids.NewUUIDv7(f.clk, ids.NewDeterministic("tasks-api")))
}

// assertDelegatorMentionRidesNextDigest checks that the delegator's mention
// never schedules a wake but sits pending for the next digest.
func (f *channelFixture) assertDelegatorMentionRidesNextDigest(t *testing.T) {
	t.Helper()
	pending, err := f.hp.vendorStore.PendingMailbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if r.RecipientTaskID == f.mgrID {
			t.Fatalf("pending = %+v, want no scheduled wake for the delegator", pending)
		}
	}
	rows, err := f.hp.vendorStore.PendingMailboxRows(t.Context(), f.teamID, f.mgrID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("delegator pending rows = %d, want one mention per routed post", len(rows))
	}
}

func TestUserPostRoutesByMentionAndThread(t *testing.T) {
	t.Parallel()
	f := newChannelFixture(t)
	rec := newOriginRecorder()
	svc := tasksServiceWith(t, f, rec)

	// to_task_id routes to that agent: origin user, immediate.
	mentionSeq := f.postAndCheck(t, svc, rec, &PostChannelCommand{
		CommandID: testPostMentionCmd, TeamID: f.teamID, ToTaskID: f.memAID, Text: testPostMentionText,
	}, "chan-a")

	// The thread default routes to the task's agent.
	f.postAndCheck(t, svc, rec, &PostChannelCommand{
		CommandID: "post-thread", TeamID: f.teamID, ThreadTaskID: f.memAID, Text: "thread reply",
	}, "chan-a")

	// The general channel routes to the lead.
	f.postAndCheck(t, svc, rec, &PostChannelCommand{
		CommandID: "post-general", TeamID: f.teamID, Text: "how is it going",
	}, f.session.ID)

	// The delegator's mention rides the next digest without a separate wake.
	f.assertDelegatorMentionRidesNextDigest(t)

	// Posting to a task outside the team is rejected with the typed code.
	if _, err := svc.PostChannelMessage(t.Context(), &PostChannelCommand{
		CommandID: "post-outside", TeamID: f.teamID, ToTaskID: "chan-other-o", Text: "hi",
	}); errcode.CodeOf(err) != errcode.TasksTaskNotFound {
		t.Fatalf("post outside the team code = %v, want tasks.task_not_found", errcode.CodeOf(err))
	}

	// An ended team rejects the post with the typed code.
	f.endTeam(t)
	if _, err := svc.PostChannelMessage(t.Context(), &PostChannelCommand{
		CommandID: "post-ended", TeamID: f.teamID, ToTaskID: f.memAID, Text: "too late",
	}); errcode.CodeOf(err) != errcode.TasksTeamEnded {
		t.Fatalf("post to an ended team code = %v, want tasks.team_ended", errcode.CodeOf(err))
	}

	// A replayed command_id returns the cached line without posting again,
	// and a replayed command_id with other arguments fails typed.
	before := f.userPostLine(t)
	replayed, err := svc.PostChannelMessage(t.Context(), &PostChannelCommand{
		CommandID: testPostMentionCmd, TeamID: f.teamID, ToTaskID: f.memAID, Text: testPostMentionText,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ChannelSeq != mentionSeq || f.userPostLine(t).ChannelSeq != before.ChannelSeq {
		t.Fatalf("replay posted again: %d vs #%d (top line #%d)", replayed.ChannelSeq, mentionSeq, before.ChannelSeq)
	}
	if _, err := svc.PostChannelMessage(t.Context(), &PostChannelCommand{
		CommandID: testPostMentionCmd, TeamID: f.teamID, ToTaskID: f.memBID, Text: testPostMentionText,
	}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("replayed command_id with other arguments code = %v, want invalid_argument", errcode.CodeOf(err))
	}
}

// postAndCheck posts one command, asserts the routed delivery, and returns
// the new line's `#n`.
func (f *channelFixture) postAndCheck(t *testing.T, svc *TasksService, rec *originRecordingSessions, cmd *PostChannelCommand, wantSession string) int64 {
	t.Helper()
	res, err := svc.PostChannelMessage(t.Context(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	turns := rec.turnsTo(wantSession)
	if len(turns) == 0 || turns[len(turns)-1].origin != vendor.OriginUser || turns[len(turns)-1].text != cmd.Text {
		t.Fatalf("deliveries to %s = %+v, want one origin=user turn with %q", wantSession, turns, cmd.Text)
	}
	return res.ChannelSeq
}

// endTeam cancels every task and ends the team, so the channel is read-only.
func (f *channelFixture) endTeam(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	rec, tasks, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	seq := rec.LastSeq
	for i := range tasks {
		if teamTaskTerminal(tasks[i].State) {
			continue
		}
		next, cancelErr := f.hp.vendorStore.AppendTeamEvent(ctx, f.teamID, seq, &vendorstore.AppendTeamEventInput{
			EventID: "cancel-api-" + tasks[i].TaskID, TaskID: tasks[i].TaskID,
			EventType: vendorstore.EventTaskEnded, Outcome: "cancelled",
		})
		if cancelErr != nil {
			t.Fatalf("cancel %s: %v", tasks[i].TaskID, cancelErr)
		}
		seq = next
	}
	if _, err := f.hp.vendorStore.AppendTeamEvent(ctx, f.teamID, seq, &vendorstore.AppendTeamEventInput{
		EventID: "end-api", EventType: vendorstore.EventTeamEnded, Outcome: "cancelled",
	}); err != nil {
		t.Fatal(err)
	}
}

// userPostLine returns the newest user post line of the fixture's team.
func (f *channelFixture) userPostLine(t *testing.T) vendorstore.TeamChannelMessage {
	t.Helper()
	rows, err := f.hp.vendorStore.TeamChannelMessages(t.Context(), f.teamID, vendorstore.TeamChannelPageQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for i := range rows {
		if rows[i].Kind == vendorstore.ChannelKindSend && rows[i].SenderKind == vendorstore.ChannelSenderUser {
			return rows[i]
		}
	}
	t.Fatal("no user post line in the channel")
	return vendorstore.TeamChannelMessage{}
}

// approvalLines returns the team's approval reference lines in #n order.
func (f *channelFixture) approvalLines(t *testing.T) []vendorstore.TeamChannelMessage {
	t.Helper()
	rows, err := f.hp.vendorStore.TeamChannelMessages(t.Context(), f.teamID, vendorstore.TeamChannelPageQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	var out []vendorstore.TeamChannelMessage
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Kind == vendorstore.ChannelKindApproval {
			out = append(out, rows[i])
		}
	}
	return out
}

func TestApprovalLineCarriesOnlyAReference(t *testing.T) {
	t.Parallel()
	x := newApprovalLineFixture(t)

	x.assertRequestedReference(t)
	x.assertResolvedReference(t)
}

// approvalLineFixture holds the approval-line test state: the team fixture,
// the raised approval ticket, and the topic subscription.
type approvalLineFixture struct {
	f      *channelFixture
	sub    *Subscription
	ticket *interaction.Ticket
}

func newApprovalLineFixture(t *testing.T) *approvalLineFixture {
	t.Helper()
	f := newChannelFixture(t)
	// The fixture created the team directly through the store, so its channel
	// topic needs the registration a real decide path would have done.
	f.rt.RegisterTeamChannel(f.teamID)

	// Subscribe before the approval: the topic must not carry content either.
	sub, err := f.rt.hub.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: "team:" + f.teamID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	// The member's session raises a native tool approval whose summary is
	// content that must never leave the inbox (interaction §7): the channel
	// records the reference only.
	opID, opErr := interaction.NativeOperationKey("inv-1", "att-1", 1)
	if opErr != nil {
		t.Fatal(opErr)
	}
	ticket, err := f.rt.interactions.Ensure(&interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Summary:     "SECRET-SUMMARY run the tool rm -rf",
		Risk:        testRiskWrite,
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
		Binding: interaction.Binding{
			SessionID: f.aBinding.SessionID, ToolName: testToolShell,
			InvocationID: "inv-1", AttemptID: "att-1", CheckGeneration: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &approvalLineFixture{f: f, sub: sub, ticket: ticket}
}

// assertRequestedReference checks the requested reference line, its event
// payload and its topic event.
func (x *approvalLineFixture) assertRequestedReference(t *testing.T) {
	t.Helper()
	testutil.Poll(t, "the approval reference line", func() bool {
		return len(x.f.approvalLines(t)) == 1
	})
	line := x.f.approvalLines(t)[0]
	raw, err := x.f.hp.vendorStore.OpenTeamEventPayload(t.Context(), x.f.teamID, line.SourceSeq)
	if err != nil {
		t.Fatal(err)
	}
	var ref taskApprovalRefPayload
	if jsonErr := json.Unmarshal(raw, &ref); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if ref.RequestID != x.ticket.RequestID || ref.Tool != testToolShell || ref.Outcome != "" {
		t.Fatalf("approval reference = %+v, want the request and the tool only", ref)
	}
	assertNoApprovalContent(t, raw)
	requested := readChannelEvent(t, x.sub, line.ChannelSeq)
	assertNoApprovalContent(t, []byte(requested.Content))
	if !strings.Contains(requested.Content, x.ticket.RequestID) || !strings.Contains(requested.Content, testToolShell) {
		t.Fatalf("requested topic event = %+v, want the request and the tool in the reference", requested)
	}
}

// assertResolvedReference answers the approval and checks the resolved
// reference line, event and topic event.
func (x *approvalLineFixture) assertResolvedReference(t *testing.T) {
	t.Helper()
	if _, err := x.f.rt.AnswerInteraction(t.Context(), &AnswerInteractionCommand{
		CommandID: "approve-1", Principal: testAppPrincipal,
		Incarnation: x.f.rt.interactions.Incarnation(),
		RequestID:   x.ticket.RequestID, Outcome: OutcomeDenied,
	}); err != nil {
		t.Fatal(err)
	}
	testutil.Poll(t, "the resolved approval line", func() bool {
		return len(x.f.approvalLines(t)) == 2
	})
	resolved := x.f.approvalLines(t)[1]
	rawResolved, err := x.f.hp.vendorStore.OpenTeamEventPayload(t.Context(), x.f.teamID, resolved.SourceSeq)
	if err != nil {
		t.Fatal(err)
	}
	var resolvedRef taskApprovalRefPayload
	if jsonErr := json.Unmarshal(rawResolved, &resolvedRef); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if resolvedRef.RequestID != x.ticket.RequestID || resolvedRef.Outcome != string(interaction.OutcomeDenied) {
		t.Fatalf("resolved reference = %+v, want the request and the denied outcome", resolvedRef)
	}
	assertNoApprovalContent(t, rawResolved)

	resolvedEvent := readChannelEvent(t, x.sub, resolved.ChannelSeq)
	assertNoApprovalContent(t, []byte(resolvedEvent.Content))
	if !strings.Contains(resolvedEvent.Content, x.ticket.RequestID) || !strings.Contains(resolvedEvent.Content, string(interaction.OutcomeDenied)) {
		t.Fatalf("resolved topic event = %+v, want the reference and the denied outcome", resolvedEvent)
	}
}

// readChannelEvent waits for the topic event of one line and returns its
// wire shape.
func readChannelEvent(t *testing.T, sub *Subscription, seq int64) channelLineWire {
	t.Helper()
	wantSeq := mustChannelEventSeq(t, seq)
	for {
		ev, ok := sub.Next(t.Context())
		if !ok {
			t.Fatal("the topic stream ended before the expected event")
		}
		if ev.Type != channelEventName || ev.Seq != wantSeq {
			continue
		}
		var wire channelLineWire
		if err := json.Unmarshal(ev.Payload, &wire); err != nil {
			t.Fatal(err)
		}
		return wire
	}
}

// assertNoApprovalContent fails when the payload carries approval content
// instead of the reference (interaction §7, VR-05).
func assertNoApprovalContent(t *testing.T, payload []byte) {
	t.Helper()
	for _, forbidden := range []string{"SECRET-SUMMARY", "SECRET-DETAIL", "rm -rf"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("the approval event leaked %q", forbidden)
		}
	}
}

func TestApprovalLineIgnoresUnrelayedQuestions(t *testing.T) {
	t.Parallel()
	f := newChannelFixture(t)
	opID, err := interaction.NativeOperationKey(f.teamID, "card", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.rt.interactions.Ensure(&interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindQuestion,
		Summary:     "team card",
		Questions:   []interaction.Question{{ID: "q", Question: "proceed?", Header: "card"}},
		Binding:     interaction.Binding{SessionID: f.session.ID, InvocationID: f.teamID, AttemptID: "card", CheckGeneration: 1, ToolName: teamProposeToolName},
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.approvalLines(t); len(got) != 0 {
		t.Fatalf("unrelayed question emitted approval lines: %+v", got)
	}
	if _, seqErr := channelEventSeq(-1); seqErr == nil {
		t.Fatal("channelEventSeq(-1) succeeded, want typed error")
	}
}

func TestChannelContentExtractsAllKinds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind string
		raw  string
		want string
	}{
		{vendorstore.ChannelKindNote, `{"note":"keep tests green"}`, "keep tests green"},
		{vendorstore.ChannelKindQuestion, `{"to":"lead","question":"which port?"}`, "which port?"},
		{vendorstore.ChannelKindAnswer, `{"answer":"8080","answered_by":"lead"}`, "8080"},
		{vendorstore.ChannelKindResult, `{"status":"complete","summary":"done"}`, "done"},
		{vendorstore.ChannelKindStall, `{"task_id":"t1","waiting_on":"user","silent_seconds":30,"team_wide":true}`, "waiting_on=user"},
		{vendorstore.ChannelKindDecision, `{"role_edits":[{"name":"r","vendor":"claude","permission":"read"}]}`, "amended"},
		{vendorstore.ChannelKindSend, `{"to_task_id":"t1","message":"ping"}`, "ping"},
		{"unknown", "raw-fallback", "raw-fallback"},
	} {
		row := &vendorstore.TeamChannelMessage{Kind: tc.kind, ThreadRoot: "t1"}
		parts := channelPayloadParts(tc.kind, []byte(tc.raw), "lbl")
		if len(parts) == 0 {
			t.Fatalf("kind %s parts empty", tc.kind)
		}
		if got := channelContentText(row, nil, []byte(tc.raw)); !strings.Contains(got, tc.want) {
			t.Fatalf("kind %s content = %q, want substring %q", tc.kind, got, tc.want)
		}
		if bad := channelPayloadParts(tc.kind, []byte("{bad"), "lbl"); len(bad) == 0 {
			t.Fatalf("kind %s malformed fallback empty", tc.kind)
		}
	}
}
