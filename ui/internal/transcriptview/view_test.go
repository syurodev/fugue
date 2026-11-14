package transcriptview

import (
	"fmt"
	"image"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/pkg/client"
	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// harness is a headless transcript pane: a router for input, one theme, one
// State and the items it renders.
type harness struct {
	t      *testing.T
	router input.Router
	ops    op.Ops
	th     theme.Theme
	st     *State
	items  []*session.Item
	width  int
	height int
}

// newHarness builds the harness at the given viewport size.
func newHarness(t *testing.T, width, height int, items []*session.Item) *harness {
	t.Helper()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, th: theme.Light(shaper), st: NewState(), items: items, width: width, height: height}
}

// context builds this frame's layout context over the router.
func (h *harness) context() *layout.Context {
	return &layout.Context{
		Ops:         &h.ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: image.Pt(h.width, h.height)},
		Source:      h.router.Source(),
	}
}

// frame lays the pane out once and hands the ops to the router.
func (h *harness) frame() layout.Dimensions {
	h.ops.Reset()
	dims := Layout(h.context(), &h.th, h.st, h.items)
	h.router.Frame(&h.ops)
	return dims
}

// scroll queues one wheel event and runs the frame that sees it.
func (h *harness) scroll(deltaY int) layout.Dimensions {
	h.router.Queue(pointer.Event{
		Kind:   pointer.Scroll,
		Source: pointer.Mouse,
		Scroll: f32.Pt(0, float32(deltaY)),
	})
	return h.frame()
}

// message builds one assistant message item.
func message(itemID string, seq, rev uint64, text string) *session.Item {
	return &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: itemID, TurnID: "turn", Kind: "assistant_message",
		Sequence: seq, Revision: rev, View: client.TranscriptView{Text: text},
	}}
}

// windowOf replicates the layout's window arithmetic over the state's current
// heights and the offset the next frame will have after a scroll of delta, so
// tests pin exactly how many cards a frame must lay out.
func (h *harness) windowOf(delta int) (w0, w1 int) {
	prefix := h.st.prefix(h.items)
	offset := min(max(h.st.scrollOffset+delta, 0), max(0, prefix[len(h.items)]-h.height))
	first, last := visibleRange(prefix, offset, h.height)
	w0, w1 = max(0, first-aboveWindow), min(len(h.items)-1, last+belowWindow)
	return w0, w1
}

// TestVirtualListLaysOutVisiblePlusFiftyEachSide pins the virtualization
// window: a frame lays out exactly the visible cards plus fifty on each side,
// never the whole list, at the edges and mid-list.
func TestVirtualListLaysOutVisiblePlusFiftyEachSide(t *testing.T) {
	t.Parallel()
	items := make([]*session.Item, 2000)
	for i := range items {
		items[i] = message(fmt.Sprintf("i%d", i), uint64(i+1), 1, "paragraph")
	}
	h := newHarness(t, 600, 400, items)

	w0, w1 := h.windowOf(0)
	dims := h.frame()
	if got := h.st.rendered; got != w1-w0+1 {
		t.Fatalf("first frame laid out %d cards, want the window %d..%d (%d cards)", got, w0, w1, w1-w0+1)
	}
	if dims.Size.X != 600 || dims.Size.Y != 400 {
		t.Fatalf("pane size = %v, want the viewport", dims.Size)
	}

	// Scroll into the middle: the window follows, still the visible items
	// plus fifty on each side, and the far ends stay unlaid out.
	w0, w1 = h.windowOf(1000 * estimateH)
	h.scroll(1000 * estimateH)
	if got := h.st.rendered; got != w1-w0+1 {
		t.Fatalf("scrolled frame laid out %d cards, want the window %d..%d (%d cards)", got, w0, w1, w1-w0+1)
	}
	if w0 == 0 || w1 == len(items)-1 {
		t.Fatalf("window %d..%d never left the edges", w0, w1)
	}
}

