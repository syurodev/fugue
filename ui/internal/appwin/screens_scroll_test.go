package appwin

import (
	"image"
	"testing"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/session"
)

// The screens' scroll anchors of F10.4d (desktop-app v0.9 §5.4): a fresh
// session opens at the last turn's user message — not the bottom — and a
// composer send anchors its message near the top once the item lands.

// pageWithLastUser builds a newest page whose last user message is uid at
// sequence useq, with reply items after it.
func pageWithLastUser(uid string, useq int) []client.TranscriptItem {
	page := itemsFrom(1, useq-1)
	page = append(page, client.TranscriptItem{
		ItemID: uid, Kind: session.KindUserMessage, Sequence: uint64(max(useq, 0)), Revision: 1,
		View: client.TranscriptView{Text: "reopen me"},
	})
	return append(page, itemsFrom(useq+1, 3)...)
}

// TestReopeningASessionAnchorsAtTheLastTurnUserMessage pins the reopen rule:
// a session's first open lands its view at the last turn's user message
// before the pane draws, returning to an open session keeps the reader's
// place, and a session with no turn keeps the fresh follow.
func TestReopeningASessionAnchorsAtTheLastTurnUserMessage(t *testing.T) {
	t.Parallel()
	s, _ := newTestScreens(t)

	s.openSession("s1")
	s.applyResult(session.Result{Kind: session.ResultPage, Session: "s1", Items: pageWithLastUser("u-last", 4)})
	view := s.viewFor("s1")
	if got := view.AnchoredAt(); got != "u-last" {
		t.Fatalf("the reopened session anchored at %q, want the last turn's u-last", got)
	}
	if view.Following() {
		t.Fatal("the reopen anchor left the follow on")
	}

	// Returning to an already-open session keeps the reader's place: their
	// choice wins, the reopen anchor does not fire again.
	view.FollowOff()
	view.ScrollTo(500)
	s.openSession("s2")
	s.openSession("s1")
	if got := s.viewFor("s1").Offset(); got != 500 {
		t.Fatalf("session s1's offset = %d after the round trip, want the reader's 500", got)
	}

	// A session with no turn yet clears the arm and keeps the fresh view,
	// which pins the newest item on its first frame (the view-level rule).
	s.openSession("s3")
	s.applyResult(session.Result{Kind: session.ResultPage, Session: "s3", Items: itemsFrom(1, 5)})
	if got := s.viewFor("s3").AnchoredAt(); got != "" {
		t.Fatalf("a turn-less session anchored at %q, want the fresh follow", got)
	}
	if got := s.viewFor("s3").Offset(); got != 0 {
		t.Fatalf("the turn-less session's offset = %d, want the fresh 0", got)
	}
}

// TestSentMessageFiresTheTurnAnchorWhenItsItemLands pins the send path end
// to end: an Enter-send arms the anchor, the sent message's item anchors it
// near the top when it lands, and a failed send disarms.
func TestSentMessageFiresTheTurnAnchorWhenItsItemLands(t *testing.T) {
	t.Parallel()
	fx := newComposerFixture(t, stateReady, client.Capability{})
	f := newFrameRunner(t)
	st := fx.state()
	fx.sc.open = "s1"
	fx.sc.transcripts["s1"] = session.NewTranscript("s1")
	fx.sc.transcripts["s1"].ApplyNewestPage(itemsFrom(1, 10))

	f.focus(t, fx)
	f.typeInto(st, "one question")
	f.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(f.env, gtx, fx.sc, true)
	})
	f.tapReturn(0)
	f.frame(t, image.Pt(600, 400), func(gtx *layout.Context) {
		fx.c.layout(f.env, gtx, fx.sc, true)
	})
	if !fx.sc.turnArm {
		t.Fatal("the Enter-send did not arm the new-turn anchor")
	}
	if got := fx.sc.turnSeq; got != 10 {
		t.Fatalf("turnSeq = %d, want the pre-send sequence 10", got)
	}

	// The sent message's item lands: the anchor fires at it.
	page := itemsFrom(1, 10)
	page = append(page, client.TranscriptItem{
		ItemID: "u-sent", Kind: session.KindUserMessage, Sequence: 11, Revision: 1,
		View: client.TranscriptView{Text: "one question"},
	})
	fx.sc.transcripts["s1"].ApplyNewestPage(page)
	fx.sc.applyTurnAnchor()
	if got := fx.sc.viewFor("s1").AnchoredAt(); got != "u-sent" {
		t.Fatalf("the sent message anchored at %q, want u-sent", got)
	}
	if fx.sc.turnArm {
		t.Fatal("the armed anchor stayed armed after firing")
	}

	// A failed send stands the anchor down: no item will ever arrive.
	fx.sc.turnArm = true
	fx.sc.applyPostResult(session.PostResult{
		Kind: session.PostSend, Session: "s1", Text: "one question", Err: client.ErrDisconnected,
	})
	if fx.sc.turnArm {
		t.Fatal("the failed send left the anchor armed")
	}
}
