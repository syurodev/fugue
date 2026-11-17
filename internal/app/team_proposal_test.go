package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	teamTestWorkspace = "team-ws"
	teamTestRole      = "researcher"
	teamTestLeadTask  = "t-lead"
	teamTestPending   = "pending"
	teamTestRejected  = "rejected"
)

// teamProposalFixture is the F11.2a fixture: a host with the fake vendor CLI,
// a workspace whose catalog carries one read role, and a live Claude session
// holding a turn, so the propose path runs against a real actor.
type teamProposalFixture struct {
	hp      *HostProcess
	rt      *HostRuntime
	svc     *VendorService
	tools   *SessionToolService
	session VendorSessionView
	binding SessionBinding
	wsID    string
}

func newTeamProposalFixture(t *testing.T, extraTOML string) *teamProposalFixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(200, 0))
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir: t.TempDir(), WorkspaceEnv: config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config: testHostConfig(), Clock: clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("team-test")),
		Transport: newScriptedTransport(), VendorBinary: exe,
		Bridge: VendorBridge{Binary: exe, Socket: filepath.Join(t.TempDir(), "host.sock")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	if _, setupErr := hp.Vault().Setup(t.Context(), redact.NewSecret("team-test-password")); setupErr != nil {
		t.Fatal(setupErr)
	}
	rt := hp.Runtime()
	svc, err := rt.VendorService()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := rt.SessionTools()
	if err != nil {
		t.Fatal(err)
	}
	ws, err := rt.CreateWorkspace(t.Context(), testAppPrincipal, teamTestWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, cfgErr := rt.UpdateWorkspaceConfig(t.Context(), testAppPrincipal, ws.ID, teamWorkspaceTOML(t, extraTOML), ws.ConfigHash); cfgErr != nil {
		t.Fatal(cfgErr)
	}
	session, binding := startTeamFixtureSession(t, rt, svc, tools, ws.ID)
	return &teamProposalFixture{hp: hp, rt: rt, svc: svc, tools: tools, session: session, binding: binding, wsID: ws.ID}
}

// startTeamFixtureSession starts one Claude session in the workspace and
// holds its turn, so the propose path runs against a live actor.
func startTeamFixtureSession(t *testing.T, rt *HostRuntime, svc *VendorService, tools *SessionToolService, workspaceID string) (VendorSessionView, SessionBinding) {
	t.Helper()
	sub, err := rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	session, err := svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{CommandID: "team-start", Vendor: VendorClaude, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	awaitSessionState(t, sub, session.ID, "ready")
	if sendErr := svc.Send(t.Context(), testAppPrincipal, VendorInput{CommandID: "team-turn", SessionID: session.ID, Text: fakeCLIHold}); sendErr != nil {
		t.Fatal(sendErr)
	}
	tools.tokens.mu.Lock()
	binding, ok := tools.tokens.bySession[session.ID]
	tools.tokens.mu.Unlock()
	if !ok {
		t.Fatalf("missing live session binding for %s", session.ID)
	}
	return session, binding
}

func teamWorkspaceTOML(t *testing.T, extra string) []byte {
	t.Helper()
	root := filepath.ToSlash(t.TempDir())
	raw := "name = \"" + teamTestWorkspace + "\"\n" + extra +
		"[[roots]]\nname = \"main\"\npath = \"" + root + "\"\naccess = \"rw\"\n" +
		"[[roles]]\nname = \"" + teamTestRole + "\"\nvendor = \"claude\"\npermission = \"read\"\n"
	return []byte(raw)
}

func validTeamProposalArgs() *ProposeArgs {
	return &ProposeArgs{
		Title: "Split the syntax work",
		Brief: "Two independent read streams reported to the lead.",
		Tasks: []teams.ProposedTask{
			{ID: teamTestLeadTask, TeamRole: teams.RoleLead, Role: teamTestRole, Goal: "Coordinate the streams", WriteScope: []string{}, Deliverable: teams.DeliverableReport},
			{ID: "t-a", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Read the lexer", WriteScope: []string{"docs/lexer.md"}, Deliverable: teams.DeliverableReport},
			{ID: "t-b", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Read the parser", WriteScope: []string{"docs/parser.md"}, Deliverable: teams.DeliverableReport},
		},
	}
}

// proposeThroughTool drives the real fugue_team_propose tool call path.
func (f *teamProposalFixture) proposeThroughTool(t *testing.T, args *ProposeArgs) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal proposal args: %v", err)
	}
	result, callErr := f.tools.Call(t.Context(), f.binding, teamProposeToolName, raw)
	if callErr != nil {
		return "", callErr
	}
	if result.IsError {
		t.Fatalf("tool returned an error result: %s", result.Text)
	}
	id, ok := strings.CutPrefix(result.Text, "proposal ")
	if !ok {
		t.Fatalf("tool result %q does not name a proposal", result.Text)
	}
	return strings.TrimSuffix(id, " is recorded and waits for the user's decision in the approval inbox"), nil
}

func (f *teamProposalFixture) pendingCards(t *testing.T) []PendingInteractionView {
	t.Helper()
	views, _, err := f.rt.ListPendingInteractions(t.Context(), testAppPrincipal)
	if err != nil {
		t.Fatal(err)
	}
	cards := make([]PendingInteractionView, 0, len(views))
	for i := range views {
		if views[i].SessionID == f.session.ID && views[i].Kind == string(interaction.KindQuestion) && views[i].Tool == teamProposeToolName {
			cards = append(cards, views[i])
		}
	}
	return cards
}

func (f *teamProposalFixture) teamSnapshot(t *testing.T, teamID string) (vendorstore.TeamRecord, []vendorstore.TaskRecord) {
	t.Helper()
	rec, tasks, err := f.hp.vendorStore.TeamSnapshot(t.Context(), teamID)
	if err != nil {
		t.Fatalf("TeamSnapshot(%s): %v", teamID, err)
	}
	return rec, tasks
}

func (f *teamProposalFixture) proposalItem(t *testing.T, teamID string) vendorstore.StoredItem {
	t.Helper()
	itemID, ok, err := f.hp.vendorStore.ProposalItemID(t.Context(), f.session.ID, teamID)
	if err != nil {
		t.Fatalf("ProposalItemID: %v", err)
	}
	if !ok {
		t.Fatalf("no team_proposal item for %s", teamID)
	}
	item, err := f.hp.vendorStore.StoredItemMeta(t.Context(), f.session.ID, itemID)
	if err != nil {
		t.Fatalf("StoredItemMeta: %v", err)
	}
	return item
}

// TestNoTaskStartsBeforeApproval pins OR-E13's state half: the stored team is
// proposed, every task is queued, the item is pending and nothing but the
// proposing session exists.
func TestNoTaskStartsBeforeApproval(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	proposalID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if proposalID == "" {
		t.Fatal("Propose returned no proposal ID")
	}
	assertProposedTeam(t, f, proposalID, 3)
	sessions, err := f.hp.vendorStore.ListSessions(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != f.session.ID {
		t.Fatalf("sessions = %+v, want only the proposing session", sessions)
	}
	item := f.proposalItem(t, proposalID)
	if item.Metadata.Decision != teamTestPending || item.Metadata.AgentCount != 2 {
		t.Fatalf("item metadata = %+v, want pending with 2 agents", item.Metadata)
	}
	assertSingleProposalCard(t, f)
}

// assertProposedTeam asserts the team is proposed with every task still
// queued (OR-E13's state half).
func assertProposedTeam(t *testing.T, f *teamProposalFixture, teamID string, wantTasks int) (vendorstore.TeamRecord, []vendorstore.TaskRecord) {
	t.Helper()
	rec, tasks := f.teamSnapshot(t, teamID)
	if rec.State != vendorstore.TeamStateProposed {
		t.Fatalf("team state = %s, want proposed", rec.State)
	}
	if len(tasks) != wantTasks {
		t.Fatalf("task count = %d, want %d", len(tasks), wantTasks)
	}
	for i := range tasks {
		if tasks[i].State != vendorstore.TaskStateQueued {
			t.Fatalf("task %s state = %s, want queued", tasks[i].TaskID, tasks[i].State)
		}
	}
	return rec, tasks
}

// assertSingleProposalCard asserts exactly one proposal card with the four
// §5.3 choices is pending for the fixture's session.
func assertSingleProposalCard(t *testing.T, f *teamProposalFixture) {
	t.Helper()
	cards := f.pendingCards(t)
	if len(cards) != 1 {
		t.Fatalf("pending cards = %d, want 1", len(cards))
	}
	if len(cards[0].Questions) != 1 || len(cards[0].Questions[0].Options) != 4 {
		t.Fatalf("card questions/options = %+v, want one question with four options", cards[0].Questions)
	}
}

// TestNewProposalSupersedesThePendingOne pins Q-103: the old proposal is
// rejected with the superseded note, its tasks settle before TeamEnded, and
// only the new card stays pending.
func TestNewProposalSupersedesThePendingOne(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	first, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose 1: %v", err)
	}
	second, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose 2: %v", err)
	}
	if first == second {
		t.Fatal("both proposals share one ID")
	}
	rec, tasks := f.teamSnapshot(t, first)
	if rec.State != vendorstore.TeamStateCancelled {
		t.Fatalf("superseded team state = %s, want cancelled", rec.State)
	}
	for i := range tasks {
		if tasks[i].State != vendorstore.TaskStateCancelled {
			t.Fatalf("superseded task %s state = %s, want cancelled", tasks[i].TaskID, tasks[i].State)
		}
	}
	assertTasksSettledBeforeTeamEnd(t, f, first)
	if item := f.proposalItem(t, first); item.Metadata.Decision != teamTestRejected {
		t.Fatalf("superseded item decision = %s, want rejected", item.Metadata.Decision)
	}
	if rec2, _ := f.teamSnapshot(t, second); rec2.State != vendorstore.TeamStateProposed {
		t.Fatalf("second team state = %s, want proposed", rec2.State)
	}
	if item := f.proposalItem(t, second); item.Metadata.Decision != teamTestPending {
		t.Fatalf("second item decision = %s, want pending", item.Metadata.Decision)
	}
	assertSingleProposalCard(t, f)
}

// assertTasksSettledBeforeTeamEnd pins the Q-84 order: every task-cancel
// event precedes TeamEnded.
func assertTasksSettledBeforeTeamEnd(t *testing.T, f *teamProposalFixture, teamID string) {
	t.Helper()
	events, err := f.hp.vendorStore.TeamEventsSince(t.Context(), teamID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	endedAt := slices.IndexFunc(events, func(e vendorstore.RecordedTeamEvent) bool { return e.EventType == vendorstore.EventTeamEnded })
	if endedAt < 0 {
		t.Fatal("superseded team has no TeamEnded event")
	}
	for i := range events[:endedAt] {
		if events[i].EventType == vendorstore.EventTeamEnded {
			t.Fatal("TeamEnded appended before every task settled (Q-84)")
		}
	}
}

// TestPendingProposalIsCancelledWhenTheSessionEnds pins Q-104: closing the
// proposing session cancels its pending proposal and invalidates the card.
func TestPendingProposalIsCancelledWhenTheSessionEnds(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	proposalID, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	sub, subErr := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if subErr != nil {
		t.Fatal(subErr)
	}
	defer sub.Close()
	if _, closeErr := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "team-close", SessionID: f.session.ID}); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	awaitSessionState(t, sub, f.session.ID, "closed")
	rec, tasks, item := awaitCancelledProposal(t, f, proposalID)
	if rec.State != vendorstore.TeamStateCancelled {
		t.Fatalf("team state after close = %s, want cancelled", rec.State)
	}
	for i := range tasks {
		if tasks[i].State != vendorstore.TaskStateCancelled {
			t.Fatalf("task %s state after close = %s, want cancelled", tasks[i].TaskID, tasks[i].State)
		}
	}
	if item.Metadata.Decision != teamTestRejected {
		t.Fatalf("item decision after close = %s, want rejected", item.Metadata.Decision)
	}
	payload, err := f.hp.vendorStore.OpenTeamEventPayload(t.Context(), proposalID, rec.LastSeq)
	if err != nil {
		t.Fatalf("OpenTeamEventPayload: %v", err)
	}
	var decision cancelDecisionPayload
	if err := json.Unmarshal(payload, &decision); err != nil {
		t.Fatalf("unmarshal decision payload: %v", err)
	}
	if decision.Note != "session ended" {
		t.Fatalf("cancel note = %q, want session ended", decision.Note)
	}
}

