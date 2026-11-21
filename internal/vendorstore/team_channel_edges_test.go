package vendorstore

// F11.10a channel edge coverage (review of PR #186): the digest citation
// lookup, the sibling-send counts with their sliding window on the injected
// clock, the paging ends, the TaskMessaged rejections, the informational
// mailbox rows, and the rebuild over retry chains and team-wide stalls.

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
)

// timedStore builds a store on a caller-owned fake clock, so a test can
// advance the window the sibling-send caps slide on.
func timedStore(tb testing.TB) (*Store, *clock.Fake) {
	tb.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC))
	db, err := storage.Open(tb.Context(), tb.TempDir()+"/test.db")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { requireNoError(tb, db.Close()) })
	if migErr := storage.Migrate(tb.Context(), db, clk); migErr != nil {
		tb.Fatal(migErr)
	}
	s, err := New(tb.Context(), db, cheapvault.Unlocked(tb), clk, nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { requireNoError(tb, s.Close()) })
	return s, clk
}

// seedTeamStarts creates an active team with a lead and one member, starting
// only the tasks it names, and returns the last sequence.
func seedTeamStarts(t *testing.T, s *Store, teamID string, started map[string]bool) int64 {
	t.Helper()
	spec := []byte(`{"goal":"edges"}`)
	rec, _, err := s.CreateTeam(t.Context(), &NewTeamInput{
		TeamID: teamID, WorkspaceID: testWorkspaceID, ProposedEventID: "p-" + teamID,
		ProposedPayload: []byte(`{"title":"edges","brief":"Edge coverage."}`),
		Tasks: []NewTaskInput{
			{TaskID: teamID + "-lead", HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "c-l-" + teamID, ProtectedSpec: spec},
			{TaskID: teamID + "-a", ParentTaskID: teamID + "-lead", HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableReport, CreatedEventID: "c-a-" + teamID, ProtectedSpec: spec},
		},
	})
	requireNoError(t, err)
	seq := appendSeq(t, s, teamID, rec.LastSeq, []AppendTeamEventInput{{EventID: "start-" + teamID, EventType: EventTeamStarted}})
	for _, id := range []string{teamID + "-lead", teamID + "-a"} {
		if !started[id] {
			continue
		}
		seq = appendSeq(t, s, teamID, seq, []AppendTeamEventInput{
			{EventID: "as-" + id, TaskID: id, EventType: EventTaskAssigned},
			{EventID: "st-" + id, TaskID: id, EventType: EventTaskStarted},
		})
	}
	return seq
}

// rebuildChannelLines replays a team's event log through the channel mapping
// and returns the rows the rebuild would write, in log order.
func rebuildChannelLines(t *testing.T, s *Store, team string) []TeamChannelMessage {
	t.Helper()
	events, err := s.TeamEventsSince(t.Context(), team, 0, 1000)
	requireNoError(t, err)
	var rebuilt []TeamChannelMessage
	channelSeq := int64(0)
	for i := range events {
		ev := &AppendTeamEventInput{
			TaskID: events[i].TaskID, EventType: events[i].EventType,
			Outcome: events[i].Outcome, FailureCode: events[i].FailureCode,
		}
		if events[i].HasPayload {
			raw, openErr := s.OpenTeamEventPayload(t.Context(), team, events[i].Sequence)
			requireNoError(t, openErr)
			ev.ProtectedPayload = raw
		}
		for _, line := range channelLinesForEvent(ev) {
			channelSeq++
			rebuilt = append(rebuilt, TeamChannelMessage{
				TeamID: team, ChannelSeq: channelSeq, Kind: line.kind, ThreadRoot: line.threadRoot,
				SenderKind: line.senderKind, SenderTaskID: line.senderTask, RecipientTaskID: line.recipient,
				SourceSeq: events[i].Sequence, Bytes: int64(len(ev.ProtectedPayload)),
				CreatedAt: events[i].CreatedAt,
			})
		}
	}
	return rebuilt
}

