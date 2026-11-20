package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

type fakeHostResources struct {
	ramBytes uint64
	cores    int
	memErr   error
}

func (f fakeHostResources) TotalMemoryBytes() (uint64, error) {
	if f.memErr != nil {
		return 0, f.memErr
	}
	return f.ramBytes, nil
}

func (f fakeHostResources) CPUCores() int { return f.cores }

type cpuWeightCaptureAdapter struct {
	actorTestAdapter
	mu      sync.Mutex
	weights map[string]int
}

func (a *cpuWeightCaptureAdapter) Start(_ context.Context, spec *vendor.StartSpec) (vendor.Session, error) {
	a.mu.Lock()
	if a.weights == nil {
		a.weights = make(map[string]int)
	}
	a.weights[spec.SessionID] = spec.CPUWeight
	a.mu.Unlock()
	return newActorTestSession(), nil
}

func TestComputeSchedulerTiersFormulaAndOverrides(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ram  uint64
		core int
		cfg  workspace.HostConfig
		want SchedulerTiers
	}{
		{"small ram floors at 1", 2 << 30, 2, workspace.HostConfig{}, SchedulerTiers{PerAccount: 6, MachineWide: 1, HeavyCommands: 1}},
		{"4.8 GiB gives 2", 4<<30 + 800<<20, 4, workspace.HostConfig{}, SchedulerTiers{PerAccount: 6, MachineWide: 2, HeavyCommands: 1}},
		{"16 GiB caps at 8", 16 << 30, 8, workspace.HostConfig{}, SchedulerTiers{PerAccount: 6, MachineWide: 8, HeavyCommands: 2}},
		{"host overrides win", 16 << 30, 8, workspace.HostConfig{PerAccountSessions: 3, MaxSessions: 4, HeavyCommands: 5}, SchedulerTiers{PerAccount: 3, MachineWide: 4, HeavyCommands: 5}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ComputeSchedulerTiers(fakeHostResources{ramBytes: tc.ram, cores: tc.core}, &tc.cfg)
			if err != nil {
				t.Fatalf("ComputeSchedulerTiers: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestVendorSessionCapComesFromTheSchedulerTiers(t *testing.T) {
	t.Parallel()
	t.Run("machine cap from RAM", testMachineCapFromRAM)
	t.Run("per account 6 and machine 8", testPerAccountAndMachineCap)
	t.Run("tier 2 host config override on StartHost", testTier2HostConfigOverride)
}

func newEmptySessionsFixture(t *testing.T) *transcriptFixture {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	if migErr := storage.Migrate(t.Context(), db, clk); migErr != nil {
		t.Fatal(migErr)
	}
	v := cheapvault.Unlocked(t)
	s, err := vendorstore.New(t.Context(), db, v, clk, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	hub := NewEventHub(testIncarn)
	service, err := NewTranscripts(t.Context(), TranscriptDeps{Store: s, Hub: hub, Vault: v, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := service.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	return &transcriptFixture{t: t, store: s, vault: v, hub: hub, clock: clk, service: service}
}

func testMachineCapFromRAM(t *testing.T) {
	t.Parallel()
	f := newEmptySessionsFixture(t)
	// 4 GiB + 3 * 400 MiB -> machine cap 3
	m := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: newActorTestSession()}, Vault: f.vault,
		HostResources: fakeHostResources{ramBytes: 4<<30 + 3*(400<<20), cores: 4},
		Clock:         f.clock, IDs: ids.NewSequence("tier-"),
	})
	for i := range 3 {
		id := fmt.Sprintf("ram-%d", i)
		if err := m.Start(t.Context(), &vendorstore.Session{ID: id, Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: testActorRole}, &vendor.StartSpec{SessionID: id}); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}
	err := m.Start(t.Context(), &vendorstore.Session{ID: "ram-3", Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: testActorRole}, &vendor.StartSpec{SessionID: "ram-3"})
	if !errors.Is(err, ErrVendorSessionCapacity) {
		t.Fatalf("4th start err = %v, want ErrVendorSessionCapacity", err)
	}
	mustCloseActorSessions(t, m)
}

func testPerAccountAndMachineCap(t *testing.T) {
	t.Parallel()
	f := newEmptySessionsFixture(t)
	ad := actorTestAdapter{session: newActorTestSession()}
	m := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service,
		Adapters:      map[string]vendor.Adapter{VendorClaude: ad, VendorCodex: ad},
		Vault:         f.vault,
		HostResources: fakeHostResources{ramBytes: 16 << 30, cores: 8},
		Clock:         f.clock, IDs: ids.NewSequence("acct-"),
	})
	for i := range 6 {
		id := fmt.Sprintf("claude-%d", i)
		if err := m.Start(t.Context(), &vendorstore.Session{ID: id, Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: testActorRole}, &vendor.StartSpec{SessionID: id}); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}
	if err := m.Start(t.Context(), &vendorstore.Session{ID: "claude-6", Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: testActorRole}, &vendor.StartSpec{SessionID: "claude-6"}); !errors.Is(err, ErrVendorSessionCapacity) {
		t.Fatalf("7th claude start err = %v, want ErrVendorSessionCapacity", err)
	}
	for i := range 2 {
		id := fmt.Sprintf("codex-%d", i)
		if err := m.Start(t.Context(), &vendorstore.Session{ID: id, Vendor: VendorCodex, AccountID: vendorDefaultAccount, Role: testActorRole}, &vendor.StartSpec{SessionID: id}); err != nil {
			t.Fatalf("start %s: %v", id, err)
		}
	}
	if err := m.Start(t.Context(), &vendorstore.Session{ID: "codex-2", Vendor: VendorCodex, AccountID: vendorDefaultAccount, Role: testActorRole}, &vendor.StartSpec{SessionID: "codex-2"}); !errors.Is(err, ErrVendorSessionCapacity) {
		t.Fatalf("9th machine start err = %v, want ErrVendorSessionCapacity", err)
	}
	mustCloseActorSessions(t, m)
}

func testTier2HostConfigOverride(t *testing.T) {
	t.Parallel()
	f := newVendorServiceFixtureCustom(t, func(deps *HostDeps) {
		deps.HostResources = fakeHostResources{ramBytes: 16 << 30, cores: 8}
	})
	if err := os.MkdirAll(f.fugueHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.fugueHome, "config.toml"), []byte("[host]\nmax_sessions = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.start(t, "cfg-0")
	_, err := f.svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{CommandID: "cfg-1", Vendor: VendorClaude, WorkspaceID: f.ws})
	requireCode(t, err, errcode.VendorCapacity)
}

func TestTeamSubTasksGetLowerCPUWeight(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	ad := &cpuWeightCaptureAdapter{}
	m := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: ad, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("cpu-"),
	})
	svc := &VendorService{sessions: m, adapters: map[string]vendor.Adapter{VendorClaude: ad}, tokens: newSessionTokens(nil)}
	specSolo := &vendor.StartSpec{SessionID: "solo"}
	if err := svc.spawn(specSolo, vendorSessionRole, func() error { _, err := ad.Start(t.Context(), specSolo); return err }); err != nil {
		t.Fatal(err)
	}
	specTask := &vendor.StartSpec{SessionID: "member"}
	if err := svc.spawn(specTask, string(vendorstore.RoleMember), func() error { _, err := ad.Start(t.Context(), specTask); return err }); err != nil {
		t.Fatal(err)
	}
	if ad.weights["solo"] != 0 || ad.weights["member"] != teamTaskCPUWeight {
		t.Fatalf("weights = %+v, want solo=0 member=%d", ad.weights, teamTaskCPUWeight)
	}
}

