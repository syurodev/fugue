package vendorstore

// F11.10a channel projection evidence (§6.7, Q-176 A, Q-178 A): every
// §6.7-listed team event projects into numbered threads with metadata only;
// retry chains share one thread; replaying the event log through the mapping
// reproduces the rows; a stall posts into the task thread and, when
// team-wide, into the general channel too.

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// seedChannelTeam creates one active team with a lead, a manager, and two
// members (one under the manager, one under the lead) and returns the last
// sequence, appending TaskAssigned and TaskStarted for every task it names.
func seedChannelTeam(t *testing.T, s *Store, teamID string, assign map[string]bool) int64 {
	t.Helper()
	ctx := t.Context()
	rec, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: teamID, WorkspaceID: testWorkspaceID, ProposedEventID: "p-" + teamID,
		ProposedPayload: []byte(`{"title":"channel plan","brief":"Build the widget."}`),
		Tasks: []NewTaskInput{
			{TaskID: teamID + "-lead", HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "c-lead-" + teamID, ProtectedSpec: []byte(`{"goal":"lead"}`)},
			{TaskID: teamID + "-mgr", ParentTaskID: teamID + "-lead", HierarchyRole: RoleTeamManager, CatalogRole: testRoleCoder, Deliverable: DeliverableReport, CreatedEventID: "c-mgr-" + teamID, ProtectedSpec: []byte(`{"goal":"manage"}`)},
			{TaskID: teamID + "-a", ParentTaskID: teamID + "-mgr", HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableReport, CreatedEventID: "c-a-" + teamID, ProtectedSpec: []byte(`{"goal":"work a"}`)},
			{TaskID: teamID + "-b", ParentTaskID: teamID + "-mgr", HierarchyRole: RoleMember, CatalogRole: testRoleCoder, Deliverable: DeliverableReport, CreatedEventID: "c-b-" + teamID, ProtectedSpec: []byte(`{"goal":"work b"}`)},
		},
	})
	requireNoError(t, err)
	seq := appendSeq(t, s, teamID, rec.LastSeq, []AppendTeamEventInput{
		{EventID: "start-" + teamID, EventType: EventTeamStarted},
	})
	for _, id := range []string{teamID + "-lead", teamID + "-mgr", teamID + "-a", teamID + "-b"} {
		if !assign[id] {
			continue
		}
		seq = appendSeq(t, s, teamID, seq, []AppendTeamEventInput{
			{EventID: "as-" + id, TaskID: id, EventType: EventTaskAssigned},
			{EventID: "st-" + id, TaskID: id, EventType: EventTaskStarted},
		})
	}
	return seq
}

// channelRows reads the whole channel and asserts the per-team numbering is
// contiguous (the read is newest first).
func channelRows(t *testing.T, s *Store, teamID string) []TeamChannelMessage {
	t.Helper()
	rows, err := s.TeamChannelMessages(t.Context(), teamID, TeamChannelPageQuery{Limit: 500})
	requireNoError(t, err)
	for i := 1; i < len(rows); i++ {
		if rows[i].ChannelSeq != rows[i-1].ChannelSeq-1 {
			t.Fatalf("channel numbering is not contiguous: %d then %d", rows[i-1].ChannelSeq, rows[i].ChannelSeq)
		}
	}
	return rows
}

// findChannelLine returns the chronologically first line of a kind: the read
// is newest first, so the search walks from the end.
// assertGeneralLines checks the general-channel lines: the plan opens the
// channel at #1, the decision is a user line, and the note names its author.
func assertGeneralLines(t *testing.T, rows []TeamChannelMessage, mgr string) {
	t.Helper()
	plan := rows[len(rows)-1] // the read is newest first
	if plan.Kind != ChannelKindPlan || plan.ChannelSeq != 1 {
		t.Fatalf("first line = %+v, want plan at #1", plan)
	}
	if plan.SourceSeq != 1 || plan.SenderKind != ChannelSenderAgent {
		t.Fatalf("plan line metadata = %+v", plan)
	}
	decision := findChannelLine(rows, ChannelKindDecision)
	if decision == nil || decision.SenderKind != ChannelSenderUser || decision.ThreadRoot != "" {
		t.Fatalf("decision line = %+v, want a general-channel user line", decision)
	}
	noteLine := findChannelLine(rows, ChannelKindNote)
	if noteLine == nil || noteLine.SenderTaskID != mgr || noteLine.ThreadRoot != "" {
		t.Fatalf("note line = %+v, want the manager's note in the general channel", noteLine)
	}
}

