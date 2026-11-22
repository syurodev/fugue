package transcriptview

import (
	"fmt"
	"image"
	"slices"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/session"
)

// The scroll and user-choice parts of DA-E15 (desktop-app v0.9 §5.4, "Cuộn"
// and "Thao tác của user thắng"): the view follows the stream at the newest
// edge, one wheel, marking drag or scroll key stops it, the "Latest" button
// returns with a running dot, a sent message anchors near the top with its
// context, resizes, expansions and prepends keep the reader's line, stop and
// error events never move the view, and the reader's expand/collapse choices
// beat the auto rules across frames and virtualization.

// userMsg builds one user_message item.
func userMsg(itemID string, seq uint64, text string) *session.Item {
	return &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: itemID, TurnID: "turn", Kind: session.KindUserMessage,
		Sequence: seq, Revision: 1, View: client.TranscriptView{Text: text},
	}}
}

// runningTool builds one standalone running tool row (its own turn, so it
// does not fold into a neighbouring group).
func runningTool(itemID string, seq uint64, turn string) *session.Item {
	return &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: itemID, TurnID: turn, Kind: kindToolCall, Sequence: seq, Revision: 1,
		Metadata: client.TranscriptMetadata{Tool: budgetTool, Status: statusRunning},
		View:     client.TranscriptView{Input: `{"file_path":"pkg/main.go"}`},
	}}
}

// keyTap queues one scroll-key press and runs the frame that sees it.
func (h *harness) keyTap(name key.Name) {
	h.router.Queue(key.Event{Name: name, State: key.Press})
	h.frame()
}

// dragFromTo queues a press then a pressed move (the marking gesture) and
// runs the frames that see them.
func (h *harness) dragFromTo(from, to image.Point) {
	h.router.Queue(pointer.Event{
		Kind: pointer.Press, Source: pointer.Mouse,
		Position: f32.Pt(float32(from.X), float32(from.Y)), Buttons: pointer.ButtonPrimary,
	})
	h.frame()
	h.router.Queue(pointer.Event{
		Kind: pointer.Move, Source: pointer.Mouse,
		Position: f32.Pt(float32(to.X), float32(to.Y)), Buttons: pointer.ButtonPrimary,
	})
	h.frame()
}

// clickAt queues a press and release at a point and runs the frames that
// see them.
func (h *harness) clickAt(p image.Point) {
	pos := f32.Pt(float32(p.X), float32(p.Y))
	h.router.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Position: pos, Buttons: pointer.ButtonPrimary})
	h.frame()
	h.router.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pos})
	h.frame()
}

// streamBatches appends n message items in ten batches, framing between
// them, the way a stream lands.
func (h *harness) streamBatches(start, n int) {
	const batches = 10
	for b := range batches {
		for i := start + b*n/batches; i < start+(b+1)*n/batches; i++ {
			h.items = append(h.items, message(fmt.Sprintf("s%d", i), uint64(max(i+1, 0)), "streamed"))
		}
		h.frame()
	}
}

// TestStreamFollowsTheBottomUntilTheReaderScrolls pins the follow rule over
// a 200-item stream: the reader at the newest edge rides every batch, one
// upward wheel ends the ride, the stream no longer drags the view, and the
// "Latest" jump re-engages it.
func TestStreamFollowsTheBottomUntilTheReaderScrolls(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 600, 400, uniformItems(20, "i"))
	h.frame()
	for range 9 {
		last := len(h.items)
		h.streamBatches(last, 20)
		if _, got := h.visibleIDs(); got != fmt.Sprintf("s%d", len(h.items)-1) {
			t.Fatalf("following reader sees %q last, want the newest s%d", got, len(h.items)-1)
		}
	}
	if len(h.items) != 200 {
		t.Fatalf("streamed %d items, want 200", len(h.items))
	}

	h.scroll(-120)
	if h.st.Following() {
		t.Fatal("one upward wheel did not end the follow")
	}
	still := len(h.items)
	h.streamBatches(still, 20)
	if _, got := h.visibleIDs(); got == fmt.Sprintf("s%d", len(h.items)-1) {
		t.Fatal("the stream dragged the view after the wheel ended the follow")
	}

	h.st.FollowLatest()
	h.frame()
	if !h.st.Following() {
		t.Fatal("the Latest jump did not re-engage the follow")
	}
	if _, got := h.visibleIDs(); got != fmt.Sprintf("s%d", len(h.items)-1) {
		t.Fatalf("after the jump the reader sees %q last, want the newest", got)
	}
}

