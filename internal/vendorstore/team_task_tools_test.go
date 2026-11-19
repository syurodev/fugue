package vendorstore

import (
	"errors"
	"testing"
)

func TestTeamNotesReadAndCount(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := seedMailboxTeam(t, s)
	ctx := t.Context()

	snap, _, err := s.TeamSnapshot(ctx, rec.TeamID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendTeamEvent(ctx, rec.TeamID, snap.LastSeq, &AppendTeamEventInput{
		EventID: "ev-note-1", EventType: EventTeamNoted, ProtectedPayload: []byte(`{"note":"n1"}`),
	}); err != nil {
		t.Fatalf("AppendTeamEvent(TeamNoted): %v", err)
	}
	if count, countErr := s.CountTeamNotes(ctx, rec.TeamID); countErr != nil || count != 1 {
		t.Fatalf("CountTeamNotes = (%d, %v), want (1, nil)", count, countErr)
	}
	notes, total, err := s.OpenTeamNotePayloads(ctx, rec.TeamID, 0, 10)
	if err != nil || total != 1 || len(notes) != 1 || string(notes[0]) != `{"note":"n1"}` {
		t.Fatalf("OpenTeamNotePayloads = (%q, %d, %v)", notes, total, err)
	}
	if empty, emptyTotal, emptyErr := s.OpenTeamNotePayloads(ctx, rec.TeamID, 5, 10); emptyErr != nil || emptyTotal != 1 || len(empty) != 0 {
		t.Fatalf("OpenTeamNotePayloads(offset=5) = (%d, %d, %v)", len(empty), emptyTotal, emptyErr)
	}
}

func TestTaskNoReportNudge(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := seedMailboxTeam(t, s)
	ctx := t.Context()

	first, err := s.MarkTaskNoReportNudged(ctx, rec.TeamID, decideTestLeadTask)
	if err != nil || !first {
		t.Fatalf("first MarkTaskNoReportNudged = (%v, %v), want (true, nil)", first, err)
	}
	if second, err2 := s.MarkTaskNoReportNudged(ctx, rec.TeamID, decideTestLeadTask); err2 != nil || second {
		t.Fatalf("second MarkTaskNoReportNudged = (%v, %v), want (false, nil)", second, err2)
	}
}

func TestTaskLatestReport(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := seedMailboxTeam(t, s)
	ctx := t.Context()

	if _, found, openErr := s.OpenLatestTaskEventPayload(ctx, rec.TeamID, decideTestLeadTask, EventTaskReported); openErr != nil || found {
		t.Fatalf("OpenLatestTaskEventPayload(absent) = (%v, %v)", found, openErr)
	}
	snap, _, snapErr := s.TeamSnapshot(ctx, rec.TeamID)
	if snapErr != nil {
		t.Fatal(snapErr)
	}
	seq, err := s.AppendTeamEvent(ctx, rec.TeamID, snap.LastSeq, &AppendTeamEventInput{
		EventID: "ev-start-1", TaskID: decideTestLeadTask, EventType: EventTaskStarted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendTeamEvent(ctx, rec.TeamID, seq, &AppendTeamEventInput{
		EventID: "ev-rep-1", TaskID: decideTestLeadTask, EventType: EventTaskReported, ProtectedPayload: []byte(`{"summary":"ok"}`),
	}); err != nil {
		t.Fatal(err)
	}
	rep, found, err := s.OpenLatestTaskEventPayload(ctx, rec.TeamID, decideTestLeadTask, EventTaskReported)
	if err != nil || !found || string(rep) != `{"summary":"ok"}` {
		t.Fatalf("OpenLatestTaskEventPayload = (%q, %v, %v)", rep, found, err)
	}
}

func TestEnqueueDirectMailbox(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	rec := seedMailboxTeam(t, s)
	ctx := t.Context()

	if err := s.EnqueueDirectMailbox(ctx, rec.TeamID, "", MailboxKindSend, "u1", []byte("x")); !errors.Is(err, ErrTeamInvalidInput) {
		t.Fatalf("EnqueueDirectMailbox(empty recipient) = %v, want ErrTeamInvalidInput", err)
	}
	if err := s.EnqueueDirectMailbox(ctx, "no-team", decideTestLeadTask, MailboxKindSend, "u1", []byte("x")); !errors.Is(err, ErrTeamNotFound) {
		t.Fatalf("EnqueueDirectMailbox(no-team) = %v, want ErrTeamNotFound", err)
	}
	if err := s.EnqueueDirectMailbox(ctx, rec.TeamID, decideTestLeadTask, MailboxKindSend, "u1", []byte(`{"summary":"hi"}`)); err != nil {
		t.Fatalf("EnqueueDirectMailbox: %v", err)
	}
}
