package vendorstore

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/vault"
)

const testListTeamTwoID = "team-2"

func TestListTeamsFiltersCountsAndLockedVault(t *testing.T) {
	t.Parallel()
	s := seedListTeamsFixture(t)

	raw, found, err := s.OpenTeamProposedPayload(t.Context(), "team-1")
	requireNoError(t, err)
	if !found || len(raw) == 0 {
		t.Fatalf("expected TeamProposed payload, got found=%v len=%d", found, len(raw))
	}

	s.vault.Lock()
	assertListTeamsLocked(t, s)
}

func seedListTeamsFixture(t *testing.T) *Store {
	t.Helper()
	ctx := t.Context()
	s, clk := timedStore(t)

	seq1 := seedChannelTeam(t, s, "team-1", map[string]bool{
		"team-1-lead": true,
		"team-1-mgr":  true,
		"team-1-a":    true,
		"team-1-b":    true,
	})
	_, err := s.AppendTeamEvent(ctx, "team-1", seq1, &AppendTeamEventInput{
		EventID: "ask-a", TaskID: "team-1-a", EventType: EventTaskQuestionAsked,
		ProtectedPayload: []byte(`{"question":"need help","to":"user"}`),
	})
	requireNoError(t, err)
	_, err = s.AppendTeamEvent(ctx, "team-1", seq1+1, &AppendTeamEventInput{
		EventID: "fail-b", TaskID: "team-1-b", EventType: EventTaskEnded,
		Outcome: TaskOutcomeFailed, FailureCode: "err_boom",
	})
	requireNoError(t, err)
	_, err = s.AppendTeamEvent(ctx, "team-1", seq1+2, &AppendTeamEventInput{
		EventID: "stop-lead", TaskID: "team-1-lead", EventType: EventTaskEnded,
		Outcome: outcomeStopped,
	})
	requireNoError(t, err)
	_, err = s.db.ExecContext(ctx, `UPDATE team_records SET session_id = 'sess-1' WHERE team_id = 'team-1'`)
	requireNoError(t, err)

	clk.Advance(1000)
	_, _, err = s.CreateTeam(ctx, &NewTeamInput{
		TeamID: testListTeamTwoID, WorkspaceID: "ws-2", SessionID: "sess-2", ProposedEventID: "p-team-2",
		ProposedPayload: []byte(`{"title":"team two","brief":"second team"}`),
		Tasks: []NewTaskInput{
			{TaskID: "team-2-lead", HierarchyRole: RoleLead, CatalogRole: testRoleResearch, Deliverable: DeliverableReport, CreatedEventID: "c-lead-2", ProtectedSpec: []byte(`{"goal":"lead 2"}`)},
		},
	})
	requireNoError(t, err)
	return s
}

func assertListTeamsLocked(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	if _, _, lockedErr := s.OpenTeamProposedPayload(ctx, "team-1"); !errors.Is(lockedErr, vault.ErrLocked) {
		t.Fatalf("expected vault.ErrLocked, got %v", lockedErr)
	}
	assertListTeamsCountsAndSeqs(t, s)
	assertListTeamsFilters(t, s)
}

func assertListTeamsCountsAndSeqs(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	all, err := s.ListTeams(ctx, "", "")
	requireNoError(t, err)
	if len(all) != 2 || all[0].TeamID != testListTeamTwoID || all[1].TeamID != "team-1" {
		t.Fatalf("ListTeams() = %+v, want [team-2, team-1]", all)
	}
	t1 := all[1]
	if t1.TaskCount != 4 || t1.RunningCount != 1 || t1.AskingCount != 1 || t1.FailedCount != 1 {
		t.Fatalf("team-1 counts = (%d, %d, %d, %d), want (4, 1, 1, 1)", t1.TaskCount, t1.RunningCount, t1.AskingCount, t1.FailedCount)
	}
	seqs, err := s.TeamTaskLastChannelSeqs(ctx, "team-1")
	requireNoError(t, err)
	if seqs["team-1-a"] == 0 || seqs["team-1-b"] == 0 {
		t.Fatalf("expected non-zero channel seqs for team-1 tasks, got %+v", seqs)
	}
}

func assertListTeamsFilters(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	byWS, err := s.ListTeams(ctx, testWorkspaceID, "")
	requireNoError(t, err)
	if len(byWS) != 1 || byWS[0].TeamID != "team-1" {
		t.Fatalf("ListTeams(ws) = %+v, want [team-1]", byWS)
	}
	bySess, err := s.ListTeams(ctx, "", "sess-2")
	requireNoError(t, err)
	if len(bySess) != 1 || bySess[0].TeamID != testListTeamTwoID {
		t.Fatalf("ListTeams(sess-2) = %+v, want [team-2]", bySess)
	}
}
