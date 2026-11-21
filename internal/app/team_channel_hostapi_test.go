package app

// F11.10b HA-E08 evidence (host-api v0.17 §5.3): PageChannel pages both ways
// in `#n` order by thread and general channel; the vault locking refuses
// PageChannel and leaves the `team:<id>` topic metadata only; command_id
// dedup never posts twice. The session-principal denial runs over the real
// transport in internal/hostapi (TestChannelHostAPIDeniesSessionPrincipal);
// this file drives the service and the topic through the runtime, so the
// seeding can use the team store directly.

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const testChannelHello = "hello team"

func mustChannelEventSeq(t *testing.T, seq int64) EventSeq {
	t.Helper()
	out, err := channelEventSeq(seq)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestChannelHostAPIPagesPostsAndLocksMetadataOnly(t *testing.T) {
	t.Parallel()
	x := newChannelHostAPIFixture(t)

	x.assertUnlockedSnapshotHasContent(t)
	x.assertPostDedupAndLiveEvent(t)
	x.assertCursorCatchUp(t)
	x.assertPagingBothWays(t)
	x.assertChannelValidationErrors(t)
	x.assertLockedVaultFailsClosedAndTopicIsMetadataOnly(t)
}

// channelHostAPIFixture is the HA-E08 state: a real host, a team whose lead
// runs in a ready session, and a registered channel topic.
type channelHostAPIFixture struct {
	f       *channelFixture
	teamID  string
	svc     *TasksService
	sub     *Subscription
	postSeq int64
}

func newChannelHostAPIFixture(t *testing.T) *channelHostAPIFixture {
	t.Helper()
	f := newChannelFixture(t)
	teamID := f.teamID
	svc := f.rt.tasksService
	if svc == nil {
		t.Fatal("the tasks service is not wired")
	}
	f.rt.RegisterTeamChannel(teamID)
	sub, err := f.rt.hub.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TeamChannelTopicName(teamID)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return &channelHostAPIFixture{f: f, teamID: teamID, svc: svc, sub: sub}
}

// assertUnlockedSnapshotHasContent checks the pre-post snapshot.
func (x *channelHostAPIFixture) assertUnlockedSnapshotHasContent(t *testing.T) {
	t.Helper()
	snapState, _, ok := x.sub.Snapshot()
	if !ok {
		t.Fatal("no topic snapshot")
	}
	snapLines := channelWireLines(t, snapState)
	if len(snapLines) == 0 || snapLines[len(snapLines)-1].Content == "" {
		t.Fatalf("unlocked snapshot lines = %+v, want content on the oldest line", snapLines)
	}
}

// assertPostDedupAndLiveEvent posts once, replays the same command_id, and
// waits for the single live event.
func (x *channelHostAPIFixture) assertPostDedupAndLiveEvent(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	first, err := x.svc.PostChannelMessage(ctx, &PostChannelCommand{
		CommandID: "post-1", TeamID: x.teamID, ToTaskID: x.f.memAID, Text: testChannelHello,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := x.svc.PostChannelMessage(ctx, &PostChannelCommand{
		CommandID: "post-1", TeamID: x.teamID, ToTaskID: x.f.memAID, Text: testChannelHello,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ChannelSeq != second.ChannelSeq {
		t.Fatalf("replayed command_id posted again: %d then %d", first.ChannelSeq, second.ChannelSeq)
	}
	x.postSeq = first.ChannelSeq
	wantSeq := mustChannelEventSeq(t, first.ChannelSeq)
	for {
		ev, ok := x.sub.Next(ctx)
		if !ok {
			t.Fatal("the topic stream ended before the post event")
		}
		if ev.Type != channelEventName || ev.Seq != wantSeq {
			continue
		}
		// A live event payload is one line, not a snapshot.
		var live channelLineWire
		if err := json.Unmarshal(ev.Payload, &live); err != nil {
			t.Fatal(err)
		}
		if live.ChannelSeq != first.ChannelSeq || live.Content != testChannelHello {
			t.Fatalf("live event line = %+v, want the posted line with content", live)
		}
		return
	}
}

// assertPagingBothWays checks the general and thread pages and the cursors.
func (x *channelHostAPIFixture) assertPagingBothWays(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	general, err := x.svc.PageChannel(ctx, x.teamID, "", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(general) < 3 {
		t.Fatalf("general page = %d lines, want the plan, decision and post", len(general))
	}
	if general[0].Content != testChannelHello || general[0].Attempt != 1 {
		t.Fatalf("newest general line = %+v, want the post at attempt 1", general[0])
	}
	thread, err := x.svc.PageChannel(ctx, x.teamID, x.f.memAID, 0, general[0].ChannelSeq, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(thread) != 0 {
		t.Fatalf("thread page above the newest general line = %+v, want none", thread)
	}
	back, err := x.svc.PageChannel(ctx, x.teamID, "", 3, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) == 0 || back[len(back)-1].ChannelSeq != 1 || back[0].ChannelSeq >= 3 {
		t.Fatalf("backward page below #3 = %+v, want lines ending at #1", back)
	}
}

// assertLockedVaultFailsClosedAndTopicIsMetadataOnly checks the locked-vault
// behaviour of the service and the topic (§5.3: metadata only while the vault
// is locked).
func (x *channelHostAPIFixture) assertLockedVaultFailsClosedAndTopicIsMetadataOnly(t *testing.T) {
	t.Helper()
	x.f.hp.Vault().Lock()
	if _, err := x.svc.PageChannel(t.Context(), x.teamID, "", 0, 0, 0); err == nil {
		t.Fatal("locked PageChannel succeeded, want fail closed")
	}
	if _, err := x.svc.PostChannelMessage(t.Context(), &PostChannelCommand{
		CommandID: "post-locked", TeamID: x.teamID, ToTaskID: x.f.memAID, Text: "while locked",
	}); err == nil {
		t.Fatal("a post on a locked vault succeeded, want fail closed")
	}
	lockedSub, err := x.f.rt.hub.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TeamChannelTopicName(x.teamID)})
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

// assertCursorCatchUp subscribes with a cursor before the post and checks
// durable catch-up through Since.
func (x *channelHostAPIFixture) assertCursorCatchUp(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	if x.postSeq < 2 {
		t.Fatalf("postSeq = %d, want >= 2", x.postSeq)
	}
	sub, err := x.f.rt.hub.Subscribe(ctx, SubscribeParams{
		Principal: testAppPrincipal,
		Topic:     TeamChannelTopicName(x.teamID),
		Cursor: &EventCursor{
			Topic:       TeamChannelTopicName(x.teamID),
			Incarnation: x.f.rt.incarnation.IncarnationID,
			Sequence:    mustChannelEventSeq(t, x.postSeq-1),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if _, _, hasSnap := sub.Snapshot(); hasSnap {
		t.Fatal("cursor catch-up unexpectedly sent a snapshot")
	}
	ev, ok := sub.Next(ctx)
	if !ok || ev.Type != channelEventName || ev.Seq != mustChannelEventSeq(t, x.postSeq) {
		t.Fatalf("catch-up event = (%+v, %v), want %s at #%d", ev, ok, channelEventName, x.postSeq)
	}
	topic := &teamChannelTopic{topics: x.f.rt.teamChannels, teamID: x.teamID}
	if far, farErr := topic.Since(ctx, math.MaxUint64); farErr != nil || len(far) != 0 {
		t.Fatalf("Since(MaxUint64) = (%v, %v), want empty", far, farErr)
	}
}

// assertChannelValidationErrors checks PageChannel and PostChannelMessage
// boundary rejections and TasksService accessor.
func (x *channelHostAPIFixture) assertChannelValidationErrors(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	gotSvc, svcErr := x.f.rt.TasksService()
	if svcErr != nil || gotSvc != x.svc {
		t.Fatalf("TasksService() = (%v, %v), want %v", gotSvc, svcErr, x.svc)
	}
	for _, tc := range []struct {
		name string
		err  error
		want errcode.Code
	}{
		{"empty team", errOfPage(x.svc.PageChannel(ctx, "", "", 0, 0, 0)), errcode.TasksTeamNotFound},
		{"negative cursor", errOfPage(x.svc.PageChannel(ctx, x.teamID, "", -1, 0, 0)), errcode.InvalidArgument},
		{"since >= before", errOfPage(x.svc.PageChannel(ctx, x.teamID, "", 5, 5, 0)), errcode.InvalidArgument},
		{"missing team", errOfPage(x.svc.PageChannel(ctx, "no-team", x.f.memAID, 0, 0, 0)), errcode.TasksTeamNotFound},
		{"empty command_id", errOfPost(x.svc.PostChannelMessage(ctx, &PostChannelCommand{TeamID: x.teamID, Text: "hi"})), errcode.InvalidArgument},
		{"empty post text", errOfPost(x.svc.PostChannelMessage(ctx, &PostChannelCommand{CommandID: "c1", TeamID: x.teamID, Text: " \t\n "})), errcode.InvalidArgument},
		{"oversized post", errOfPost(x.svc.PostChannelMessage(ctx, &PostChannelCommand{CommandID: "c2", TeamID: x.teamID, Text: strings.Repeat("a", maxChannelPostBytes+1)})), errcode.TasksPostTooLarge},
	} {
		if errcode.CodeOf(tc.err) != tc.want {
			t.Fatalf("%s code = %v, want %v", tc.name, errcode.CodeOf(tc.err), tc.want)
		}
	}
}

func errOfPage(_ []ChannelLineView, err error) error { return err }
func errOfPost(_ PostChannelResult, err error) error { return err }

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
