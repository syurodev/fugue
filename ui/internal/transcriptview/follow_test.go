package transcriptview

import (
	"fmt"
	"testing"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/session"
)

// uniformItems builds n message items with the ids the tests name.
func uniformItems(n int, prefix string) []*session.Item {
	items := make([]*session.Item, n)
	for i := range items {
		items[i] = message(fmt.Sprintf("%s%d", prefix, i), uint64(i+1), "paragraph")
	}
	return items
}

// visibleIDs returns the ids of the items the current offset renders first
// and last.
func (h *harness) visibleIDs() (string, string) {
	prefix := h.st.prefix(h.items)
	first, last := visibleRange(prefix, h.st.scrollOffset, h.height)
	return h.items[first].ItemID, h.items[last].ItemID
}

// TestOpeningASessionShowsTheNewestItem pins that a fresh view follows the
// newest item: the first frame after the newest page lands on it, not on the
// oldest item of the page.
func TestOpeningASessionShowsTheNewestItem(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 600, 400, uniformItems(50, "i"))
	h.frame()
	if !h.st.Following() {
		t.Fatal("a fresh view does not follow the newest item")
	}
	if _, last := h.visibleIDs(); last != "i49" {
		t.Fatalf("opening shows %q last, want the newest item i49", last)
	}
	if h.st.Offset() == 0 {
		t.Fatal("the view did not scroll to the newest item")
	}
}

// TestNewItemsAreFollowedOnlyAtTheBottom pins the follow rule: new items and
// growing views land on screen while the reader is at the bottom, stop
// following once they scroll up, and resume when they return.
func TestNewItemsAreFollowedOnlyAtTheBottom(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 600, 400, uniformItems(20, "i"))
	h.frame()
	if _, last := h.visibleIDs(); last != "i19" {
		t.Fatalf("bottom shows %q last, want i19", last)
	}

	// A new item arrives while the reader is at the bottom: it shows.
	h.items = append(h.items, message("i20", 21, "new"))
	h.frame()
	if _, last := h.visibleIDs(); last != "i20" {
		t.Fatalf("followed bottom shows %q last, want the new i20", last)
	}

	// The reader scrolls up: following ends, and a new item no longer drags
	// the view down.
	h.scroll(-200)
	if h.st.Following() {
		t.Fatal("scrolling up did not end the follow")
	}
	h.items = append(h.items, message("i21", 22, "newer"))
	h.frame()
	if _, last := h.visibleIDs(); last == "i21" {
		t.Fatal("a new item was followed after the reader scrolled up")
	}

	// Back at the bottom, the follow resumes.
	h.scroll(100 * estimateH)
	if !h.st.Following() {
		t.Fatal("returning to the bottom did not resume the follow")
	}
	h.items = append(h.items, message("i22", 23, "newest"))
	h.frame()
	if _, last := h.visibleIDs(); last != "i22" {
		t.Fatalf("resumed follow shows %q last, want the new i22", last)
	}

	// Short content never scrolls: an upward wheel must not end the follow,
	// and a new item still shows when the content grows.
	short := newHarness(t, 600, 400, uniformItems(2, "s"))
	short.frame()
	short.scroll(-200)
	if !short.st.Following() {
		t.Fatal("an upward wheel ended the follow on content shorter than the viewport")
	}
	short.items = append(short.items, message("s2", 3, "new"))
	short.frame()
	if _, last := short.visibleIDs(); last != "s2" {
		t.Fatalf("short content shows %q last, want the new s2", last)
	}
}

// TestPrependingOlderItemsKeepsTheReaderAnchor pins the anchor rule: when an
// older page is prepended and its height accounted for, the item the reader
// was looking at stays in place instead of jumping by the page's height.
func TestPrependingOlderItemsKeepsTheReaderAnchor(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 600, 400, uniformItems(60, "i"))
	h.st.follow = false
	h.frame()
	h.scroll(20 * estimateH)
	before, _ := h.visibleIDs()

	// An older page prepends ten items above everything held.
	prepended := uniformItems(10, "p")
	h.items = append(append([]*session.Item{}, prepended...), h.items...)
	h.st.AccountForPrepended(len(prepended))
	// The first frame corrects from the prepended heights' estimate, the
	// second from the measured heights; the anchor survives both.
	h.frame()
	h.frame()

	// The anchored item stays in view: its exact pixel position is only as
	// good as the prepended heights' estimate, but the reader must not lose
	// their place.
	anchored := -1
	for i, item := range h.items {
		if item.ItemID == before {
			anchored = i
		}
	}
	if anchored < 0 {
		t.Fatalf("the anchored item %q vanished", before)
	}
	prefix := h.st.prefix(h.items)
	first, last := visibleRange(prefix, h.st.scrollOffset, h.height)
	if anchored < first || anchored > last {
		t.Fatalf("after the prepend the reader sees items %d..%d, want the anchored %d in view",
			first, last, anchored)
	}
	if h.st.AtTop() {
		t.Fatal("the reader is back at the top after the load, so paging would crawl")
	}
}

// TestExpansionSurvivesANewRevision pins the expansion rule: a card the
// reader opened stays open when the item's revision changes, and the new
// revision's height is seeded from the old one instead of the estimate, so
// the list does not jump.
func TestExpansionSurvivesANewRevision(t *testing.T) {
	t.Parallel()
	tool := &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: "t1", Kind: "tool_call", Sequence: 1, Revision: 1,
		Metadata: client.TranscriptMetadata{Tool: budgetTool, Status: budgetStatus, ExitCode: int32Ptr(0), DurationMillis: 12},
		View:     client.TranscriptView{Text: "tool output"},
	}}
	h := newHarness(t, 600, 900, []*session.Item{tool})
	h.st.follow = false
	h.frame()
	collapsed := h.st.heightOf(tool)

	// The reader expands the card while the tool runs.
	h.st.expanded["t1"] = true
	h.frame()
	expanded := h.st.heightOf(tool)
	if expanded <= collapsed {
		t.Fatalf("expanded height = %d, want more than the collapsed %d", expanded, collapsed)
	}

	// The tool completes: a new revision, same reader intent.
	tool.Revision = 2
	if got := h.st.heightOf(tool); got != expanded {
		t.Fatalf("new revision height = %d, want the seeded %d", got, expanded)
	}
	h.frame()
	if !h.st.expanded["t1"] {
		t.Fatal("the card collapsed when the item's revision changed")
	}
	if got := h.st.heightOf(tool); got <= collapsed {
		t.Fatalf("height after the revision = %d, want the expanded layout above %d", got, collapsed)
	}
}