// assertThreadLines checks the per-thread lines: the lead's fugue
// assignment, a's question and the manager's answer in the asker's thread,
// and b's report in its own.
func assertThreadLines(t *testing.T, rows []TeamChannelMessage, lead, mgr, a, b string) {
	t.Helper()
	assertTaskLine(t, rows, []string{lead, mgr, a, b})
	assertThreadLine(t, rows, ChannelKindQuestion, a, a)
	assertThreadLine(t, rows, ChannelKindAnswer, mgr, a)
	assertThreadLine(t, rows, ChannelKindResult, b, b)
}

// assertTaskLine checks that a fugue assignment line exists in the thread of
// one of the named tasks.
func assertTaskLine(t *testing.T, rows []TeamChannelMessage, tasks []string) {
	t.Helper()
	taskLine := findChannelLine(rows, ChannelKindTask)
	if taskLine == nil || taskLine.SenderKind != ChannelSenderFugue {
		t.Fatalf("task line = %+v, want a fugue line", taskLine)
	}
	for _, id := range tasks {
		if taskLine.ThreadRoot == ThreadRootTaskID(id) {
			return
		}
	}
	t.Fatalf("task line thread = %q, want an assigned task's thread", taskLine.ThreadRoot)
}

// assertThreadLine checks one agent-authored line: its sender, and the
// thread it belongs to.
func assertThreadLine(t *testing.T, rows []TeamChannelMessage, kind, sender, threadTask string) {
	t.Helper()
	line := findChannelLine(rows, kind)
	if line == nil || line.SenderTaskID != sender || line.ThreadRoot != ThreadRootTaskID(threadTask) {
		t.Fatalf("%s line = %+v, want %s in task %s's thread", kind, line, sender, threadTask)
	}
}

// assertPayloadsStaySealed opens every payload a byte count points at: the
// row holds metadata only, the content stays in the source event.
func assertPayloadsStaySealed(t *testing.T, s *Store, team string, rows []TeamChannelMessage) {
	t.Helper()
	for i := range rows {
		if rows[i].Bytes > 0 {
			if _, err := s.OpenTeamEventPayload(t.Context(), team, rows[i].SourceSeq); err != nil {
				t.Fatalf("line #%d payload does not open: %v", rows[i].ChannelSeq, err)
			}
		}
	}
}

func findChannelLine(rows []TeamChannelMessage, kind string) *TeamChannelMessage {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Kind == kind {
			return &rows[i]
		}
	}
	return nil
}

