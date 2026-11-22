package vendorstore

import (
	"testing"
	"time"
)

// TestEndedTeamsListsOnlyTerminalTeamsWithEndedAt pins that EndedTeams
// returns only terminal teams (done, cancelled, failed) ordered by ended_at
// and team_id, ignoring active/proposed teams, and succeeds while locked.
func TestEndedTeamsListsOnlyTerminalTeamsWithEndedAt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, v, _ := protectedStore(t)

	// Active team: must not appear in EndedTeams.
	if _, _, err := s.CreateTeam(ctx, &NewTeamInput{
		TeamID: "team-active", WorkspaceID: testWorkspaceID, SessionID: "sess-a",
		ProposedEventID: "ev-a1", ProposedPayload: []byte("{}"),
		Tasks: []NewTaskInput{{
			TaskID: "ta-1", HierarchyRole: RoleLead, CatalogRole: "lead",
			Deliverable: DeliverableReport, CreatedEventID: "tc-a1", ProtectedSpec: []byte("{}"),
		}},
	}); err != nil {
		t.Fatal(err)
	}

	// Create and settle three terminal teams at distinct ended_at times.
	createEnded := func(teamID string, endedAt time.Time, state TeamState) {
		t.Helper()
		if _, _, err := s.CreateTeam(ctx, &NewTeamInput{
			TeamID: teamID, WorkspaceID: testWorkspaceID, SessionID: "sess-" + teamID,
			ProposedEventID: "ev-p-" + teamID, ProposedPayload: []byte("{}"),
			Tasks: []NewTaskInput{{
				TaskID: "t-" + teamID, HierarchyRole: RoleLead, CatalogRole: "lead",
				Deliverable: DeliverableReport, CreatedEventID: "tc-" + teamID, ProtectedSpec: []byte("{}"),
			}},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE team_records SET state = ?, ended_at = ? WHERE team_id = ?`,
			string(state), endedAt.UnixMilli(), teamID,
		); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	createEnded("team-done-late", base.Add(2*time.Hour), TeamStateDone)
	createEnded("team-cancel-early", base, TeamStateCancelled)
	createEnded("team-fail-mid", base.Add(time.Hour), TeamStateFailed)

	// Lock the vault: metadata-only EndedTeams must still succeed (INV-09).
	v.Lock()

	got, err := s.EndedTeams(ctx)
	if err != nil {
		t.Fatalf("EndedTeams(locked) = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("EndedTeams count = %d, want 3 (%+v)", len(got), got)
	}
	wantOrder := []string{"team-cancel-early", "team-fail-mid", "team-done-late"}
	for i, wantID := range wantOrder {
		if got[i].TeamID != wantID {
			t.Fatalf("EndedTeams[%d].TeamID = %q, want %q", i, got[i].TeamID, wantID)
		}
	}
}