// TestProposalValidatesAgainstTheRoleCatalog pins the catalog validation on
// the service path: unknown roles and disabled workspaces fail typed.
func TestProposalValidatesAgainstTheRoleCatalog(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	bad := validTeamProposalArgs()
	bad.Tasks[1].Role = "astronaut"
	_, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, bad)
	if errcode.CodeOf(err) != errcode.InvalidArgument || !errors.Is(err, teams.ErrUnknownRole) {
		t.Fatalf("unknown role error = %v (code %v), want invalid argument wrapping ErrUnknownRole", err, errcode.CodeOf(err))
	}
	// A rejected proposal never allocates a team: no pending team row exists.
	if _, ok, err := f.hp.vendorStore.PendingTeamForSession(t.Context(), f.session.ID); err != nil || ok {
		t.Fatalf("rejected proposal left a pending team = (%v, %v)", ok, err)
	}
	// A workspace with the flag off refuses the proposal (Q-105).
	disabled := newTeamProposalFixture(t, "[session]\nteams = false\n")
	if _, err := disabled.rt.teamProposals.Propose(t.Context(), disabled.session.ID, validTeamProposalArgs()); errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("disabled workspace error = %v, want failed precondition", err)
	}
}

// TestOverlappingWriteScopeWithoutDependsOnWarns pins the §5.3 warning on the
// card the user reviews.
func TestOverlappingWriteScopeWithoutDependsOnWarns(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	args := validTeamProposalArgs()
	args.Tasks[2].WriteScope = []string{"docs/lexer.md"}
	if _, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, args); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	cards := f.pendingCards(t)
	if len(cards) != 1 {
		t.Fatalf("pending cards = %d, want 1", len(cards))
	}
	if !strings.Contains(cards[0].Summary, "Split the syntax work") || !strings.Contains(cards[0].Body, "scope_overlap") {
		t.Fatalf("card does not carry the overlap warning: %q / %q", cards[0].Summary, cards[0].Body)
	}
	clean := newTeamProposalFixture(t, "")
	if _, err := clean.rt.teamProposals.Propose(t.Context(), clean.session.ID, validTeamProposalArgs()); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	for _, card := range clean.pendingCards(t) {
		if strings.Contains(card.Body, "scope_overlap") {
			t.Fatalf("disjoint scopes warned: %q", card.Body)
		}
	}
}