func TestChannelProjectsTeamEventsIntoNumberedThreads(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	team := "chan-proj"
	lead, mgr, a, b := team+"-lead", team+"-mgr", team+"-a", team+"-b"
	seq := seedChannelTeam(t, s, team, map[string]bool{lead: true, mgr: true, a: true, b: true})

	note := []byte(`{"author_task_id":"` + mgr + `","note":"use the v2 API"}`)
	question := []byte(`{"question":"which branch?","to":"parent"}`)
	seq = appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "note-1", EventType: EventTeamNoted, ProtectedPayload: note},
		{EventID: "q-1", TaskID: a, EventType: EventTaskQuestionAsked, ProtectedPayload: question},
		{EventID: "ans-1", TaskID: a, EventType: EventTaskQuestionAnswered, ProtectedPayload: []byte(`{"answer":"use main","answered_by":"` + mgr + `"}`)},
		{EventID: "rep-1", TaskID: b, EventType: EventTaskReported, ProtectedPayload: []byte(`{"status":"succeeded","summary":"part done"}`)},
		{EventID: "msg-1", TaskID: a, EventType: EventTaskMessaged, ProtectedPayload: []byte(`{"to_task_id":"` + b + `","message":"diff is up"}`)},
	})

	rows := channelRows(t, s, team)
	assertGeneralLines(t, rows, mgr)
	assertThreadLines(t, rows, lead, mgr, a, b)
	sendLine := findChannelLine(rows, ChannelKindSend)
	if sendLine == nil || sendLine.SenderTaskID != a || sendLine.RecipientTaskID != b || sendLine.ThreadRoot != ThreadRootTaskID(a) {
		t.Fatalf("send line = %+v, want a to b in a's thread", sendLine)
	}

	// The content is never duplicated: the row holds metadata and a byte
	// count, the payload stays sealed in the source event.
	assertPayloadsStaySealed(t, s, team, rows)
	if sendLine.Bytes != int64(len(`{"to_task_id":"`+b+`","message":"diff is up"}`)) {
		t.Fatalf("send line bytes = %d, want the sealed payload length", sendLine.Bytes)
	}

	assertChannelEndLines(t, s, team, seq, a, b)
}

// assertChannelEndLines checks the §6.7 end-of-task lines: a failed task end
// projects a failed line into its thread; a cancelled one makes none.
func assertChannelEndLines(t *testing.T, s *Store, team string, seq int64, a, b string) {
	t.Helper()
	appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "fail-1", TaskID: b, EventType: EventTaskEnded, Outcome: outcomeFailed, FailureCode: "budget"},
		{EventID: "cancel-1", TaskID: a, EventType: EventTaskEnded, Outcome: outcomeCancelled},
	})
	rows := channelRows(t, s, team)
	failed := findChannelLine(rows, ChannelKindFailed)
	if failed == nil || failed.ThreadRoot != ThreadRootTaskID(b) {
		t.Fatalf("failed line = %+v, want b's failure in its thread", failed)
	}
	for i := range rows {
		if rows[i].Kind == ChannelKindTask && rows[i].SourceSeq > failed.SourceSeq {
			t.Fatalf("a cancelled task end projected a line: %+v", rows[i])
		}
	}
}

func TestChannelRetryAttemptsShareOneThread(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	team := "chan-retry"
	lead, a := team+"-lead", team+"-a"
	seq := seedChannelTeam(t, s, team, map[string]bool{lead: true, a: true})
	seq = appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "fail-" + team, TaskID: a, EventType: EventTaskEnded, Outcome: outcomeFailed, FailureCode: "checks"},
	})
	res, err := s.RetryTask(t.Context(), team, a, &TaskRetryInput{ExpectedSeq: seq, SupersededEventID: "sup-" + team, CreatedEventID: "c-retry-" + team})
	if err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	seq = res.LastSeq
	appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "as-retry-" + team, TaskID: a + "~r1", EventType: EventTaskAssigned},
		{EventID: "st-retry-" + team, TaskID: a + "~r1", EventType: EventTaskStarted},
		{EventID: "q-retry-" + team, TaskID: a + "~r1", EventType: EventTaskQuestionAsked, ProtectedPayload: []byte(`{"question":"retry scope?","to":"parent"}`)},
	})

	rows := channelRows(t, s, team)
	root := ThreadRootTaskID(a)
	inThread := 0
	for i := range rows {
		if rows[i].ThreadRoot == root {
			inThread++
		}
	}
	// a's assigned line, its failure line, the attempt-open line of the
	// supersede, the retry assignment and the retry question share one
	// thread; TaskStarted makes no line (§6.7).
	if inThread != 5 {
		t.Fatalf("thread %q holds %d lines, want 5", root, inThread)
	}
	q := findChannelLine(rows, ChannelKindQuestion)
	if q == nil || q.ThreadRoot != root {
		t.Fatalf("retry question line = %+v, want it in thread %q", q, root)
	}
	// The read API pages the shared thread as one.
	page, err := s.TeamChannelMessages(t.Context(), team, TeamChannelPageQuery{ThreadRoots: []string{root}, Limit: 100})
	requireNoError(t, err)
	if len(page) != inThread {
		t.Fatalf("thread page = %d lines, want %d", len(page), inThread)
	}
}

