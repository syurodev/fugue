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

func TestUserPostRoutesByMentionAndThread(t *testing.T) {
	t.Parallel()
	f := newChannelFixture(t)
	ctx := t.Context()
	sent := &originRecordingSessions{}
	svc := NewTasksService(f.hp.vendorStore, sent, f.hp.Vault(), f.clk, ids.NewUUIDv7(f.clk, ids.NewDeterministic("tasks-api")))

	// to_task_id routes to that agent: origin user, immediate.
	res, err := svc.PostChannelMessage(ctx, PostChannelCommand{
		CommandID: "post-mention", TeamID: f.teamID, ToTaskID: f.memAID, Text: "focus on tests",
	})
	if err != nil {
		t.Fatal(err)
	}
	turns := sent.turnsTo("chan-a")
	if len(turns) != 1 || turns[0].origin != vendor.OriginUser || turns[0].text != "focus on tests" {
		t.Fatalf("deliveries to a = %+v, want one origin=user turn with the text", turns)
	}
	line := f.userPostLine(t)
	if line.ChannelSeq != res.ChannelSeq || line.RecipientTaskID != f.memAID || line.SenderKind != vendorstore.ChannelSenderUser {
		t.Fatalf("posted line = %+v, want the user's send to a", line)
	}

	// The thread default routes to the task's agent.
	if _, err := svc.PostChannelMessage(ctx, PostChannelCommand{
		CommandID: "post-thread", TeamID: f.teamID, ThreadTaskID: f.memAID, Text: "thread reply",
	}); err != nil {
		t.Fatal(err)
	}
	if got := sent.turnsTo("chan-a"); len(got) != 2 {
		t.Fatalf("deliveries to a after the thread post = %d, want 2", len(got))
	}

	// The general channel routes to the lead.
	if _, err := svc.PostChannelMessage(ctx, PostChannelCommand{
		CommandID: "post-general", TeamID: f.teamID, Text: "how is it going",
	}); err != nil {
		t.Fatal(err)
	}
	if got := sent.turnsTo(f.session.ID); len(got) != 1 || got[0].origin != vendor.OriginUser {
		t.Fatalf("deliveries to the lead = %+v, want one origin=user turn", got)
	}

	// The delegator's mention rides the next digest without a separate wake.
	pending, err := f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if r.RecipientTaskID == f.mgrID {
			t.Fatalf("pending = %+v, want no scheduled wake for the delegator", pending)
		}
	}
	rows, err := f.hp.vendorStore.PendingMailboxRows(ctx, f.teamID, f.mgrID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("delegator pending rows = %d, want one mention per routed post", len(rows))
	}

	// Posting to a task outside the team is rejected with the typed code.
	if _, err := svc.PostChannelMessage(ctx, PostChannelCommand{
		CommandID: "post-outside", TeamID: f.teamID, ToTaskID: "chan-other-o", Text: "hi",
	}); errcode.CodeOf(err) != errcode.TasksTaskNotFound {
		t.Fatalf("post outside the team code = %v, want tasks.task_not_found", errcode.CodeOf(err))
	}

	// An ended team rejects the post with the typed code.
	f.endTeam(t)
	if _, err := svc.PostChannelMessage(ctx, PostChannelCommand{
		CommandID: "post-ended", TeamID: f.teamID, ToTaskID: f.memAID, Text: "too late",
	}); errcode.CodeOf(err) != errcode.TasksTeamEnded {
		t.Fatalf("post to an ended team code = %v, want tasks.team_ended", errcode.CodeOf(err))
	}

	// A replayed command_id returns the cached line without posting again,
	// and a replayed command_id with other arguments fails typed.
	before := f.userPostLine(t)
	replayed, err := svc.PostChannelMessage(ctx, PostChannelCommand{
		CommandID: "post-mention", TeamID: f.teamID, ToTaskID: f.memAID, Text: "focus on tests",
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ChannelSeq != res.ChannelSeq || f.userPostLine(t).ChannelSeq != before.ChannelSeq {
		t.Fatalf("replay posted again: %d then %d (top line #%d)", res.ChannelSeq, replayed.ChannelSeq, before.ChannelSeq)
	}
	if _, err := svc.PostChannelMessage(ctx, PostChannelCommand{
		CommandID: "post-mention", TeamID: f.teamID, ToTaskID: f.memBID, Text: "focus on tests",
	}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("replayed command_id with other arguments code = %v, want invalid_argument", errcode.CodeOf(err))
	}
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
	f := newChannelFixture(t)
	ctx := t.Context()

	// The fixture created the team directly through the store, so its channel
	// topic needs the registration a real decide path would have done.
	f.rt.RegisterTeamChannel(f.teamID)

	// Subscribe before the approval: the topic must not carry content either.
	sub, err := f.rt.hub.Subscribe(ctx, SubscribeParams{Principal: testAppPrincipal, Topic: "team:" + f.teamID})
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
		Risk:        "write",
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
		Binding: interaction.Binding{
			SessionID: f.aBinding.SessionID, ToolName: "shell",
			InvocationID: "inv-1", AttemptID: "att-1", CheckGeneration: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The lifecycle hook records the reference line off the owner's
	// goroutine; wait for it.
	testutil.Poll(t, "the approval reference line", func() bool {
		return len(f.approvalLines(t)) == 1
	})
	line := f.approvalLines(t)[0]
	raw, err := f.hp.vendorStore.OpenTeamEventPayload(ctx, f.teamID, line.SourceSeq)
	if err != nil {
		t.Fatal(err)
	}
	var ref taskApprovalRefPayload
	if jsonErr := json.Unmarshal(raw, &ref); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if ref.RequestID != ticket.RequestID || ref.Tool != "shell" || ref.Outcome != "" {
		t.Fatalf("approval reference = %+v, want the request and the tool only", ref)
	}
	assertNoApprovalContent(t, raw)

	// The topic carries the requested reference event, never the content.
	requested := readChannelEvent(t, sub, line.ChannelSeq)
	assertNoApprovalContent(t, []byte(requested.Content))
	if !strings.Contains(requested.Content, ticket.RequestID) || !strings.Contains(requested.Content, "shell") {
		t.Fatalf("requested topic event = %+v, want the request and the tool in the reference", requested)
	}

	// The resolution records the outcome only, never the answer text.
	if _, err := f.rt.AnswerInteraction(ctx, &AnswerInteractionCommand{
		CommandID: "approve-1", Principal: testAppPrincipal,
		Incarnation: f.rt.interactions.Incarnation(),
		RequestID:   ticket.RequestID, Outcome: OutcomeDenied,
	}); err != nil {
		t.Fatal(err)
	}
	testutil.Poll(t, "the resolved approval line", func() bool {
		return len(f.approvalLines(t)) == 2
	})
	resolved := f.approvalLines(t)[1]
	rawResolved, err := f.hp.vendorStore.OpenTeamEventPayload(ctx, f.teamID, resolved.SourceSeq)
	if err != nil {
		t.Fatal(err)
	}
	var resolvedRef taskApprovalRefPayload
	if jsonErr := json.Unmarshal(rawResolved, &resolvedRef); jsonErr != nil {
		t.Fatal(jsonErr)
	}
	if resolvedRef.RequestID != ticket.RequestID || resolvedRef.Outcome != string(interaction.OutcomeDenied) {
		t.Fatalf("resolved reference = %+v, want the request and the denied outcome", resolvedRef)
	}
	assertNoApprovalContent(t, rawResolved)

	// The topic carries the resolved reference event, never the content.
	resolvedEvent := readChannelEvent(t, sub, resolved.ChannelSeq)
	assertNoApprovalContent(t, []byte(resolvedEvent.Content))
	if !strings.Contains(resolvedEvent.Content, ticket.RequestID) || !strings.Contains(resolvedEvent.Content, string(interaction.OutcomeDenied)) {
		t.Fatalf("resolved topic event = %+v, want the reference and the denied outcome", resolvedEvent)
	}
}

// readChannelEvent waits for the topic event of one line and returns its
// wire shape.
func readChannelEvent(t *testing.T, sub *Subscription, seq int64) channelLineWire {
	t.Helper()
	for {
		ev, ok := sub.Next(t.Context())
		if !ok {
			t.Fatal("the topic stream ended before the expected event")
		}
		if ev.Type != "channel.appended" || ev.Seq != uint64(seq) {
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
	for _, secret := range []string{"SECRET-SUMMARY", "SECRET-DETAIL", "rm -rf"} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("the approval event leaked %q", secret)
		}
	}
}
