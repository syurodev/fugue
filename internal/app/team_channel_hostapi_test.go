package app

// F11.10b HA-E08 evidence (host-api v0.17 §5.3): PageChannel pages both ways
// in `#n` order by thread and general channel; the vault locking refuses
// PageChannel and leaves the `team:<id>` topic metadata only; command_id
// dedup never posts twice. The session-principal denial runs over the real
// transport in internal/hostapi (TestChannelHostAPIDeniesSessionPrincipal);
// this file drives the service and the topic through the runtime, so the
// seeding can use the team store directly.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/syurodev/fugue/internal/vendorstore"
)

// seedChannelHostTeam stores an active team with a lead whose task runs in
// the given session.
func seedChannelHostTeam(t *testing.T, store *vendorstore.Store, teamID, leadSession string) {
	t.Helper()
	spec := []byte(`{"goal":"channel e2e"}`)
	rec, _, err := store.CreateTeam(t.Context(), &vendorstore.NewTeamInput{
		TeamID: teamID, WorkspaceID: "ws-chan", ProposedEventID: "p-" + teamID,
		ProposedPayload: []byte(`{"title":"channel","brief":"E2E brief."}`),
		Tasks: []vendorstore.NewTaskInput{
			{TaskID: teamID + "-lead", HierarchyRole: vendorstore.RoleLead, CatalogRole: "researcher", Deliverable: vendorstore.DeliverableReport, CreatedEventID: "c-" + teamID, ProtectedSpec: spec},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	seq := rec.LastSeq
	for _, ev := range []vendorstore.AppendTeamEventInput{
		{EventID: "start-" + teamID, EventType: vendorstore.EventTeamStarted},
		{EventID: "as-" + teamID, TaskID: teamID + "-lead", EventType: vendorstore.EventTaskAssigned, SessionID: leadSession},
		{EventID: "st-" + teamID, TaskID: teamID + "-lead", EventType: vendorstore.EventTaskStarted},
	} {
		next, appendErr := store.AppendTeamEvent(t.Context(), teamID, seq, &ev)
		if appendErr != nil {
			t.Fatalf("AppendTeamEvent(%s): %v", ev.EventID, appendErr)
		}
		seq = next
	}
}

func TestChannelHostAPIPagesPostsAndLocksMetadataOnly(t *testing.T) {
	t.Parallel()
	f := newChannelFixture(t)
	ctx := t.Context()
	teamID := f.teamID
	svc := f.rt.tasksService
	if svc == nil {
		t.Fatal("the tasks service is not wired")
	}
	f.rt.RegisterTeamChannel(teamID)

	// Subscribe before the posts: the snapshot carries the seeded lines with
	// their content, and a live event follows the post.
	sub, err := f.rt.hub.Subscribe(ctx, SubscribeParams{Principal: testAppPrincipal, Topic: TeamChannelTopicName(teamID)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	snapState, _, ok := sub.Snapshot()
	if !ok {
		t.Fatal("no topic snapshot")
	}
	snapLines := channelWireLines(t, snapState)
	if len(snapLines) == 0 || snapLines[len(snapLines)-1].Content == "" {
		t.Fatalf("unlocked snapshot lines = %+v, want content on the oldest line", snapLines)
	}

	// Post twice with the same command_id: the second returns the same `#n`
	// and no second line is recorded.
	first, postErr := svc.PostChannelMessage(ctx, PostChannelCommand{
		CommandID: "post-1", TeamID: teamID, ToTaskID: f.memAID, Text: "hello team",
	})
	if postErr != nil {
		t.Fatal(postErr)
	}
	second, postErr := svc.PostChannelMessage(ctx, PostChannelCommand{
		CommandID: "post-1", TeamID: teamID, ToTaskID: f.memAID, Text: "hello team",
	})
	if postErr != nil {
		t.Fatal(postErr)
	}
	if first.ChannelSeq != second.ChannelSeq {
		t.Fatalf("replayed command_id posted again: %d then %d", first.ChannelSeq, second.ChannelSeq)
	}
	liveLines := channelEventLines(t, sub, first.ChannelSeq)
	if len(liveLines) != 1 || liveLines[0].Content != "hello team" {
		t.Fatalf("live event lines = %+v, want the posted line with content", liveLines)
	}

	// PageChannel pages both ways: the general channel (no thread) holds the
	// plan, decision and the posted user line; the thread holds the task
	// assignment. Newest first, before and since as exclusive cursors.
	general, pageErr := svc.PageChannel(ctx, teamID, "", 0, 0, 0)
	if pageErr != nil {
		t.Fatal(pageErr)
	}
	if len(general) < 3 {
		t.Fatalf("general page = %d lines, want the plan, decision and post", len(general))
	}
	if general[0].Content != "hello team" || general[0].Attempt != 1 {
		t.Fatalf("newest general line = %+v, want the post at attempt 1", general[0])
	}
	thread, pageErr := svc.PageChannel(ctx, teamID, f.memAID, 0, int64(general[0].ChannelSeq), 0)
	if pageErr != nil {
		t.Fatal(pageErr)
	}
	if len(thread) != 0 {
		t.Fatalf("thread page above the newest general line = %+v, want none", thread)
	}
	forward, pageErr := svc.PageChannel(ctx, teamID, "", 0, 2, 0)
	if pageErr != nil {
		t.Fatal(pageErr)
	}
	if len(forward) < 2 || forward[0].ChannelSeq <= 2 {
		t.Fatalf("forward page = %+v, want lines above #2 newest first", forward)
	}
	back, pageErr := svc.PageChannel(ctx, teamID, "", int64(forward[len(forward)-1].ChannelSeq)+0, 0, 0)
	if pageErr != nil {
		t.Fatal(pageErr)
	}
	if len(back) == 0 || back[len(back)-1].ChannelSeq != 1 {
		t.Fatalf("backward page ends at %d, want line #1", back[len(back)-1].ChannelSeq)
	}

	// The vault locks: PageChannel refuses, the fresh topic snapshot carries
	// metadata only, and a post fails closed.
	f.hp.Vault().Lock()
	if _, lockedErr := svc.PageChannel(ctx, teamID, "", 0, 0, 0); lockedErr == nil {
		t.Fatal("locked PageChannel succeeded, want fail closed")
	}
	if _, lockedPostErr := svc.PostChannelMessage(ctx, PostChannelCommand{
		CommandID: "post-locked", TeamID: teamID, ToTaskID: f.memAID, Text: "while locked",
	}); lockedPostErr == nil {
		t.Fatal("a post on a locked vault succeeded, want fail closed")
	}
	lockedSub, err := f.rt.hub.Subscribe(ctx, SubscribeParams{Principal: testAppPrincipal, Topic: TeamChannelTopicName(teamID)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lockedSub.Close)
	lockedState, _, ok := lockedSub.Snapshot()
	if !ok {
		t.Fatal("no locked snapshot")
	}
	lockedLines := channelWireLines(t, lockedState)
	if len(lockedLines) == 0 {
		t.Fatal("locked snapshot empty, want the metadata of every line")
	}
	for i := range lockedLines {
		if lockedLines[i].Content != "" {
			t.Fatalf("locked snapshot line #%d carries content %q, want metadata only", lockedLines[i].ChannelSeq, lockedLines[i].Content)
		}
	}
}

// channelWireLines decodes a snapshot payload's lines.
func channelWireLines(t *testing.T, payload []byte) []channelLineWire {
	t.Helper()
	var wire struct {
		Lines []channelLineWire `json:"lines"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Lines
}

// channelEventLines waits for the topic events of one source line.
func channelEventLines(t *testing.T, sub *Subscription, seq int64) []channelLineWire {
	t.Helper()
	for {
		ev, ok := sub.Next(context.Background())
		if !ok {
			t.Fatal("the topic stream ended before the expected event")
		}
		if ev.Type != "channel.appended" || ev.Seq != uint64(seq) {
			continue
		}
		return []channelLineWire{{ChannelSeq: int64(ev.Seq), Content: wireContentOf(ev.Payload)}}
	}
}

// wireContentOf extracts the content field of one topic payload.
func wireContentOf(payload []byte) string {
	var wire struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(payload, &wire); err != nil {
		return ""
	}
	return wire.Content
}