func TestChannelSourcesAndCounts(t *testing.T) {
	t.Parallel()
	s, clk := timedStore(t)
	ctx := t.Context()
	team := "chan-src"
	lead, a := team+"-lead", team+"-a"
	seq := seedTeamStarts(t, s, team, map[string]bool{lead: true, a: true})

	// One sibling send a -> lead (legal at the store level), then a stall
	// that is team-wide, so its source projects two lines.
	seq = appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "msg-1", TaskID: a, EventType: EventTaskMessaged, ProtectedPayload: []byte(`{"to_task_id":"` + lead + `","message":"diff is up"}`)},
		{EventID: "stall-1", TaskID: a, EventType: EventTaskStalled, ProtectedPayload: []byte(`{"task_id":"` + a + `","waiting_on":"vendor","silent_seconds":600,"team_wide":true}`)},
	})

	// The digest citation lookup returns every line of a source; the thread
	// line precedes the team-wide duplicate.
	bySource, err := s.TeamChannelLinesForSources(ctx, team, []int64{seq - 1, seq})
	requireNoError(t, err)
	if len(bySource) != 2 {
		t.Fatalf("sources = %d, want 2", len(bySource))
	}
	stallLines := bySource[seq]
	if len(stallLines) != 2 || stallLines[0].ThreadRoot != ThreadRootTaskID(a) || stallLines[1].ThreadRoot != "" {
		t.Fatalf("stall lines = %+v, want the thread line first and the general line second", stallLines)
	}
	if len(bySource[seq-1]) != 1 || bySource[seq-1][0].Kind != ChannelKindSend {
		t.Fatalf("send source = %+v, want one send line", bySource[seq-1])
	}
	empty, err := s.TeamChannelLinesForSources(ctx, team, nil)
	requireNoError(t, err)
	if len(empty) != 0 {
		t.Fatalf("empty sources = %+v, want none", empty)
	}
	if _, err := s.TeamChannelLinesForSources(ctx, team, []int64{999}); err != nil {
		t.Fatalf("unknown source read: %v", err)
	}

	assertSendCounts(t, s, clk, team, a)
}

// assertSendCounts checks the sliding window of the sibling-send counts on
// the injected clock: inside the window both counts see the event, past it
// neither does, and the required arguments fail typed.
func assertSendCounts(t *testing.T, s *Store, clk *clock.Fake, team, a string) {
	t.Helper()
	byAgent, total, err := s.CountTaskMessages(t.Context(), team, a, clk.Now().Add(-10*time.Minute).UnixMilli())
	requireNoError(t, err)
	if byAgent != 1 || total != 1 {
		t.Fatalf("counts = (%d, %d), want (1, 1)", byAgent, total)
	}
	clk.Advance(11 * time.Minute)
	byAgent, total, err = s.CountTaskMessages(t.Context(), team, a, clk.Now().Add(-10*time.Minute).UnixMilli())
	requireNoError(t, err)
	if byAgent != 0 || total != 0 {
		t.Fatalf("counts after the window = (%d, %d), want (0, 0)", byAgent, total)
	}
	if _, _, countErr := s.CountTaskMessages(t.Context(), "", a, 0); !errors.Is(countErr, ErrTeamInvalidInput) {
		t.Fatalf("empty team count err = %v, want ErrTeamInvalidInput", countErr)
	}
	if _, _, countErr := s.CountTaskMessages(t.Context(), team, "", 0); !errors.Is(countErr, ErrTeamInvalidInput) {
		t.Fatalf("empty sender count err = %v, want ErrTeamInvalidInput", countErr)
	}
}

