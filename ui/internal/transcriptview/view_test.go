package transcriptview

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// harness is a headless transcript pane: a router for input, one kit.Env
// over one theme, one State, and the items it renders.
type harness struct {
	t      *testing.T
	router input.Router
	ops    op.Ops
	env    *kit.Env
	th     theme.Theme
	st     *State
	items  []*session.Item
	width  int
	height int
	// now is the fake frame clock the env begins every frame at.
	now time.Time
}

// newHarness builds the harness at the given viewport size.
func newHarness(t *testing.T, width, height int, items []*session.Item) *harness {
	t.Helper()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)
	return &harness{
		t: t, env: &kit.Env{Th: &th, Sh: shaper}, th: th, st: NewState(),
		items: items, width: width, height: height,
		now: time.Unix(1_700_000_000, 0),
	}
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
	h.env.Begin(h.now)
	dims := Layout(h.env, h.context(), h.st, h.items)
	h.router.Frame(&h.ops)
	return dims
}

// scroll queues one wheel event and runs the frame that sees it.
func (h *harness) scroll(deltaY int) {
	h.router.Queue(pointer.Event{
		Kind:   pointer.Scroll,
		Source: pointer.Mouse,
		Scroll: f32.Pt(0, float32(deltaY)),
	})
	h.frame()
}

// message builds one assistant message item at revision 1.
func message(itemID string, seq uint64, text string) *session.Item {
	return &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: itemID, TurnID: "turn", Kind: "assistant_message",
		Sequence: seq, Revision: 1, View: client.TranscriptView{Text: text},
	}}
}

// TestTranscriptLaysOutOnlyAWindowAroundTheViewport pins the move onto the
// kit's VirtualList: a frame lays out the visible cards plus the kit's render
// window around them, never the whole list, at the pinned bottom and
// mid-list.
func TestTranscriptLaysOutOnlyAWindowAroundTheViewport(t *testing.T) {
	t.Parallel()
	items := make([]*session.Item, 2000)
	for i := range items {
		items[i] = message(fmt.Sprintf("i%d", i), uint64(i+1), "paragraph")
	}
	h := newHarness(t, 600, 400, items)
	// This test pins the window, not the follow: read from a fixed offset
	// instead of the pinned bottom.
	h.st.FollowOff()

	dims := h.frame()
	// The viewport holds some rows and the kit renders its window around
	// them; the far ends of the 2,000 items stay unlaid out.
	if got := h.st.Rendered(); got <= 0 || got > 120 {
		t.Fatalf("first frame laid out %d cards, want the bounded render window", got)
	}
	if h.st.list.CacheLen() >= len(items) {
		t.Fatalf("cache holds %d of %d items, want the far ends unlaid out", h.st.list.CacheLen(), len(items))
	}
	if dims.Size.X != 600 || dims.Size.Y != 400 {
		t.Fatalf("pane size = %v, want the viewport", dims.Size)
	}

	// Scrolled into the middle: still the bounded window, and the edges
	// stay unlaid out.
	h.scroll(-1)
	h.scroll(1000 * estimateH)
	if got := h.st.Rendered(); got <= 0 || got > 120 {
		t.Fatalf("scrolled frame laid out %d cards, want the bounded render window", got)
	}
	if h.st.list.CacheLen() >= len(items) {
		t.Fatalf("cache holds %d of %d items after the scroll, want the edges unlaid out", h.st.list.CacheLen(), len(items))
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
	if got := h.st.Rendered(); got != 3 {
		t.Fatalf("laid out %d cards, want the three items", got)
	}
	for _, item := range items {
		if h.st.list.Expanded(item.ItemID) {
			t.Fatalf("%s card started expanded", item.Kind)
		}
	}
	// Expanding a card grows it: the body renders only then.
	before := h.st.list.ItemState("t1").MeasuredHeight
	h.st.list.SetExpanded("t1", true)
	h.frame()
	if after := h.st.list.ItemState("t1").MeasuredHeight; after <= before {
		t.Fatalf("expanded tool card height = %d, want more than the collapsed %d", after, before)
	}
}

// int32Ptr returns a pointer to v, for metadata fields that are optional.
func int32Ptr(v int32) *int32 { return &v }

// TestUnknownItemKindRendersAGenericCard pins that a kind the app does not
// know renders as a generic card naming its wire value: never a crash and
// never a dropped item. The client carries an unnamed kind as its decimal
// wire value, and the card says "Unknown item (kind N)".
func TestUnknownItemKindRendersAGenericCard(t *testing.T) {
	t.Parallel()
	items := []*session.Item{{TranscriptItem: client.TranscriptItem{
		ItemID: "x1", Kind: "99", Sequence: 1, Revision: 1,
		View: client.TranscriptView{Text: "future content"},
	}}}
	h := newHarness(t, 600, 900, items)
	dims := h.frame()
	if h.st.Rendered() != 1 {
		t.Fatalf("laid out %d cards, want the one unknown item", h.st.Rendered())
	}
	if dims.Size.Y <= 0 {
		t.Fatal("the unknown card rendered nothing")
	}
	if got := headerFor(items[0]); got != "Unknown item (kind 99)" {
		t.Fatalf("generic header = %q, want the kind's wire value", got)
	}
	// A named kind keeps its name on the generic header.
	named := headerFor(&session.Item{TranscriptItem: client.TranscriptItem{Kind: "notice"}})
	if named == "Unknown item (kind notice)" {
		t.Fatalf("named kind %q rendered as unknown", named)
	}
}