func TestChannelRebuildsFromTheTeamEventLog(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	team := "chan-rebuild"
	lead, mgr, a, b := team+"-lead", team+"-mgr", team+"-a", team+"-b"
	seq := seedChannelTeam(t, s, team, map[string]bool{lead: true, mgr: true, a: true, b: true})
	seq = appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "n-" + team, EventType: EventTeamNoted, ProtectedPayload: []byte(`{"author_task_id":"` + mgr + `","note":"pin the dependency"}`)},
		{EventID: "q-" + team, TaskID: a, EventType: EventTaskQuestionAsked, ProtectedPayload: []byte(`{"question":"import order?","to":"parent"}`)},
		{EventID: "ans-" + team, TaskID: a, EventType: EventTaskQuestionAnswered, ProtectedPayload: []byte(`{"answer":"stdlib first","answered_by":"` + mgr + `"}`)},
		{EventID: "msg-" + team, TaskID: a, EventType: EventTaskMessaged, ProtectedPayload: []byte(`{"to_task_id":"` + b + `","message":"branch pushed"}`)},
		{EventID: "stall-" + team, TaskID: b, EventType: EventTaskStalled, ProtectedPayload: []byte(`{"task_id":"` + b + `","waiting_on":"vendor","silent_seconds":600,"team_wide":false}`)},
		{EventID: "rep-" + team, TaskID: b, EventType: EventTaskReported, ProtectedPayload: []byte(`{"status":"succeeded","summary":"done"}`)},
		{EventID: "fail-" + team, TaskID: a, EventType: EventTaskEnded, Outcome: outcomeFailed, FailureCode: "deadline"},
	})
	res, err := s.RetryTask(t.Context(), team, a, &TaskRetryInput{ExpectedSeq: seq, SupersededEventID: "sup-" + team, CreatedEventID: "c-retry-" + team})
	if err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	seq = res.LastSeq
	appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "as-r-" + team, TaskID: a + "~r1", EventType: EventTaskAssigned},
	})

	// Rebuild: replay the log through the mapping and compare with the table.
	events, err := s.TeamEventsSince(t.Context(), team, 0, 1000)
	requireNoError(t, err)
	var rebuilt []TeamChannelMessage
	channelSeq := int64(0)
	for i := range events {
		ev := &AppendTeamEventInput{
			TaskID:    events[i].TaskID,
			EventType: events[i].EventType,
			Outcome:   events[i].Outcome,
		}
		if events[i].FailureCode != "" {
			ev.FailureCode = events[i].FailureCode
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
	stored := channelRows(t, s, team)
	slices.Reverse(stored) // the read is newest first; rebuild walks the log forward
	if diff := cmp.Diff(stored, rebuilt); diff != "" {
		t.Fatalf("rebuild from the event log differs (-stored +rebuilt):\n%s", diff)
	}
}

func TestStallEventPostsIntoTheTaskThread(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	team := "chan-stall"
	lead, b := team+"-lead", team+"-b"
	seq := seedChannelTeam(t, s, team, map[string]bool{lead: true, b: true})
	stall := []byte(`{"task_id":"` + b + `","waiting_on":"vendor","silent_seconds":600,"team_wide":false}`)
	seq = appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "stall-task-" + team, TaskID: b, EventType: EventTaskStalled, ProtectedPayload: stall},
	})
	rows := channelRows(t, s, team)
	stalls := 0
	for i := range rows {
		if rows[i].Kind == ChannelKindStall {
			stalls++
			if rows[i].ThreadRoot != ThreadRootTaskID(b) || rows[i].SenderKind != ChannelSenderWatchdog {
				t.Fatalf("stall line = %+v, want a watchdog line in b's thread", rows[i])
			}
		}
	}
	if stalls != 1 {
		t.Fatalf("task stall projected %d line(s), want 1", stalls)
	}

	// §6.7: the same stall also shows in the general channel when the whole
	// team is waiting.
	appendSeq(t, s, team, seq, []AppendTeamEventInput{
		{EventID: "stall-team-" + team, TaskID: b, EventType: EventTaskStalled, ProtectedPayload: []byte(`{"task_id":"` + b + `","waiting_on":"vendor","silent_seconds":900,"team_wide":true}`)},
	}) // seq not needed: the stall test reads rows back
	rows = channelRows(t, s, team)
	stalls = 0
	for i := range rows {
		if rows[i].Kind == ChannelKindStall {
			stalls++
		}
	}
	if stalls != 3 {
		t.Fatalf("team-wide stall projected %d stall line(s) in total, want 3", stalls)
	}
	page, err := s.TeamChannelMessages(t.Context(), team, TeamChannelPageQuery{GeneralOnly: true, Limit: 10})
	requireNoError(t, err)
	if len(page) == 0 || page[0].Kind != ChannelKindStall {
		t.Fatalf("general channel page = %+v, want the team-wide stall on top", page)
	}
}