// TestUsageEstimateSaysNoDataInsteadOfGuessing pins §5.3: stage A has no task
// usage history, so the card says so, and the median path only renders a
// known unit.
func TestUsageEstimateSaysNoDataInsteadOfGuessing(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	if _, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs()); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	for _, card := range f.pendingCards(t) {
		if !strings.Contains(card.Body, "no usage data yet") {
			t.Fatalf("card body lacks the no-data line: %q", card.Body)
		}
		if strings.Contains(card.Body, "$") || strings.Contains(card.Body, "quota window per task") {
			t.Fatalf("card body guesses a number: %q", card.Body)
		}
	}
	for _, tc := range []struct {
		name   string
		values []float64
		unit   string
		want   string
		known  bool
	}{
		{name: "percent median odd", values: []float64{0.1, 0.5, 0.3}, unit: usageUnitPercent, want: "0.3% of the quota window per task", known: true},
		{name: "usd median even", values: []float64{2, 4}, unit: usageUnitUSD, want: "$3 per task", known: true},
		{name: "unknown unit", values: []float64{1}, unit: "credits", want: "", known: false},
		{name: "no values", values: nil, unit: usageUnitPercent, want: "", known: false},
	} {
		got, known := medianUsage(tc.values, tc.unit)
		if known != tc.known || got != tc.want {
			t.Fatalf("%s: medianUsage = (%q, %v), want (%q, %v)", tc.name, got, known, tc.want, tc.known)
		}
	}
	// The median window is the last 20 values, not the whole history.
	long := make([]float64, 25)
	for i := range long {
		long[i] = float64(i + 1)
	}
	got, known := medianUsage(long, usageUnitPercent)
	if !known || got != "15.5% of the quota window per task" {
		t.Fatalf("window median = (%q, %v), want 15.5%%", got, known)
	}
}