// TestWheelDragSelectionOrScrollKeyStopsTheFollow pins the three reader acts
// of DA-E15: one wheel scroll, one marking drag, one scroll-key press — each
// ends the follow, and a new item stops dragging the view.
func TestWheelDragSelectionOrScrollKeyStopsTheFollow(t *testing.T) {
	t.Parallel()
	wheel := newHarness(t, 600, 400, uniformItems(30, "w"))
	wheel.frame()
	wheel.scroll(-120)
	if wheel.st.Following() {
		t.Fatal("the wheel kept the follow on")
	}
	wheel.items = append(wheel.items, message("w-new", 31, "new"))
	wheel.frame()
	if _, last := wheel.visibleIDs(); last == "w-new" {
		t.Fatal("a new item was followed after the wheel")
	}

	drag := newHarness(t, 600, 400, uniformItems(30, "d"))
	drag.frame()
	drag.dragFromTo(image.Pt(300, 200), image.Pt(300, 120))
	if drag.st.Following() {
		t.Fatal("the marking drag kept the follow on")
	}
	drag.items = append(drag.items, message("d-new", 31, "new"))
	drag.frame()
	if _, last := drag.visibleIDs(); last == "d-new" {
		t.Fatal("a new item was followed after the drag")
	}

	keys := newHarness(t, 600, 400, uniformItems(60, "k"))
	keys.frame()
	before := keys.st.Offset()
	keys.keyTap(key.NamePageUp)
	if keys.st.Following() {
		t.Fatal("the scroll key kept the follow on")
	}
	if keys.st.Offset() >= before {
		t.Fatalf("PageUp left the offset at %d, want it above %d", keys.st.Offset(), before)
	}
	keys.items = append(keys.items, message("k-new", 61, "new"))
	keys.frame()
	if _, last := keys.visibleIDs(); last == "k-new" {
		t.Fatal("a new item was followed after the scroll key")
	}
}

// TestLatestButtonReturnsToTheNewestWithARunningDot pins the "Latest"
// button: it shows when the reader is away from the newest edge, carries the
// running dot while a running item is off-screen, drops the dot when the
// running row is in view, hides at the newest edge, and its click returns.
func TestLatestButtonReturnsToTheNewestWithARunningDot(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 600, 400, uniformItems(30, "i"))
	h.items = append(h.items, runningTool("run1", 31, "turn-latest"))
	h.frame()
	if h.st.LatestShown() {
		t.Fatal("the Latest button showed while the reader followed the newest edge")
	}

	h.scroll(-120)
	if !h.st.LatestShown() {
		t.Fatal("the Latest button did not show away from the newest edge")
	}
	if !h.st.LatestDot() {
		t.Fatal("the running dot missed while the running row was off-screen")
	}

	// Anchored at the running row, it is in view: the dot drops, the button
	// stays.
	h.st.AnchorTurn("run1")
	h.frame()
	if !h.st.LatestShown() {
		t.Fatal("the Latest button vanished while the reader was still away from the edge")
	}
	if h.st.LatestDot() {
		t.Fatal("the running dot showed while the running row was in view")
	}

	h.scroll(-120)
	rect := h.st.LatestRect()
	if rect.Empty() {
		t.Fatal("the Latest button had no rect to click")
	}
	h.clickAt(image.Pt(rect.Min.X+rect.Dx()/2, rect.Min.Y+rect.Dy()/2))
	if !h.st.Following() {
		t.Fatal("clicking the Latest button did not re-engage the follow")
	}
	h.frame()
	if h.st.LatestShown() {
		t.Fatal("the Latest button stayed after the jump to the newest edge")
	}
}

