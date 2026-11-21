package app

// F11.10a channel evidence at the tool and digest layer (§6.7, Q-176 A,
// Q-177 A, Q-178 A): fugue_team_read scopes by role and pages both ways;
// sibling sends only reach tasks of the same direct delegator and obey the
// 6/30-per-10-minutes caps; digests cite the #n of their source lines and the
// delegator's mention rides its next digest without a separate wake; and no
// channel stream exists for agents to subscribe to — they only read through
// the tool, which wakes no one.

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

type channelFixture struct {
	*wakeFixture
	teamID                                                string
	leadID, mgrID, memAID, memBID, memCID                 string
	leadBinding, mgrBinding, aBinding, bBinding, cBinding SessionBinding
}

// newChannelFixture stores an active team with a manager holding two members
// (a and b) and a lead holding one member (c), every task running in a fake
// ready session. The service's wake is detached: the tests drive their own
// pump, so deliveries are observed deterministically on the fake sessions.
func newChannelFixture(t *testing.T) *channelFixture {
	t.Helper()
	f := newWakeFixture(t)
	f.rt.teamTasks.wake = nil
	ctx := t.Context()
	store := f.hp.vendorStore

	roleBytes, err := json.Marshal(teamDecisionPayload{
		Decision: decisionApprove,
		Roles:    []decidedRole{{Name: teamTestRole, Vendor: VendorClaude, Permission: string(teams.PermissionRead)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	propBytes, err := json.Marshal(teams.ProposalInput{
		Title: "Channel", Brief: "Shared team brief.",
		Roles: []teams.ProposedRole{{Name: teamTestRole, Vendor: VendorClaude, Permission: teams.PermissionRead}},
	})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(teams.ProtectedTaskContent{Goal: "Channel fixture work."})
	if err != nil {
		t.Fatal(err)
	}

	teamID := "chan-team"
	L, M, A, B, C := teamID+"-lead", teamID+"-mgr", teamID+"-a", teamID+"-b", teamID+"-c"
	seedTeamSessions(t, f.teamProposalFixture, teamID, [][3]string{
		{"chan-mgr", M, string(vendorstore.RoleTeamManager)},
		{"chan-a", A, string(vendorstore.RoleMember)},
		{"chan-b", B, string(vendorstore.RoleMember)},
		{"chan-c", C, string(vendorstore.RoleMember)},
	})
	_, _, err = store.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: f.wsID, SessionID: f.session.ID,
		ProposedEventID: "chan-prop", ProposedPayload: propBytes,
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: L, HierarchyRole: vendorstore.RoleLead, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "chan-c1", ProtectedSpec: spec},
			{TaskID: M, ParentTaskID: L, HierarchyRole: vendorstore.RoleTeamManager, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "chan-c2", ProtectedSpec: spec},
			{TaskID: A, ParentTaskID: M, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "chan-c3", ProtectedSpec: spec},
			{TaskID: B, ParentTaskID: M, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "chan-c4", ProtectedSpec: spec},
			{TaskID: C, ParentTaskID: L, HierarchyRole: vendorstore.RoleMember, CatalogRole: teamTestRole, Deliverable: vendorstore.DeliverableReport, CreatedEventID: "chan-c5", ProtectedSpec: spec, ExplicitStart: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.appendTeamEvent(t, teamID, &vendorstore.AppendTeamEventInput{EventID: "chan-start", EventType: vendorstore.EventTeamStarted, ProtectedPayload: roleBytes})
	for _, bind := range [][2]string{
		{L, f.session.ID}, {M, "chan-mgr"}, {A, "chan-a"}, {B, "chan-b"}, {C, "chan-c"},
	} {
		f.appendTeamEvent(t, teamID, &vendorstore.AppendTeamEventInput{EventID: "as-" + bind[0], TaskID: bind[0], EventType: vendorstore.EventTaskAssigned, SessionID: bind[1]})
		f.appendTeamEvent(t, teamID, &vendorstore.AppendTeamEventInput{EventID: "st-" + bind[0], TaskID: bind[0], EventType: vendorstore.EventTaskStarted})
	}
	return &channelFixture{
		wakeFixture: f,
		teamID:      teamID, leadID: L, mgrID: M, memAID: A, memBID: B, memCID: C,
		leadBinding: f.binding,
		mgrBinding:  mustAttachRole(t, f.teamProposalFixture, "chan-mgr", vendorstore.RoleTeamManager),
		aBinding:    mustAttachRole(t, f.teamProposalFixture, "chan-a", vendorstore.RoleMember),
		bBinding:    mustAttachRole(t, f.teamProposalFixture, "chan-b", vendorstore.RoleMember),
		cBinding:    mustAttachRole(t, f.teamProposalFixture, "chan-c", vendorstore.RoleMember),
	}
}

func (f *channelFixture) siblingSend(t *testing.T, b SessionBinding, targetID, message string) {
	t.Helper()
	res, err := f.tools.Call(t.Context(), b, taskSendToolName, []byte(`{"task_id":"`+targetID+`","message":"`+message+`"}`))
	if err != nil || res.IsError {
		t.Fatalf("sibling send to %s = (%+v, %v), want success", targetID, res, err)
	}
}

func (f *channelFixture) readChannel(t *testing.T, b SessionBinding, args string) string {
	t.Helper()
	res, err := f.tools.Call(t.Context(), b, teamReadToolName, []byte(args))
	if err != nil || res.IsError {
		t.Fatalf("fugue_team_read(%s) = (%+v, %v), want success", args, res, err)
	}
	return res.Text
}

func (f *channelFixture) sendChannelLine(t *testing.T) int64 {
	t.Helper()
	rows, err := f.hp.vendorStore.TeamChannelMessages(t.Context(), f.teamID, vendorstore.TeamChannelPageQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Kind == vendorstore.ChannelKindSend {
			return rows[i].ChannelSeq
		}
	}
	t.Fatal("no send line in the channel")
	return 0
}

// firstChannelNumber parses the #n of the first rendered line.
func firstChannelNumber(text string) int64 {
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		if n, err := strconv.ParseInt(strings.Fields(line)[0][1:], 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func TestSiblingSendOnlyToSameDelegatorAndCapped(t *testing.T) {
	t.Parallel()
	f := newChannelFixture(t)
	ctx := t.Context()
	svc := f.rt.teamTasks

	// OR-E20: the sibling send is delivered, in a's thread, and the
	// delegator's mention never schedules its own delivery.
	f.siblingSend(t, f.aBinding, f.memBID, "diff is up")
	line := f.sendChannelLine(t)
	if line == 0 {
		t.Fatal("sibling send projected no channel line")
	}
	pending, err := f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sawTarget, sawDelegator := false, false
	for _, r := range pending {
		switch r.RecipientTaskID {
		case f.memBID:
			sawTarget = true
		case f.mgrID:
			sawDelegator = true
		}
	}
	if !sawTarget || sawDelegator {
		t.Fatalf("pending = %+v, want the target only (the informational mention must not wake the delegator)", pending)
	}

	// Other delegators, other teams, the caller itself, and the lead's
	// non-child targets are rejected with typed codes.
	if _, err := svc.Send(ctx, f.aBinding.SessionID, f.memCID, "hi"); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("send across delegators code = %v, want permission_denied", errcode.CodeOf(err))
	}
	if _, err := svc.Send(ctx, f.aBinding.SessionID, "chan-other-o", "hi"); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("send to another team's task code = %v, want not_found", errcode.CodeOf(err))
	}
	if _, err := svc.Send(ctx, f.aBinding.SessionID, f.memAID, "hi"); errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("send to self code = %v, want failed_precondition", errcode.CodeOf(err))
	}
	if _, err := svc.Send(ctx, f.leadBinding.SessionID, f.memAID, "hi"); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("lead sibling send code = %v, want permission_denied", errcode.CodeOf(err))
	}
	// A manager may send to a sibling under the same delegator.
	f.siblingSend(t, f.mgrBinding, f.memCID, "heads up")

	// The agent cap: six sends pass, the seventh is rejected typed.
	for range 5 {
		f.siblingSend(t, f.aBinding, f.memBID, "more")
	}
	if _, err := svc.Send(ctx, f.aBinding.SessionID, f.memBID, "one too many"); errcode.CodeOf(err) != errcode.TasksSendRateLimited {
		t.Fatalf("seventh sibling send code = %v, want tasks.send_rate_limited", errcode.CodeOf(err))
	}

	// The team cap counts every agent's sends: fill the window to 30 with
	// direct store appends from the manager, then member b (whose own count
	// is zero) is rejected by the team cap on a valid sibling target.
	rec, _, err := f.hp.vendorStore.TeamSnapshot(ctx, f.teamID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 24 {
		seq, appendErr := f.hp.vendorStore.AppendTeamEvent(ctx, f.teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
			EventID: "cap-" + strconv.Itoa(i), TaskID: f.memBID, EventType: vendorstore.EventTaskMessaged,
			ProtectedPayload: []byte(`{"to_task_id":"` + f.memAID + `","message":"filler"}`),
		})
		if appendErr != nil {
			t.Fatalf("append cap filler %d: %v", i, appendErr)
		}
		rec.LastSeq = seq
	}
	if _, err := svc.Send(ctx, f.bBinding.SessionID, f.memAID, "fresh agent"); errcode.CodeOf(err) != errcode.TasksSendRateLimited {
		t.Fatalf("send over the team cap code = %v, want tasks.send_rate_limited", errcode.CodeOf(err))
	}
}

func TestDigestCitesChannelNumbers(t *testing.T) {
	t.Parallel()
	f := newChannelFixture(t)
	ctx := t.Context()

	// The sibling send becomes channel line #k; the target's digest cites it.
	f.siblingSend(t, f.aBinding, f.memBID, "diff is up")
	k := f.sendChannelLine(t)
	if err := f.wake.Pump(ctx); err != nil {
		t.Fatal(err)
	}
	sent := f.sessions.sends["chan-b"]
	if len(sent) != 1 {
		t.Fatalf("b received %d turn(s), want the one digest", len(sent))
	}
	if !strings.Contains(sent[0], "(see #"+strconv.FormatInt(k, 10)+")") {
		t.Fatalf("b's digest does not cite #%d: %q", k, sent[0])
	}
	if got := f.sessions.sends["chan-mgr"]; len(got) != 0 {
		t.Fatalf("the delegator was woken for the informational mention: %q", got)
	}

	// The delegator sees the send in its NEXT digest, without a separate
	// wake: a real question starts M's digest window and the mention rides it.
	if _, err := f.rt.teamTasks.Ask(ctx, f.aBinding.SessionID, "which branch?", ""); err != nil {
		t.Fatalf("ask: %v", err)
	}
	f.clk.Advance(digestWait + time.Second)
	if err := f.wake.Pump(ctx); err != nil {
		t.Fatal(err)
	}
	mgrSent := f.sessions.sends["chan-mgr"]
	if len(mgrSent) != 1 {
		t.Fatalf("the delegator received %d turn(s), want one digest", len(mgrSent))
	}
	if !strings.Contains(mgrSent[0], "diff is up") || !strings.Contains(mgrSent[0], "(see #"+strconv.FormatInt(k, 10)+")") {
		t.Fatalf("the delegator's digest lacks the mention citation: %q", mgrSent[0])
	}
}

func TestTeamReadScopesByRoleAndPagesBothWays(t *testing.T) {
	t.Parallel()
	f := newChannelFixture(t)
	ctx := t.Context()
	svc := f.rt.teamTasks

	f.siblingSend(t, f.aBinding, f.memBID, "diff is up")
	if _, err := svc.Note(ctx, f.leadBinding.SessionID, "pin the v2 API"); err != nil {
		t.Fatalf("note: %v", err)
	}

	// A member reads its own thread by default, the general channel and a
	// sibling thread on request; nothing else.
	own := f.readChannel(t, f.aBinding, `{}`)
	if !strings.Contains(own, "untrusted source=team-channel") || !strings.Contains(own, "diff is up") {
		t.Fatalf("own-thread read = %q, want the send line and its data block", own)
	}
	if strings.Contains(own, "pin the v2 API") {
		t.Fatal("the own-thread read leaked the general channel")
	}
	general := f.readChannel(t, f.aBinding, `{"thread":"general"}`)
	if !strings.Contains(general, "Plan proposal") || !strings.Contains(general, "pin the v2 API") {
		t.Fatalf("general read = %q, want the plan and the note", general)
	}
	sibling := f.readChannel(t, f.aBinding, `{"thread":"`+f.memBID+`"}`)
	if !strings.Contains(sibling, "task "+f.memBID+" delegated") {
		t.Fatalf("sibling-thread read = %q, want b's assignment", sibling)
	}
	if _, err := svc.TeamRead(ctx, f.aBinding.SessionID, "all", 0, 0, 0); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("member whole-team read code = %v, want permission_denied", errcode.CodeOf(err))
	}
	if _, err := svc.TeamRead(ctx, f.aBinding.SessionID, f.mgrID, 0, 0, 0); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("member manager-thread read code = %v, want permission_denied", errcode.CodeOf(err))
	}

	// The manager reads its part and the general channel, not the lead's
	// other part.
	for _, target := range []string{f.memAID, f.memBID, "general"} {
		if _, err := svc.TeamRead(ctx, f.mgrBinding.SessionID, target, 0, 0, 0); err != nil {
			t.Fatalf("manager read of %s: %v", target, err)
		}
	}
	if _, err := svc.TeamRead(ctx, f.mgrBinding.SessionID, f.memCID, 0, 0, 0); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("manager read outside its part code = %v, want permission_denied", errcode.CodeOf(err))
	}
	if _, err := svc.TeamRead(ctx, f.mgrBinding.SessionID, "all", 0, 0, 0); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("manager whole-team read code = %v, want permission_denied", errcode.CodeOf(err))
	}

	// The lead reads everything.
	all, err := svc.TeamRead(ctx, f.leadBinding.SessionID, "all", 0, 0, 0)
	if err != nil {
		t.Fatalf("lead whole-team read: %v", err)
	}
	if !strings.Contains(all, "task "+f.memCID+" delegated") || !strings.Contains(all, "pin the v2 API") {
		t.Fatalf("lead whole-team read = %q, want every thread and the general channel", all)
	}

	// Paging both ways on a's own thread: limit 1 gives the newest line,
	// `before` steps back, `since` steps forward.
	page1 := f.readChannel(t, f.aBinding, `{"limit":1}`)
	top := firstChannelNumber(page1)
	if top == 0 {
		t.Fatalf("paged read = %q, want a numbered line", page1)
	}
	page2 := f.readChannel(t, f.aBinding, `{"before":`+strconv.FormatInt(top, 10)+`,"limit":1}`)
	older := firstChannelNumber(page2)
	if older == 0 || older >= top {
		t.Fatalf("backward page = %q, want a line below #%d", page2, top)
	}
	page3 := f.readChannel(t, f.aBinding, `{"since":`+strconv.FormatInt(older, 10)+`,"limit":10}`)
	if got := firstChannelNumber(page3); got != top {
		t.Fatalf("forward page = %q, want #%d on top", page3, top)
	}
}

