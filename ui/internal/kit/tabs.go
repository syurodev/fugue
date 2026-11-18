package kit

import (
	"image"
	"slices"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TabsVariant selects between document tabs and mode tabs (§6.22).
type TabsVariant int

// The §6.22 tab variants.
const (
	TabsDocument TabsVariant = iota
	TabsMode
)

// TabItem describes one tab in a Tabs strip (§6.22).
type TabItem struct {
	ID       string
	Label    string
	Icon     IconName
	Closable bool
	Pinned   bool
	Dirty    bool
	Disabled bool
}

// TabsProps configures a Tabs strip for one frame (§6.22).
type TabsProps struct {
	Variant         TabsVariant
	Items           []TabItem
	ActiveID        string
	GlobalShortcuts bool   // allow Ctrl+Tab / Ctrl+Shift+Tab without tab-bar focus
	DraggingID      string // optional caller/preview dragging tab ID
}

// TabsEvents reports interactions on a Tabs strip in the current frame.
type TabsEvents struct {
	Selected  string
	Closed    string
	PinToggle string
	Reordered bool
	FromIndex int
	ToIndex   int
	Err       error
}

type tabSlot struct {
	id        string
	tag       bool
	x         motion.Follower
	width     motion.Follower
	closeF    motion.Follower
	presence  motion.Presence
	targetX   int
	targetW   int
	rect      image.Rectangle
	closeRect image.Rectangle
	visible   bool
}

// TabsState holds the long-lived motion, MRU, scroll, drag and overflow-menu
// state of a Tabs strip (§6.22).
type TabsState struct {
	tag         bool
	indicator   slidingPill
	focusF      motion.Follower
	slots       map[string]*tabSlot
	order       []string
	mru         []string
	scrollX     int
	hoverID     string
	pressID     string
	pressClose  string
	dragID      string
	dragStartX  int
	dragPtrX    int
	dragActive  bool
	overflowBtn *ButtonState
	overflow    *MenuState
	hiddenIDs   []string
	stripRect   image.Rectangle
	focusRingR  image.Rectangle
	dragDefer   bool
	lastActive  string
	inited      bool
}

// NewTabsState builds a TabsState at rest.
func NewTabsState() *TabsState {
	return &TabsState{
		indicator:   newSlidingPill(),
		focusF:      motion.NewFollower(motion.Snappy()),
		slots:       map[string]*tabSlot{},
		overflowBtn: NewButtonState(),
		overflow:    NewMenuState(),
	}
}

// Tag returns the Tabs strip's single tab-stop input tag (§6.22).
func (s *TabsState) Tag() event.Tag { return &s.tag }

// TabTag returns the per-tab tag for Env.Preview targeting in the gallery.
func (s *TabsState) TabTag(id string) event.Tag {
	sl := s.ensureSlot(id)
	return &sl.tag
}

// IndicatorRect returns the current sliding pill rectangle, for motion
// evidence tests.
func (s *TabsState) IndicatorRect() image.Rectangle { return s.indicator.rect }

// TabRect returns the laid-out rectangle of tab id, for focus-ring tests.
func (s *TabsState) TabRect(id string) image.Rectangle {
	if sl := s.slots[id]; sl != nil {
		return sl.rect
	}
	return image.Rectangle{}
}

// FocusRingRect returns the rectangle where the inset focus ring was last drawn.
func (s *TabsState) FocusRingRect() image.Rectangle { return s.focusRingR }

// HiddenIDs returns the IDs of tabs currently overflowing the visible strip,
// which are listed in the overflow Menu (§6.22).
func (s *TabsState) HiddenIDs() []string { return slices.Clone(s.hiddenIDs) }

// NextOnClose returns the active tab ID that should become active after closing
// closingID, using MRU order when closing the active tab and preserving
// focus when closing a background tab (§6.22).
func (s *TabsState) NextOnClose(items []TabItem, activeID, closingID string) string {
	if closingID != activeID {
		return activeID
	}
	present := make(map[string]bool, len(items))
	for _, it := range items {
		if it.ID != closingID && !it.Disabled {
			present[it.ID] = true
		}
	}
	for i := len(s.mru) - 1; i >= 0; i-- {
		if present[s.mru[i]] {
			return s.mru[i]
		}
	}
	for _, it := range items {
		if present[it.ID] {
			return it.ID
		}
	}
	return ""
}

func (s *TabsState) touchMRU(id string) {
	if id == "" {
		return
	}
	s.mru = slices.DeleteFunc(s.mru, func(v string) bool { return v == id })
	s.mru = append(s.mru, id)
}

func (s *TabsState) ensureSlot(id string) *tabSlot {
	sl, ok := s.slots[id]
	if !ok {
		sl = &tabSlot{
			id:       id,
			x:        motion.NewFollower(motion.Smooth()),
			width:    motion.NewFollower(motion.Smooth()),
			closeF:   motion.NewFollower(motion.Snappy()),
			presence: motion.NewPresence(motion.Smooth(), motion.Exit()),
		}
		if !s.inited {
			sl.presence.Snap(true)
		}
		s.slots[id] = sl
	}
	return sl
}

func orderedTabItems(v TabsVariant, items []TabItem) []TabItem {
	if v != TabsDocument {
		return items
	}
	out := make([]TabItem, 0, len(items))
	for _, it := range items {
		if it.Pinned {
			out = append(out, it)
		}
	}
	for _, it := range items {
		if !it.Pinned {
			out = append(out, it)
		}
	}
	return out
}

func (s *TabsState) syncSlots(items []TabItem) {
	seen := make(map[string]bool, len(items))
	s.order = s.order[:0]
	for _, it := range items {
		seen[it.ID] = true
		s.order = append(s.order, it.ID)
		sl := s.ensureSlot(it.ID)
		sl.presence.Show(true)
	}
	s.inited = true
	for id := range s.slots {
		if !seen[id] {
			delete(s.slots, id)
		}
	}
}

// Layout lays out the Tabs strip, handles keyboard (Ctrl+Tab/Ctrl+Shift+Tab
// for document, Left/Right arrows for mode), pointer click/drag-to-reorder,
// horizontal scroll and the overflow Menu (§6.22).
func (s *TabsState) Layout(env *Env, gtx *layout.Context, pr TabsProps) (layout.Dimensions, TabsEvents) {
	var ev TabsEvents
	if pr.ActiveID != "" && pr.ActiveID != s.lastActive {
		s.lastActive = pr.ActiveID
		s.touchMRU(pr.ActiveID)
	}
	ordered := orderedTabItems(pr.Variant, pr.Items)
	pr.Items = ordered
	s.syncSlots(ordered)

	h := gtx.Dp(theme.ControlMD)
	maxW := gtx.Constraints.Max.X
	if maxW <= 0 {
		maxW = gtx.Dp(theme.DialogWidth)
	}

	s.readInput(env, gtx, pr, &ev)
	s.layoutGeometry(env, gtx, pr, maxW, h)
	s.stepIndicator(env, pr)

	if err := s.draw(env, gtx, pr, h, &ev); err != nil {
		ev.Err = err
	}
	return layout.Dimensions{Size: image.Pt(maxW, h)}, ev
}

func (s *TabsState) readInput(env *Env, gtx *layout.Context, pr TabsProps, ev *TabsEvents) {
	if len(pr.Items) == 0 {
		return
	}
	var filters []event.Filter
	if pr.Variant == TabsDocument {
		filters = s.docFilters(pr)
	} else {
		filters = append(filters,
			key.FocusFilter{Target: s.Tag()},
			key.Filter{Focus: s.Tag(), Name: key.NameLeftArrow},
			key.Filter{Focus: s.Tag(), Name: key.NameRightArrow},
		)
	}
	filters = append(filters, pointer.Filter{
		Target:  s.Tag(),
		Kinds:   pointer.Move | pointer.Enter | pointer.Leave | pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Scroll,
		ScrollX: pointer.ScrollRange{Min: -100, Max: 100},
		ScrollY: pointer.ScrollRange{Min: -100, Max: 100},
	})

	for {
		e, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		switch ke := e.(type) {
		case key.Event:
			s.handleKeyEvent(pr, ke, ev)
		case pointer.Event:
			s.handlePointer(env, gtx, pr, ke, ev)
		}
	}
}

func (s *TabsState) handleKeyEvent(pr TabsProps, ke key.Event, ev *TabsEvents) {
	if ke.State != key.Press {
		return
	}
	step := s.keyStep(pr.Variant, ke)
	if step == 0 {
		return
	}
	if id := nextTabID(pr.Items, pr.ActiveID, step); id != "" && id != pr.ActiveID {
		ev.Selected = id
		s.touchMRU(id)
		s.ensureVisible(id)
	}
}

func (s *TabsState) keyStep(v TabsVariant, ke key.Event) int {
	if v == TabsDocument && ke.Name == key.NameTab && ke.Modifiers.Contain(key.ModCtrl) {
		if ke.Modifiers.Contain(key.ModShift) {
			return -1
		}
		return 1
	}
	if v == TabsMode {
		if ke.Name == key.NameLeftArrow {
			return -1
		}
		if ke.Name == key.NameRightArrow {
			return 1
		}
	}
	return 0
}

func (s *TabsState) handlePointer(env *Env, gtx *layout.Context, pr TabsProps, pe pointer.Event, ev *TabsEvents) {
	pt := image.Pt(int(pe.Position.X), int(pe.Position.Y))
	switch pe.Kind {
	case pointer.Move, pointer.Enter:
		s.hoverID = s.tabAt(pt)
	case pointer.Leave, pointer.Cancel:
		s.resetPointer()
	case pointer.Scroll:
		s.scrollBy(pe)
	case pointer.Press:
		s.beginPress(gtx, pr, pt)
	case pointer.Drag:
		s.dragTab(gtx, pr, pt, ev)
	case pointer.Release:
		s.finishPress(pr, pt, ev)
	default:
	}
	_ = env
}

func (s *TabsState) resetPointer() {
	s.hoverID = ""
	s.pressID = ""
	s.pressClose = ""
	s.dragID = ""
	s.dragActive = false
}

func (s *TabsState) scrollBy(pe pointer.Event) {
	delta := int(pe.Scroll.X)
	if delta == 0 {
		delta = int(pe.Scroll.Y)
	}
	s.scrollX = max(s.scrollX+delta, 0)
}

func (s *TabsState) beginPress(gtx *layout.Context, pr TabsProps, pt image.Point) {
	id := s.tabAt(pt)
	if id == "" {
		return
	}
	gtx.Execute(key.FocusCmd{Tag: s.tabFocusTag(pr.Variant, id)})
	if pr.Variant == TabsDocument {
		if sl := s.slots[id]; sl != nil && !sl.closeRect.Empty() && pt.In(sl.closeRect) {
			s.pressClose = id
			return
		}
	}
	s.pressID = id
	s.dragID = id
	s.dragStartX = pt.X
	s.dragPtrX = pt.X
	s.dragActive = false
}

func (s *TabsState) dragTab(gtx *layout.Context, pr TabsProps, pt image.Point, ev *TabsEvents) {
	if pr.Variant != TabsDocument || s.dragID == "" {
		return
	}
	s.dragPtrX = pt.X
	if !s.dragActive && absInt(pt.X-s.dragStartX) >= gtx.Dp(theme.SpaceXS) {
		s.dragActive = true
	}
	if s.dragActive {
		s.checkReorder(pr.Items, pt.X, ev)
	}
}

func (s *TabsState) finishPress(pr TabsProps, pt image.Point, ev *TabsEvents) {
	if s.pressClose != "" {
		sl := s.slots[s.pressClose]
		if sl != nil && pt.In(sl.closeRect) && !isTabDisabled(pr.Items, s.pressClose) {
			ev.Closed = s.pressClose
		}
		s.resetPointer()
		return
	}
	if !s.dragActive && s.pressID != "" && s.tabAt(pt) == s.pressID {
		if !isTabDisabled(pr.Items, s.pressID) && s.pressID != pr.ActiveID {
			ev.Selected = s.pressID
			s.touchMRU(s.pressID)
			s.ensureVisible(s.pressID)
		}
	}
	s.pressID = ""
	s.dragID = ""
	s.dragActive = false
}

func (s *TabsState) checkReorder(items []TabItem, ptrX int, ev *TabsEvents) {
	from := -1
	for i, it := range items {
		if it.ID == s.dragID {
			from = i
			break
		}
	}
	if from < 0 {
		return
	}
	for i, it := range items {
		if i == from {
			continue
		}
		sl := s.slots[it.ID]
		if sl == nil {
			continue
		}
		mid := (sl.rect.Min.X + sl.rect.Max.X) / 2
		if (i < from && ptrX < mid) || (i > from && ptrX > mid) {
			ev.Reordered = true
			ev.FromIndex = from
			ev.ToIndex = i
			return
		}
	}
}

func (s *TabsState) tabAt(pt image.Point) string {
	for _, id := range s.order {
		sl := s.slots[id]
		if sl != nil && sl.visible && pt.In(sl.rect) {
			return id
		}
	}
	return ""
}

func (s *TabsState) ensureVisible(id string) {
	sl := s.slots[id]
	if sl == nil {
		return
	}
	visW := s.stripRect.Dx()
	if visW <= 0 {
		return
	}
	if sl.targetX < s.scrollX {
		s.scrollX = sl.targetX
	} else if sl.targetX+sl.targetW > s.scrollX+visW {
		s.scrollX = sl.targetX + sl.targetW - visW
	}
}

func nextTabID(items []TabItem, activeID string, step int) string {
	n := len(items)
	if n == 0 {
		return ""
	}
	cur := 0
	for i, it := range items {
		if it.ID == activeID {
			cur = i
			break
		}
	}
	for i := 1; i <= n; i++ {
		idx := (cur + step*i + n*2) % n
		if !items[idx].Disabled {
			return items[idx].ID
		}
	}
	return ""
}

func isTabDisabled(items []TabItem, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return it.Disabled
		}
	}
	return false
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