func TestChannelPagingEdges(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	ctx := t.Context()
	team := "chan-edges"
	lead, a := team+"-lead", team+"-a"
	seedTeamStarts(t, s, team, map[string]bool{lead: true, a: true})

	// An empty thread and the window ends read cleanly.
	empty, err := s.TeamChannelMessages(ctx, team, TeamChannelPageQuery{ThreadRoots: []string{"nobody"}, Limit: 10})
	requireNoError(t, err)
	if len(empty) != 0 {
		t.Fatalf("empty thread page = %+v, want none", empty)
	}
	all := channelRows(t, s, team)
	top := all[0].ChannelSeq
	bottom := all[len(all)-1].ChannelSeq
	above, err := s.TeamChannelMessages(ctx, team, TeamChannelPageQuery{Before: bottom, Limit: 10})
	requireNoError(t, err)
	if len(above) != 0 {
		t.Fatalf("before the oldest line = %+v, want none", above)
	}
	beyond, err := s.TeamChannelMessages(ctx, team, TeamChannelPageQuery{Since: top, Limit: 10})
	requireNoError(t, err)
	if len(beyond) != 0 {
		t.Fatalf("since the newest line = %+v, want none", beyond)
	}
	general, err := s.TeamChannelMessages(ctx, team, TeamChannelPageQuery{GeneralOnly: true, Limit: 10})
	requireNoError(t, err)
	if len(general) == 0 {
		t.Fatal("general page empty, want the plan and decision lines")
	}
	// A limit at or below zero falls back to the 500 cap.
	capped, err := s.TeamChannelMessages(ctx, team, TeamChannelPageQuery{Limit: 0})
	requireNoError(t, err)
	if len(capped) != len(all) {
		t.Fatalf("default-limit page = %d lines, want %d", len(capped), len(all))
	}
	if _, err := s.TeamChannelMessages(ctx, "", TeamChannelPageQuery{Limit: 10}); !errors.Is(err, ErrTeamInvalidInput) {
		t.Fatalf("empty team read err = %v, want ErrTeamInvalidInput", err)
	}
}

func TestTaskMessagedValidationRejects(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	ctx := t.Context()
	team := "chan-reject"
	lead, a := team+"-lead", team+"-a"
	// The member stays queued: TaskMessaged is a no-op only from running and
	// asking (§6.7).
	seq := seedTeamStarts(t, s, team, map[string]bool{lead: true, a: false})

	if _, err := s.AppendTeamEvent(ctx, team, seq, &AppendTeamEventInput{
		EventID: "m-ghost-" + team, TaskID: team + "-ghost", EventType: EventTaskMessaged,
	}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("message to a missing task err = %v, want ErrTaskNotFound", err)
	}
	if _, err := s.AppendTeamEvent(ctx, team, seq, &AppendTeamEventInput{
		EventID: "m-queued-" + team, TaskID: a, EventType: EventTaskMessaged,
	}); !errors.Is(err, ErrTeamTransition) {
		t.Fatalf("message to a queued task err = %v, want ErrTeamTransition", err)
	}

	// Once the team is terminal, nothing further appends (Q-84); both tasks
	// must reach terminal states first.
	seq = appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "cancel-a-" + team, TaskID: a, EventType: EventTaskEnded, Outcome: outcomeCancelled},
		{EventID: "cancel-l-" + team, TaskID: lead, EventType: EventTaskEnded, Outcome: outcomeCancelled},
		{EventID: "end-" + team, EventType: EventTeamEnded, Outcome: outcomeCancelled},
	})
	if _, err := s.AppendTeamEvent(ctx, team, seq, &AppendTeamEventInput{
		EventID: "m-ended-" + team, TaskID: lead, EventType: EventTaskMessaged,
	}); !errors.Is(err, ErrTeamTerminal) {
		t.Fatalf("message on a terminal team err = %v, want ErrTeamTerminal", err)
	}

	// No rejected attempt projected a send line.
	rows := channelRows(t, s, team)
	for i := range rows {
		if rows[i].Kind == ChannelKindSend {
			t.Fatalf("a rejected TaskMessaged projected a line: %+v", rows[i])
		}
	}
}