func TestAgentsNeverReceiveTheChannelStream(t *testing.T) {
	t.Parallel()
	f := newChannelFixture(t)
	ctx := t.Context()

	// No channel topic exists to subscribe to: registration is the
	// authorization registry, so `team:<id>` is denied for every principal,
	// including the session principal a vendor tool would hold.
	for _, principal := range []string{"session", "app"} {
		_, err := f.rt.Subscribe(ctx, SubscribeParams{Principal: principal, Topic: "team:" + f.teamID})
		if !errors.Is(err, ErrSubscriptionDenied) {
			t.Fatalf("subscribe(%s, team:<id>) err = %v, want ErrSubscriptionDenied", principal, err)
		}
	}

	// Reading wakes no one and moves no mail: the read path never publishes
	// and never claims a batch.
	f.siblingSend(t, f.aBinding, f.memBID, "diff is up")
	before, err := f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.rt.teamTasks.TeamRead(ctx, f.bBinding.SessionID, "", 0, 0, 0); err != nil {
		t.Fatalf("read: %v", err)
	}
	after, err := f.hp.vendorStore.PendingMailbox(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("pending mailbox changed across a read: %d -> %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("pending mailbox changed across a read: %+v -> %+v", before[i], after[i])
		}
	}
	if got := f.sessions.sends["chan-b"]; len(got) != 0 {
		t.Fatalf("a read delivered a turn: %q", got)
	}
}