func formatQueueSnapshot(snap []QueuedTaskPosition) []string {
	out := make([]string, 0, len(snap))
	for _, q := range snap {
		if q.Blocked {
			out = append(out, fmt.Sprintf("%s@blocked:%d", q.TaskID, q.QueuePosition))
		} else {
			out = append(out, fmt.Sprintf("%s@%d", q.TaskID, q.QueuePosition))
		}
	}
	return out
}

func assertQueueSnapshotOrder(t *testing.T, disp *teamDispatcher, want []string) {
	t.Helper()
	snap, err := disp.QueueSnapshot(t.Context())
	if err != nil {
		t.Fatalf("QueueSnapshot: %v", err)
	}
	if got := formatQueueSnapshot(snap); !slices.Equal(got, want) {
		t.Fatalf("queue order = %v, want %v", got, want)
	}
}

func TestQueueOrderPrioritizesManagersThenCreationOrder(t *testing.T) {
	t.Parallel()
	f := newDispatchFixture(t, true)
	team1 := f.approve(t, priorityProposalArgs("m1"), decisionApprove, "")
	f.clk.Advance(time.Second)
	team2 := f.approve(t, priorityProposalArgs("m2"), decisionApprove, "")

	// Before leads bind, only the two leads are startable; children are blocked with position 0.
	assertQueueSnapshotOrder(t, f.disp, []string{
		team1 + "." + teamTestLeadTask + "@1",
		team2 + "." + teamTestLeadTask + "@2",
		team1 + ".team-mgr@blocked:0",
		team2 + ".team-mgr@blocked:0",
		team1 + ".d1@blocked:0",
		team2 + ".d1@blocked:0",
		team1 + ".d2@blocked:0",
		team2 + ".d2@blocked:0",
	})

	// Bind leads while child capacity is full: managers and depth-1 members become startable
	// and get 1-based positions, while depth-2 members remain blocked behind their managers.
	f.sessions.mu.Lock()
	f.sessions.noRoom = true
	f.sessions.mu.Unlock()
	if err := f.disp.DispatchAll(t.Context()); err != nil {
		t.Fatalf("DispatchAll bind leads: %v", err)
	}
	assertQueueSnapshotOrder(t, f.disp, []string{
		team1 + ".team-mgr@1",
		team2 + ".team-mgr@2",
		team1 + ".d1@3",
		team2 + ".d1@4",
		team1 + ".d2@blocked:0",
		team2 + ".d2@blocked:0",
	})

	f.sessions.mu.Lock()
	f.sessions.noRoom = false
	f.sessions.mu.Unlock()
	if err := f.disp.DispatchAll(t.Context()); err != nil {
		t.Fatalf("DispatchAll: %v", err)
	}
	f.sessions.mu.Lock()
	started := make([]string, 0, len(f.sessions.starts))
	for i := range f.sessions.starts {
		started = append(started, f.sessions.starts[i].TaskID)
	}
	f.sessions.mu.Unlock()
	// Managers (tier 1) + depth-1 members (tier 2) start before depth-2 members (tier 3).
	wantStarted := []string{
		team1 + ".team-mgr",
		team2 + ".team-mgr",
		team1 + ".d1",
		team2 + ".d1",
		team1 + ".d2",
		team2 + ".d2",
	}
	if !slices.Equal(started, wantStarted) {
		t.Fatalf("started = %v, want %v", started, wantStarted)
	}
}