func TestInformationalMailboxRidesNextDigest(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	ctx := t.Context()
	team := "chan-info"
	lead, a := team+"-lead", team+"-a"
	seq := seedTeamStarts(t, s, team, map[string]bool{lead: true, a: true})
	item := []byte(`{"task_id":"` + a + `","state":"message","summary":"filler"}`)

	// Only the informational mention is pending: the recipient is not listed,
	// so nothing schedules its delivery (§6.7, Q-177 A).
	seq = appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{
			EventID: "mention-" + team, TaskID: a, EventType: EventTaskMessaged,
			ProtectedPayload: []byte(`{"to_task_id":"` + lead + `","message":"filler"}`),
			Mailbox:          []MailboxEnqueueInput{{RecipientTaskID: lead, Kind: MailboxKindSend, Payload: item, Informational: true}},
		},
	})
	pending, err := s.PendingMailbox(ctx)
	requireNoError(t, err)
	if len(pending) != 0 {
		t.Fatalf("pending = %+v, want none while only informational rows wait", pending)
	}

	// A real row lists the recipient, and the claim delivers both together.
	appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{
			EventID: "real-" + team, TaskID: a, EventType: EventTaskMessaged,
			ProtectedPayload: []byte(`{"to_task_id":"` + lead + `","message":"real"}`),
			Mailbox:          []MailboxEnqueueInput{{RecipientTaskID: lead, Kind: MailboxKindSend, Payload: item}},
		},
	})
	pending, err = s.PendingMailbox(ctx)
	requireNoError(t, err)
	if len(pending) != 1 || pending[0].RecipientTaskID != lead {
		t.Fatalf("pending = %+v, want the lead's digest window", pending)
	}
	claimed, err := s.ClaimMailbox(ctx, team, lead)
	requireNoError(t, err)
	if len(claimed) != 2 {
		t.Fatalf("claim = %d row(s), want the informational mention to ride the batch", len(claimed))
	}
}

func TestChannelRebuildCoversRetryAndTeamWideStall(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	team := "chan-rebuild2"
	lead, a := team+"-lead", team+"-a"
	seq := seedTeamStarts(t, s, team, map[string]bool{lead: true, a: true})
	seq = appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "stall-tw-" + team, TaskID: a, EventType: EventTaskStalled, ProtectedPayload: []byte(`{"task_id":"` + a + `","waiting_on":"vendor","silent_seconds":600,"team_wide":true}`)},
		{EventID: "fail-" + team, TaskID: a, EventType: EventTaskEnded, Outcome: outcomeFailed, FailureCode: "overrun"},
	})
	res, err := s.RetryTask(t.Context(), team, a, &TaskRetryInput{ExpectedSeq: seq, SupersededEventID: "sup-" + team, CreatedEventID: "c-retry-" + team})
	requireNoError(t, err)
	seq = res.LastSeq
	appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "as-r-" + team, TaskID: a + "~r1", EventType: EventTaskAssigned},
		{EventID: "note-final-" + team, EventType: EventTeamNoted, ProtectedPayload: []byte(`{"author_task_id":"` + lead + `","note":"closing"}`)},
	})

	stored := channelRows(t, s, team)
	slices.Reverse(stored) // the read is newest first; the rebuild walks forward
	if diff := cmp.Diff(stored, rebuildChannelLines(t, s, team)); diff != "" {
		t.Fatalf("rebuild differs (-stored +rebuilt):\n%s", diff)
	}

	// The team-wide stall and the retry attempt share the rebuild's shape.
	stalls := 0
	inThread := 0
	for i := range stored {
		if stored[i].Kind == ChannelKindStall {
			stalls++
		}
		if stored[i].ThreadRoot == ThreadRootTaskID(a) {
			inThread++
		}
	}
	if stalls != 2 {
		t.Fatalf("team-wide stall lines = %d, want 2", stalls)
	}
	// a's shared thread: assigned, stall, failed, attempt-open, retry
	// assigned — five lines; TaskStarted makes none (§6.7).
	if inThread != 5 {
		t.Fatalf("thread lines = %d, want 5", inThread)
	}
}