// TestSendAnchorsTheMessageNearTheTopWithPreviousContext pins the new-turn
// anchor: the sent message sits near the top with 64 dp of the previous item
// above it, the anchor rides the item, and the streaming reply below never
// drags the view.
func TestSendAnchorsTheMessageNearTheTopWithPreviousContext(t *testing.T) {
	t.Parallel()
	items := uniformItems(60, "i")
	items = append(items, userMsg("u1", 61, "the sent message"))
	h := newHarness(t, 600, 400, items)
	h.frame()

	// The message just landed at the end: the anchor cannot scroll past the
	// end, so the view clamps to the bottom until the reply fills in below.
	h.st.AnchorTurn("u1")
	h.frame()
	if got := h.st.Offset(); got != h.st.list.MaxOffset() {
		t.Fatalf("offset = %d, want the clamped bottom %d", got, h.st.list.MaxOffset())
	}
	if h.st.Following() {
		t.Fatal("the new-turn anchor left the follow on")
	}
	if got := h.st.AnchoredAt(); got != "u1" {
		t.Fatalf("anchor = %q, want u1", got)
	}

	// The reply streams in: the anchor lifts u1's top to 64 px
	// (SpaceTurnContext at 1 px per dp), earlier content above it.
	for i := range 12 {
		h.items = append(h.items, message(fmt.Sprintf("r%d", i), uint64(62+i), "reply"))
	}
	h.frame()
	wantOffset := 0
	for _, it := range h.items {
		if it.ItemID == "u1" {
			break
		}
		wantOffset += h.st.list.ItemState(it.ItemID).MeasuredHeight
	}
	wantOffset -= 64
	if got := h.st.Offset(); got != wantOffset {
		t.Fatalf("offset after the reply = %d, want u1's top at 64 px (offset %d)", got, wantOffset)
	}
	first, _ := h.visibleIDs()
	if first == "u1" {
		t.Fatalf("the anchored message shows %q first, want earlier content above it", first)
	}

	// The reply keeps streaming: the anchored line holds, the view never
	// chases the bottom.
	before, firstBefore := h.st.Offset(), first
	h.items = append(h.items, message("r-more", 80, "more reply"))
	h.frame()
	if got := h.st.Offset(); got != before {
		t.Fatalf("the streaming reply dragged the offset %d -> %d", before, got)
	}
	if first, _ = h.visibleIDs(); first != firstBefore {
		t.Fatalf("the anchored line moved to %q, want %q", first, firstBefore)
	}
}

// TestExpansionResizeAndPrependsKeepTheReadersLine pins the item-ID anchor
// stability: an expansion above the line, a width resize and a prepended
// older page all keep the item the reader is looking at in place.
func TestExpansionResizeAndPrependsKeepTheReadersLine(t *testing.T) {
	t.Parallel()
	items := uniformItems(10, "a")
	tool := makeWorkToolItem("t5", 11, "turn-exp", "bash", statusDone, `{"command":"go test"}`, 0)
	items = append(items, tool)
	items = append(items, uniformItemsF(29, "b", 12)...)
	h := newHarness(t, 600, 400, items)
	h.st.FollowOff()
	h.frame()
	h.st.ScrollTo(8 * estimateH)
	h.frame()
	firstBefore, _ := h.visibleIDs()

	// Expanding the tool row above the anchored line: the line holds in the
	// same frame, the offset shifting by exactly the measured growth.
	beforeOffset := h.st.Offset()
	collapsed := h.st.list.ItemState("t5").MeasuredHeight
	h.st.list.SetExpanded("t5", true)
	h.frame()
	grew := h.st.list.ItemState("t5").MeasuredHeight
	if first, _ := h.visibleIDs(); first != firstBefore {
		t.Fatalf("the expansion moved the reader's line to %q, want %q", first, firstBefore)
	}
	if got := h.st.Offset(); got != beforeOffset+(grew-collapsed) {
		t.Fatalf("offset after the expansion = %d, want the growth %d folded in", got, grew-collapsed)
	}

	// A width resize re-measures every card: the line still holds.
	h.st.AnchorTurn("b18")
	h.frame()
	anchoredFirst, _ := h.visibleIDs()
	h.width = 300
	h.frame()
	if first, _ := h.visibleIDs(); first != anchoredFirst {
		t.Fatalf("the resize moved the anchored line to %q, want %q", first, anchoredFirst)
	}

	// An older page prepends above: the anchored item stays in view.
	prep := uniformItemsF(10, "p", 0)
	h.items = append(prep, h.items...)
	h.st.AccountForPrepended(len(prep))
	h.frame()
	h.frame()
	idx := slices.IndexFunc(h.items, func(it *session.Item) bool { return it.ItemID == "b18" })
	if idx < 0 {
		t.Fatal("the anchored item vanished")
	}
	first, last := h.st.list.Visible(h.context(), virtualItems(h.items), h.height)
	if idx < first || idx > last {
		t.Fatalf("after the prepend the reader sees %d..%d, want the anchored %d", first, last, idx)
	}
}