// TestTeamProposeToolOfferingFollowsSessionTeamsFlag pins Q-105 on the tool
// surface: the flag gates tools/list and the call path.
func TestTeamProposeToolOfferingFollowsSessionTeamsFlag(t *testing.T) {
	t.Parallel()
	enabled := newTeamProposalFixture(t, "")
	names := teamToolNames(t, enabled.tools, enabled.binding)
	if !slices.Contains(names, teamProposeToolName) {
		t.Fatalf("tools/list = %v, want fugue_team_propose offered", names)
	}
	// The tool path proposes end to end: policy, run, store, item, card.
	proposalID, proposeErr := enabled.proposeThroughTool(t, validTeamProposalArgs())
	if proposeErr != nil {
		t.Fatalf("tool propose: %v", proposeErr)
	}
	if rec, _ := enabled.teamSnapshot(t, proposalID); rec.State != vendorstore.TeamStateProposed {
		t.Fatalf("tool-proposed team state = %s, want proposed", rec.State)
	}
	assertSingleProposalCard(t, enabled)
	disabled := newTeamProposalFixture(t, "[session]\nteams = false\n")
	names = teamToolNames(t, disabled.tools, disabled.binding)
	if slices.Contains(names, teamProposeToolName) {
		t.Fatalf("tools/list = %v, want fugue_team_propose hidden", names)
	}
	raw, err := json.Marshal(validTeamProposalArgs())
	if err != nil {
		t.Fatal(err)
	}
	if _, callErr := disabled.tools.Call(t.Context(), disabled.binding, teamProposeToolName, raw); errcode.CodeOf(callErr) != errcode.NotFound {
		t.Fatalf("disabled call error = %v, want not found", callErr)
	}
}