// TestLayoutCacheIsBoundedAndKeyedByItemAndRevision pins the cache: the bound
// evicts the least recently used, and one item at two revisions holds two
// entries while a repeat of the same revision holds one.
func TestLayoutCacheIsBoundedAndKeyedByItemAndRevision(t *testing.T) {
	t.Parallel()
	c := newCardCache(3)
	c.put(cardKey{"a", 1}, &card{})
	c.put(cardKey{"b", 1}, &card{})
	c.put(cardKey{"c", 1}, &card{})
	if _, ok := c.get(cardKey{"a", 1}); !ok {
		t.Fatal("a just-put card was not found")
	}
	c.put(cardKey{"d", 1}, &card{})
	if _, ok := c.get(cardKey{"b", 1}); ok {
		t.Fatal("the least recently used card survived the bound")
	}
	if _, ok := c.get(cardKey{"a", 1}); !ok {
		t.Fatal("the most recently used card was evicted")
	}
	if c.len() != 3 {
		t.Fatalf("cache holds %d cards, want 3", c.len())
	}
	// Re-putting a key updates it instead of duplicating it.
	same := &card{height: 7}
	c.put(cardKey{"a", 1}, same)
	if got, ok := c.get(cardKey{"a", 1}); !ok || got != same || c.len() != 3 {
		t.Fatalf("re-put duplicated or replaced the wrong entry: %v %v", got, ok)
	}

	// Through the layout: one item at two revisions is two cache entries.
	items := []*session.Item{message("i1", 1, 1, "one")}
	h := newHarness(t, 400, 800, items)
	h.frame()
	h.items[0].Revision = 2
	h.frame()
	if got := h.st.CachedCards(); got != 2 {
		t.Fatalf("cache holds %d cards for two revisions, want 2", got)
	}
}

// TestToolCallAndReasoningCardsStartCollapsed pins that tool calls, reasoning
// and file changes render their header only until they are expanded.
func TestToolCallAndReasoningCardsStartCollapsed(t *testing.T) {
	t.Parallel()
	items := []*session.Item{
		{TranscriptItem: client.TranscriptItem{
			ItemID: "t1", Kind: "tool_call", Sequence: 1, Revision: 1,
			Metadata: client.TranscriptMetadata{Tool: "read_file", Status: "succeeded", ExitCode: int32Ptr(0), DurationMillis: 12},
			View:     client.TranscriptView{Text: "tool output"},
		}},
		{TranscriptItem: client.TranscriptItem{
			ItemID: "r1", Kind: "reasoning", Sequence: 2, Revision: 1,
			Metadata: client.TranscriptMetadata{ReasoningState: "summary"},
			View:     client.TranscriptView{Text: "thinking"},
		}},
		{TranscriptItem: client.TranscriptItem{
			ItemID: "f1", Kind: "file_change", Sequence: 3, Revision: 1,
			Metadata: client.TranscriptMetadata{ChangeKind: "modify", LinesAdded: 3, LinesRemoved: 1},
			View:     client.TranscriptView{Text: "@@ diff"},
		}},
	}
	h := newHarness(t, 600, 900, items)
	h.frame()
	for _, item := range items {
		c, ok := h.st.cards.get(cardKey{itemID: item.ItemID, revision: item.Revision})
		if !ok {
			t.Fatalf("%s card was not laid out", item.Kind)
		}
		if c.expanded {
			t.Fatalf("%s card started expanded", item.Kind)
		}
	}
	// Expanding a card grows it: the body renders only then.
	before := h.st.heightOf(items[0])
	h.st.cards.entries[cardKey{"t1", 1}].card.expanded = true
	h.frame()
	if after := h.st.heightOf(items[0]); after <= before {
		t.Fatalf("expanded tool card height = %d, want more than the collapsed %d", after, before)
	}
}

// int32Ptr returns a pointer to v, for metadata fields that are optional.
func int32Ptr(v int32) *int32 { return &v }

// TestUnknownItemKindRendersAGenericCard pins that a kind the app does not
// know renders as a generic card carrying its kind name: never a crash and
// never a dropped item.
func TestUnknownItemKindRendersAGenericCard(t *testing.T) {
	t.Parallel()
	items := []*session.Item{{TranscriptItem: client.TranscriptItem{
		ItemID: "x1", Kind: "mind_map", Sequence: 1, Revision: 1,
		View: client.TranscriptView{Text: "future content"},
	}}}
	h := newHarness(t, 600, 900, items)
	dims := h.frame()
	if h.st.rendered != 1 {
		t.Fatalf("laid out %d cards, want the one unknown item", h.st.rendered)
	}
	if dims.Size.Y <= 0 {
		t.Fatal("the unknown card rendered nothing")
	}
	if got := headerFor(items[0]); got != "mind_map" {
		t.Fatalf("generic header = %q, want the kind name", got)
	}
}