type stubAccountCapacity struct{}

func (stubAccountCapacity) HasCapacity(context.Context) (bool, error) { return true, nil }
func (stubAccountCapacity) HasCapacityFor(context.Context, string, string) (bool, error) {
	return true, nil
}

func TestDirectChildCapOfFourStillHoldsWithScheduler(t *testing.T) {
	t.Parallel()
	admit := newSchedulerAdmitter(stubAccountCapacity{}, nil, nil)
	tasks := make([]vendorstore.TaskRecord, 5)
	for i := range tasks {
		tasks[i] = vendorstore.TaskRecord{
			TeamID:        "t1",
			TaskID:        fmt.Sprintf("child-%d", i),
			ParentTaskID:  teamTestLeadTask,
			HierarchyRole: vendorstore.RoleMember,
			State:         vendorstore.TaskStateRunning,
		}
	}
	tasks[4].State = vendorstore.TaskStateQueued
	ok, err := admit.Admit(t.Context(), &tasks[4], tasks)
	if err != nil {
		t.Fatalf("Admit at 4 running children: %v", err)
	}
	if ok {
		t.Fatal("admitted 5th direct child while 4 direct children are running")
	}
	tasks[3].State = vendorstore.TaskStateDone
	ok, err = admit.Admit(t.Context(), &tasks[4], tasks)
	if err != nil || !ok {
		t.Fatalf("Admit at 3 running children = (%v, %v), want (true, nil)", ok, err)
	}
}