func teamToolNames(t *testing.T, tools *SessionToolService, binding SessionBinding) []string {
	t.Helper()
	views, err := tools.List(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(views))
	for i := range views {
		names = append(names, views[i].Name)
	}
	return names
}

// awaitCancelledProposal polls until the session-end cancel has landed: the
// team cancelled, every task cancelled and the item rejected. The cancel runs
// off the endSession hook on its own goroutine.
func awaitCancelledProposal(t *testing.T, f *teamProposalFixture, teamID string) (vendorstore.TeamRecord, []vendorstore.TaskRecord, vendorstore.StoredItem) {
	t.Helper()
	var rec vendorstore.TeamRecord
	var tasks []vendorstore.TaskRecord
	var item vendorstore.StoredItem
	for range 20000 {
		rec, tasks = f.teamSnapshot(t, teamID)
		if rec.State == vendorstore.TeamStateCancelled {
			item = f.proposalItem(t, teamID)
			if item.Metadata.Decision == teamTestRejected {
				return rec, tasks, item
			}
		}
		runtime.Gosched()
	}
	return rec, tasks, item
}

// TestConcurrentProposalsLeaveOnePending pins Q-103 under concurrency: N
// parallel proposes from one session are serialized by the service, so
// exactly one team stays proposed, every earlier one is cancelled, and
// exactly one card is live.
func TestConcurrentProposalsLeaveOnePending(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	const proposals = 6
	ids := make([]string, proposals)
	var wg sync.WaitGroup
	for i := range proposals {
		wg.Add(1)
		supervise.Go(t.Context(), "test.concurrent-propose", func(ctx context.Context) error {
			defer wg.Done()
			id, err := f.rt.teamProposals.Propose(ctx, f.session.ID, validTeamProposalArgs())
			if err != nil {
				t.Errorf("Propose %d: %v", i, err)
				return nil
			}
			ids[i] = id
			return nil
		})
	}
	wg.Wait()
	distinct := make(map[string]struct{}, proposals)
	for i := range ids {
		if ids[i] == "" {
			t.Fatal("a concurrent propose failed")
		}
		distinct[ids[i]] = struct{}{}
	}
	if len(distinct) != proposals {
		t.Fatalf("distinct proposal IDs = %d, want %d", len(distinct), proposals)
	}
	pending := 0
	for id := range distinct {
		rec, _ := f.teamSnapshot(t, id)
		switch rec.State {
		case vendorstore.TeamStateProposed:
			pending++
		case vendorstore.TeamStateCancelled:
		default:
			t.Fatalf("proposal %s state = %s, want proposed or cancelled", id, rec.State)
		}
	}
	if pending != 1 {
		t.Fatalf("proposed teams = %d, want exactly one (Q-103)", pending)
	}
	if _, ok, err := f.hp.vendorStore.PendingTeamForSession(t.Context(), f.session.ID); err != nil || !ok {
		t.Fatalf("no pending proposal left = (%v, %v), want one", ok, err)
	}
	assertSingleProposalCard(t, f)
}

