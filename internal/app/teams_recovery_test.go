package app

import (
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func TestHostStartRecoversRunningTeamTasksAsUnknown(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	clk := clock.NewFake(time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC))

	setupTeamForHostRestart(t, dir, clk)

	// Simulate host restart with vault initially locked.
	clk.Advance(time.Minute)
	hp2, err := StartHost(ctx, testHostDeps(dir, clk, newScriptedTransport()))
	if err != nil {
		t.Fatalf("StartHost 2: %v", err)
	}
	defer func() {
		if closeErr := hp2.Close(); closeErr != nil {
			t.Errorf("Close host 2: %v", closeErr)
		}
	}()

	_, tasks, err := hp2.vendorStore.TeamSnapshot(ctx, "team-host-restart")
	if err != nil {
		t.Fatalf("TeamSnapshot after host restart: %v", err)
	}
	if len(tasks) != 1 || tasks[0].State != vendorstore.TaskStateUnknown {
		t.Fatalf("expected running task marked unknown after host start, got %+v", tasks)
	}
}

const testHostLeadTaskID = "task-host-lead"

func setupTeamForHostRestart(t *testing.T, dir string, clk *clock.Fake) {
	t.Helper()
	ctx := t.Context()
	hp1, err := StartHost(ctx, testHostDeps(dir, clk, newScriptedTransport()))
	if err != nil {
		t.Fatalf("StartHost 1: %v", err)
	}
	if _, setupErr := hp1.vault.Setup(ctx, redact.NewSecret("correct-horse-battery-staple")); setupErr != nil {
		t.Fatalf("vault.Setup: %v", setupErr)
	}
	rec, _, err := hp1.vendorStore.CreateTeam(ctx, &vendorstore.NewTeamInput{
		TeamID:          "team-host-restart",
		WorkspaceID:     "ws-host-restart",
		ProposedEventID: "ev-p",
		Tasks: []vendorstore.NewTaskInput{
			{
				TaskID:         testHostLeadTaskID,
				HierarchyRole:  vendorstore.RoleLead,
				CatalogRole:    "researcher",
				Deliverable:    vendorstore.DeliverableReport,
				CreatedEventID: "ev-t1",
				ProtectedSpec:  []byte(`{"goal":"check host restart"}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	seq := rec.LastSeq
	for _, ev := range []vendorstore.AppendTeamEventInput{
		{EventID: "ev-start-team", EventType: vendorstore.EventTeamStarted},
		{EventID: "ev-assign", TaskID: testHostLeadTaskID, EventType: vendorstore.EventTaskAssigned},
		{EventID: "ev-start", TaskID: testHostLeadTaskID, EventType: vendorstore.EventTaskStarted},
	} {
		evCopy := ev
		seq, err = hp1.vendorStore.AppendTeamEvent(ctx, "team-host-restart", seq, &evCopy)
		if err != nil {
			t.Fatalf("AppendTeamEvent(%s): %v", ev.EventType, err)
		}
	}
	if closeErr := hp1.Close(); closeErr != nil {
		t.Fatalf("Close host 1: %v", closeErr)
	}
}
