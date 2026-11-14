package appwin

import (
	"fmt"
	"testing"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
)

// fakeFetcher records the reads the screens request and never answers, so a
// test drives the results by hand.
type fakeFetcher struct {
	pages []pageRequest
}

// pageRequest is one recorded page read.
type pageRequest struct {
	session string
	before  int64
}

func (f *fakeFetcher) RequestSessions() {}
func (f *fakeFetcher) RequestPage(sessionID string, before int64) {
	f.pages = append(f.pages, pageRequest{session: sessionID, before: before})
}
func (f *fakeFetcher) Results() <-chan session.Result { return make(chan session.Result) }

// newTestScreens builds screens over an idle manager and a recording fetcher.
func newTestScreens(t *testing.T) (*screens, *fakeFetcher) {
	t.Helper()
	fake := &fakeFetcher{}
	m := hostconn.NewManager(&hostconn.Config{})
	return newScreens(m, fake), fake
}

// itemsFrom builds count items with ascending sequences starting at start.
func itemsFrom(start, count int) []client.TranscriptItem {
	items := make([]client.TranscriptItem, count)
	for i := range items {
		seq := start + i
		items[i] = client.TranscriptItem{
			ItemID: fmt.Sprintf("i%d", seq), Kind: "assistant_message",
			Sequence: uint64(max(seq, 0)), Revision: 1,
			View: client.TranscriptView{Text: "paragraph"},
		}
	}
	return items
}

// TestPagingOlderLoadsOnePagePerScrollToTheTop pins the paging rule: an
// arrival at the top loads one page, the prepended history moves the reader
// off the top so no further page loads, and only a fresh arrival at the top
// loads the next one.
func TestPagingOlderLoadsOnePagePerScrollToTheTop(t *testing.T) {
	t.Parallel()
	s, fake := newTestScreens(t)
	s.open = "s1"
	s.transcripts[s.open] = session.NewTranscript(s.open)
	// A full newest page: the model holds sequences 61..260.
	s.transcripts[s.open].ApplyNewestPage(itemsFrom(61, session.PageLimit))
	view := s.viewFor(s.open)
	view.FollowOff()

	// The reader has scrolled and is at the top: one page loads.
	s.scrolled = true
	s.maybePageOlder()
	if len(fake.pages) != 1 {
		t.Fatalf("requests = %d, want the one arrival-at-the-top page", len(fake.pages))
	}

	// The older page prepends thirty items; the reader's anchor moves them
	// off the top.
	older := itemsFrom(31, 30)
	s.applyResult(session.Result{Kind: session.ResultPage, Session: s.open, Older: true, Items: older})
	if len(s.transcripts[s.open].Items()) != session.PageLimit+30 {
		t.Fatalf("items = %d, want the merged page", len(s.transcripts[s.open].Items()))
	}
	if view.AtTop() {
		t.Fatal("the reader is still at the top after the prepended page, so paging would crawl")
	}
	s.maybePageOlder()
	if got := len(fake.pages); got != 1 {
		t.Fatalf("requests after the load = %d, want no background crawl", got)
	}

	// A fresh arrival at the top loads the next page.
	view.BackToTop()
	s.maybePageOlder()
	if got := len(fake.pages); got != 2 {
		t.Fatalf("requests after the next arrival = %d, want 2", got)
	}
}

// TestScrollStateIsPerSession pins that each session keeps its own scroll
// state: session B starts fresh at its newest item and session A's offset is
// intact when the reader returns.
func TestScrollStateIsPerSession(t *testing.T) {
	t.Parallel()
	s, _ := newTestScreens(t)
	s.openSession("a")
	s.openSession("b")

	// Session B starts fresh, following its newest item.
	b := s.viewFor("b")
	if !b.Following() || b.Offset() != 0 {
		t.Fatalf("session b state = follow %v offset %d, want fresh", b.Following(), b.Offset())
	}

	// Session a keeps its scroll state across the round trip.
	a := s.viewFor("a")
	a.FollowOff()
	a.BackToTop()
	a.AccountForPrepended(0)
	a.ScrollTo(500)
	s.openSession("b")
	s.openSession("a")
	if got := s.viewFor("a").Offset(); got != 500 {
		t.Fatalf("session a offset = %d after the round trip, want 500", got)
	}
	if !s.viewFor("b").Following() {
		t.Fatal("session b lost its own follow state")
	}
}