// uniformItemsF builds n message items with a sequence base.
func uniformItemsF(n int, prefix string, seqBase int) []*session.Item {
	items := make([]*session.Item, n)
	for i := range items {
		seq := seqBase + i + 1
		items[i] = message(fmt.Sprintf("%s%d", prefix, i), uint64(max(seq, 0)), "paragraph")
	}
	return items
}

// TestStopAndErrorEventsNeverMoveTheViewport pins the stop rule: a tool
// failing below the anchored line and a retried turn appending after it
// leave the reader exactly where they were.
func TestStopAndErrorEventsNeverMoveTheViewport(t *testing.T) {
	t.Parallel()
	items := uniformItems(20, "i")
	items = append(items, runningTool("tc1", 21, "turn-err"))
	h := newHarness(t, 600, 400, items)
	h.frame()
	h.st.AnchorTurn("i10")
	h.frame()
	before := h.st.Offset()
	firstBefore, _ := h.visibleIDs()

	// The tool fails: its row grows by the reason line, below the line the
	// reader holds, and the view neither jumps nor starts following.
	tc1, ok := h.transcriptItem("tc1")
	if !ok {
		t.Fatal("tc1 vanished")
	}
	tc1.Metadata.Status = statusFailed
	tc1.Metadata.ExitCode = int32Ptr(1)
	tc1.Revision = 2
	h.frame()
	if first, _ := h.visibleIDs(); first != firstBefore {
		t.Fatalf("the failure moved the reader's line to %q, want %q", first, firstBefore)
	}
	if h.st.Following() {
		t.Fatal("the failure event re-engaged the follow")
	}

	// A retry appends a fresh turn: still no move.
	h.items = append(h.items, userMsg("u2", 22, "retry"))
	h.items = append(h.items, runningTool("tc2", 23, "turn-retry"))
	h.frame()
	if first, _ := h.visibleIDs(); first != firstBefore {
		t.Fatalf("the retry moved the reader's line to %q, want %q", first, firstBefore)
	}
	if got := h.st.Offset(); got < before {
		t.Fatalf("offset = %d, want at least the pre-failure %d", got, before)
	}
}

// transcriptItem finds one of the harness's items by id.
func (h *harness) transcriptItem(id string) (*session.Item, bool) {
	idx := slices.IndexFunc(h.items, func(it *session.Item) bool { return it.ItemID == id })
	if idx < 0 {
		return nil, false
	}
	return h.items[idx], true
}

// buildLongRunningTurn builds 60 tool calls in one turn with the first still
// running, all folding into one work group.
func buildLongRunningTurn(prefix string) []*session.Item {
	items := make([]*session.Item, 0, 60)
	for i := range 60 {
		status := statusDone
		if i == 59 {
			status = statusRunning
		}
		items = append(items, makeWorkToolItem(
			fmt.Sprintf("%s-%02d", prefix, i), uint64(i+1), "turn-"+prefix,
			"read_file", status, fmt.Sprintf(`{"file_path":"pkg/f%02d.go"}`, i), 0,
		))
	}
	return items
}

// TestUserChoiceBeatsAutoExpandAndCollapseAcrossVirtualization pins the
// user's choice: a running group auto-opens, the reader's collapse keeps it
// closed while it runs and when it finishes, the reader's open keeps it open
// past the auto-close, and a choice survives scrolling the group out of the
// virtualization window and back.
func TestUserChoiceBeatsAutoExpandAndCollapseAcrossVirtualization(t *testing.T) {
	t.Parallel()
	// A running group auto-opens; the reader collapses it; it never comes
	// back on its own — not while running, not when the turn finishes.
	h := newHarness(t, 600, 400, buildLongRunningTurn("cho"))
	h.frame()
	groupKey := groupKeyOf("cho-00")
	if !h.st.list.Expanded(groupKey) {
		t.Fatal("the running group did not auto-open")
	}
	h.st.toggleUserChoice(groupKey)
	h.frame()
	if h.st.list.Expanded(groupKey) {
		t.Fatal("the reader's collapse did not stick")
	}
	h.frame()
	if h.st.list.Expanded(groupKey) {
		t.Fatal("the running group auto-re-opened over the reader's choice")
	}
	finishTurn(h, "cho")
	h.frame()
	if h.st.list.Expanded(groupKey) {
		t.Fatal("the finished group changed state over the reader's collapse")
	}

	// The choice survives the group leaving the render window and coming
	// back: two thousand items of scrolling evict the payload, the choice
	// store holds.
	h.items = append(h.items, uniformItemsF(2200, "z", 70)...)
	scrollAcross(t, h, 2260)
	idx := slices.IndexFunc(h.items, func(it *session.Item) bool { return it.ItemID == "cho-00" })
	h.st.ScrollTo(idx * estimateH)
	h.frame()
	if h.st.list.Expanded(groupKey) {
		t.Fatal("the group reopened after virtualization over the reader's choice")
	}

	// The reader's open beats the auto-collapse: a running group the reader
	// re-opened stays open when the turn finishes.
	he := newHarness(t, 600, 400, buildLongRunningTurn("ope"))
	he.frame()
	openKey := groupKeyOf("ope-00")
	he.st.toggleUserChoice(openKey) // collapse with a choice
	he.st.toggleUserChoice(openKey) // open again — still the reader's choice
	finishTurn(he, "ope")
	he.frame()
	if !he.st.list.Expanded(openKey) {
		t.Fatal("the auto-collapse overrode the reader's open")
	}
}