const testPriorityLeadGoal = "Coordinate priority tasks"

func priorityProposalArgs(prefix string) *ProposeArgs {
	args := validTeamProposalArgs()
	args.Brief = "Priority test " + prefix
	args.Tasks = []teams.ProposedTask{
		{ID: teamTestLeadTask, TeamRole: teams.RoleLead, Role: teamTestRole, Goal: testPriorityLeadGoal, WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "d1", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Depth 1", WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "team-mgr", ParentTaskID: teamTestLeadTask, TeamRole: teams.RoleTeamManager, Role: teamTestRole, Goal: "Manager", WriteScope: []string{}, Deliverable: teams.DeliverableReport},
		{ID: "d2", ParentTaskID: "team-mgr", TeamRole: teams.RoleMember, Role: teamTestRole, Goal: "Depth 2", WriteScope: []string{}, Deliverable: teams.DeliverableReport},
	}
	return args
}

func TestHeavySlotHoldLimitClosesLeaseAndGrantsWaiter(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
	var logBuf bytes.Buffer
	sem, spawn := newTestHoldSemaphore(t, clk, &logBuf, 60)

	rel1, done1, err := sem.Acquire(t.Context())
	if err != nil {
		t.Fatalf("Acquire 1: %v", err)
	}
	defer rel1()

	waiterGranted := make(chan func(), 1)
	spawn("test.heavy-waiter", func(ctx context.Context) error {
		rel2, _, acqErr := sem.Acquire(ctx)
		if acqErr == nil {
			waiterGranted <- rel2
		}
		return acqErr
	})

	clk.Advance(59 * time.Second)
	select {
	case <-done1:
		t.Fatal("lease closed before hold limit elapsed")
	case <-waiterGranted:
		t.Fatal("waiter granted before hold limit elapsed")
	default:
	}

	clk.Advance(2 * time.Second)
	select {
	case <-done1:
	case <-t.Context().Done():
		t.Fatal("timed out waiting for lease loss on hold limit")
	}
	select {
	case rel2 := <-waiterGranted:
		rel2()
	case <-t.Context().Done():
		t.Fatal("timed out waiting for waiter to receive released slot")
	}
	if logged := logBuf.String(); !strings.Contains(logged, "error_code="+string(errcode.DeadlineExceeded)) {
		t.Fatalf("expected deadline_exceeded in log, got %q", logged)
	}
}

func newTestHoldSemaphore(t *testing.T, clk clock.Clock, logBuf *bytes.Buffer, maxHoldSec int) (*heavySemaphore, func(string, func(context.Context) error) *supervise.Task) {
	t.Helper()
	var tasks []*supervise.Task
	var tasksMu sync.Mutex
	spawn := func(name string, fn func(context.Context) error) *supervise.Task {
		task := supervise.Go(t.Context(), name, fn)
		tasksMu.Lock()
		tasks = append(tasks, task)
		tasksMu.Unlock()
		return task
	}
	t.Cleanup(func() {
		tasksMu.Lock()
		all := slices.Clone(tasks)
		tasksMu.Unlock()
		for _, task := range all {
			if err := task.Wait(); err != nil {
				t.Errorf("wait task: %v", err)
			}
		}
	})
	sem := newHeavySemaphore(
		fakeHostResources{cores: 4},
		func(context.Context) (workspace.HostConfig, error) {
			return workspace.HostConfig{HeavyCommands: 1, HeavySlotMaxHoldSeconds: maxHoldSec}, nil
		},
		clk, slog.New(slog.NewTextHandler(logBuf, nil)), spawn,
	)
	t.Cleanup(sem.Close)
	return sem, spawn
}