func TestChannelPageQueryScopesAndCursors(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	team := "chan-page"
	lead, mgr, a, b := team+"-lead", team+"-mgr", team+"-a", team+"-b"
	seedChannelTeam(t, s, team, map[string]bool{lead: true, mgr: true, a: true, b: true})
	appendSeq(t, s, team, 14, []AppendTeamEventInput{
		{EventID: "q-page", TaskID: a, EventType: EventTaskQuestionAsked, ProtectedPayload: []byte(`{"question":"page?","to":"parent"}`)},
		{EventID: "ans-page", TaskID: a, EventType: EventTaskQuestionAnswered, ProtectedPayload: []byte(`{"answer":"yes","answered_by":"` + mgr + `"}`)},
	})
	all := channelRows(t, s, team)
	if len(all) != 8 {
		t.Fatalf("channel holds %d lines, want 8 (plan, decision, 4 assignments, question, answer)", len(all))
	}
	page, err := s.TeamChannelMessages(t.Context(), team, TeamChannelPageQuery{ThreadRoots: []string{ThreadRootTaskID(a)}, Limit: 10})
	requireNoError(t, err)
	if len(page) != 3 {
		t.Fatalf("a's thread page = %d lines, want 3 (assigned, question, answer)", len(page))
	}
	if page[0].ChannelSeq != 8 || page[1].ChannelSeq != 7 || page[2].ChannelSeq != 5 {
		t.Fatalf("newest-first thread page = %d,%d,%d, want 8,7,5", page[0].ChannelSeq, page[1].ChannelSeq, page[2].ChannelSeq)
	}
	back, err := s.TeamChannelMessages(t.Context(), team, TeamChannelPageQuery{Before: page[0].ChannelSeq, ThreadRoots: []string{ThreadRootTaskID(a)}, Limit: 10})
	requireNoError(t, err)
	if len(back) != 2 || back[0].ChannelSeq != page[1].ChannelSeq {
		t.Fatalf("backward page = %+v, want lines below #%d", back, page[0].ChannelSeq)
	}
	since, err := s.TeamChannelMessages(t.Context(), team, TeamChannelPageQuery{Since: back[0].ChannelSeq, ThreadRoots: []string{ThreadRootTaskID(a)}, Limit: 10})
	requireNoError(t, err)
	if len(since) != 1 || since[0].ChannelSeq != page[0].ChannelSeq {
		t.Fatalf("forward page = %+v, want line #%d only", since, page[0].ChannelSeq)
	}
	empty, err := s.TeamChannelMessages(t.Context(), "missing-team", TeamChannelPageQuery{Limit: 10})
	requireNoError(t, err)
	if len(empty) != 0 {
		t.Fatalf("missing team read returned %d rows, want 0", len(empty))
	}
}