// groupKeyOf returns the display key the turn's work group folds under.
func groupKeyOf(firstItem string) string {
	return "wg:" + firstItem
}

// finishTurn finishes every work item of the named turn: statuses done,
// revisions bumped, the group's running flag drops.
func finishTurn(h *harness, prefix string) {
	for _, it := range h.items {
		if it.TurnID == "turn-"+prefix && it.Kind == kindToolCall {
			it.Metadata.Status = statusDone
			it.Revision++
		}
	}
}

// TestReasoningStreamsOpenAndCollapsesWhenDoneUnlessTheReaderChose pins the
// reasoning rules: a streaming reasoning item opens, collapses when done,
// and a reader choice beats both.
func TestReasoningStreamsOpenAndCollapsesWhenDoneUnlessTheReaderChose(t *testing.T) {
	t.Parallel()
	reasoning := func(id, status string) *session.Item {
		return &session.Item{TranscriptItem: client.TranscriptItem{
			ItemID: id, TurnID: "turn-r", Kind: kindReasoning, Sequence: 1, Revision: 1,
			Metadata: client.TranscriptMetadata{ReasoningState: "plain", Status: status},
			View:     client.TranscriptView{Text: "thinking"},
		}}
	}

	h := newHarness(t, 600, 400, []*session.Item{reasoning("r1", statusRunning)})
	h.frame()
	if !h.st.list.Expanded("r1") {
		t.Fatal("the streaming reasoning did not auto-open")
	}
	r1, _ := h.transcriptItem("r1")
	r1.Metadata.Status = statusDone
	r1.Revision = 2
	h.frame()
	if h.st.list.Expanded("r1") {
		t.Fatal("the finished reasoning did not auto-collapse")
	}

	// The reader collapsed it mid-stream: done never touches it again.
	h2 := newHarness(t, 600, 400, []*session.Item{reasoning("r2", statusRunning)})
	h2.frame()
	h2.st.toggleUserChoice("r2")
	if h2.st.list.Expanded("r2") {
		t.Fatal("the reader's collapse did not stick")
	}
	r2, _ := h2.transcriptItem("r2")
	r2.Metadata.Status = statusDone
	r2.Revision = 2
	h2.frame()
	if h2.st.list.Expanded("r2") {
		t.Fatal("the finished reasoning reopened over the reader's choice")
	}

	// A finished reasoning item of an old turn never moves on its own.
	h3 := newHarness(t, 600, 400, []*session.Item{reasoning("r3", statusDone)})
	h3.frame()
	if h3.st.list.Expanded("r3") {
		t.Fatal("a finished reasoning item auto-opened")
	}
}

// TestUserChoiceStoreStaysBounded pins the bound: toggling more choices than
// the store holds leaves its size at the bound — a 10,000-item transcript
// cannot grow a session's choice map without limit (F10.4d).
func TestUserChoiceStoreStaysBounded(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 600, 400, uniformItems(2600, "i"))
	h.frame()
	for i := range 2600 {
		h.st.toggleUserChoice(fmt.Sprintf("i%d", i))
	}
	if got := h.st.choices.len(); got > maxChoiceEntries {
		t.Fatalf("choice store holds %d entries, want at most %d", got, maxChoiceEntries)
	}
}