// TestProposalRacingSessionEndNeverStaysPending pins Q-104 under the race:
// propose and session close run concurrently, and afterwards the session has
// no pending proposal — a refused propose never created one, and a created
// one was cancelled by the session-end cancel.
func TestProposalRacingSessionEndNeverStaysPending(t *testing.T) {
	t.Parallel()
	for round := range 4 {
		f := newTeamProposalFixture(t, "")
		sub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var proposalID string
		var proposeErr error
		wg.Add(2)
		supervise.Go(t.Context(), "test.race-propose", func(ctx context.Context) error {
			defer wg.Done()
			proposalID, proposeErr = f.rt.teamProposals.Propose(ctx, f.session.ID, validTeamProposalArgs())
			return nil
		})
		supervise.Go(t.Context(), "test.race-close", func(ctx context.Context) error {
			defer wg.Done()
			if _, closeErr := f.svc.Close(ctx, testAppPrincipal, VendorStop{CommandID: "team-race-close", SessionID: f.session.ID}); closeErr != nil {
				t.Errorf("round %d Close: %v", round, closeErr)
			}
			return nil
		})
		wg.Wait()
		awaitSessionState(t, sub, f.session.ID, "closed")
		sub.Close()
		if proposalID != "" {
			rec, _, item := awaitCancelledProposal(t, f, proposalID)
			if rec.State != vendorstore.TeamStateCancelled {
				t.Fatalf("round %d: proposal %s state = %s, want cancelled", round, proposalID, rec.State)
			}
			if item.Metadata.Decision != teamTestRejected {
				t.Fatalf("round %d: item decision = %s, want rejected", round, item.Metadata.Decision)
			}
		} else if proposeErr != nil && errcode.CodeOf(proposeErr) != errcode.PermissionDenied {
			t.Fatalf("round %d: propose error = %v, want permission denied", round, proposeErr)
		}
		for range 20000 {
			if _, ok, _ := f.hp.vendorStore.PendingTeamForSession(t.Context(), f.session.ID); !ok {
				break
			}
			runtime.Gosched()
		}
		if _, ok, err := f.hp.vendorStore.PendingTeamForSession(t.Context(), f.session.ID); err != nil || ok {
			t.Fatalf("round %d: pending proposal survived its session = (%v, %v)", round, ok, err)
		}
	}
}

// TestProposalStillFlipsAfterMoreThanAThousandItems pins the indexed item
// lookup (F11.2a review): a proposal followed by more than 1,000 other items
// is still flipped to rejected, on supersede and on session end.
func TestProposalStillFlipsAfterMoreThanAThousandItems(t *testing.T) {
	t.Parallel()
	f := newTeamProposalFixture(t, "")
	first, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose 1: %v", err)
	}
	appendNoticeItems(t, f, 1001)
	second, err := f.rt.teamProposals.Propose(t.Context(), f.session.ID, validTeamProposalArgs())
	if err != nil {
		t.Fatalf("Propose 2: %v", err)
	}
	if item := f.proposalItem(t, first); item.Metadata.Decision != teamTestRejected {
		t.Fatalf("superseded item decision after deep transcript = %s, want rejected", item.Metadata.Decision)
	}
	appendNoticeItems(t, f, 1001)
	sub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if _, closeErr := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "team-deep-close", SessionID: f.session.ID}); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	awaitSessionState(t, sub, f.session.ID, "closed")
	rec, _, item := awaitCancelledProposal(t, f, second)
	if rec.State != vendorstore.TeamStateCancelled || item.Metadata.Decision != teamTestRejected {
		t.Fatalf("session-end cancel after deep transcript = (%s, %s), want cancelled/rejected", rec.State, item.Metadata.Decision)
	}
}

// appendNoticeItems records n harmless notice items through the session's
// actor, the way vendor observations do.
func appendNoticeItems(t *testing.T, f *teamProposalFixture, n int) {
	t.Helper()
	turnID, err := f.rt.teamProposals.recorder.ActiveTurnID(t.Context(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		itemID, idErr := f.rt.teamProposals.ids.NewID()
		if idErr != nil {
			t.Fatal(idErr)
		}
		recordErr := f.rt.teamProposals.recorder.RecordEvent(t.Context(), f.session.ID, &vendorstore.Event{
			Type: vendorstore.TranscriptItemRecorded, TurnID: turnID,
			Item: &vendorstore.Item{
				ID: itemID, TurnID: turnID, Kind: vendorstore.KindNotice,
				Metadata: vendorstore.ItemMetadata{Level: "info", Code: "team_test_note"},
			},
		})
		if recordErr != nil {
			t.Fatalf("append notice item %d: %v", i, recordErr)
		}
	}
}
