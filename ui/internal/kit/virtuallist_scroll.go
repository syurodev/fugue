package kit

import (
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/unit"
)

// Reader-anchored scrolling (desktop-app v0.9 §5.4, "Cuộn"): the list moves
// only when the reader means it to. Besides the wheel, a drag (marking text)
// and the scroll keys end the bottom pin, an external anchor (a sent message,
// a `#n` jump, a reopened session) moves the view by item key, and rows
// changing height above the anchored row shift the offset in the same frame,
// so the reader's line never jumps.

// AnchorAt moves the view on the next Layout so key sits near the top with
// top of the preceding content still visible above it (v0.9 keeps 64 dp of
// context). The anchor rides the item key: resizes, expansions and prepended
// history keep the reader's line, and the bottom pin ends — the reader left
// the newest edge on purpose. A key the list does not hold yet stays pending
// until a frame lays it out.
func (s *VirtualListState) AnchorAt(key string, top unit.Dp) {
	s.pendingAnchorAt = key
	s.pendingAnchorDp = top
	s.following = false
}

// ResumeFollow re-engages the bottom pin (the transcript's "Latest" button):
// the next frame pins the view to the end again.
func (s *VirtualListState) ResumeFollow() {
	s.following = true
	s.dragHeld = false
}

// AnchorKey reports the key the view's reader anchor rides, empty when none
// is set: the key an AnchorAt named while it is pending, otherwise the
// anchor of the last frame (§6.26).
func (s *VirtualListState) AnchorKey() string {
	if s.pendingAnchorAt != "" {
		return s.pendingAnchorAt
	}
	return s.anchorKey
}

// applyPendingAnchorAt commits a pending external anchor with the frame's
// metrics, before any row lays out: the first paint is already anchored, so
// the reader never sees the view jump (v0.9, "Mở lại phiên").
func (s *VirtualListState) applyPendingAnchorAt(gtx *layout.Context, items []VirtualItem) {
	if s.pendingAnchorAt == "" {
		return
	}
	for _, item := range items {
		if item.Key != s.pendingAnchorAt {
			continue
		}
		s.anchorKey = s.pendingAnchorAt
		s.anchorTop = gtx.Dp(s.pendingAnchorDp)
		s.pendingAnchor = true
		s.pendingAnchorAt = ""
		return
	}
}

// holdAnchorBelow keeps the anchored row in place within the frame whose
// measurement moved it: a row at or above the anchor grew or shrank by delta,
// so everything below it — the anchored row included — shifted by delta. The
// bottom pin never runs this: it owns the view while following.
func (s *VirtualListState) holdAnchorBelow(changedRow, anchorIdx, delta int) {
	if s.following || anchorIdx < 0 || changedRow >= anchorIdx || delta == 0 {
		return
	}
	s.scrollOffset += delta
	s.prevOffset += delta
}

// anchorIndex returns the anchored key's row index, or -1 when the list holds
// no anchor.
func anchorIndex(items []VirtualItem, key string) int {
	if key == "" {
		return -1
	}
	for i, item := range items {
		if item.Key == key {
			return i
		}
	}
	return -1
}

// pollScrollKeys reads the scroll keys over the list's clip (v0.9 names the
// scroll keys a way to scroll, so their press is the reader's own act). The
// window's filters deliver them scope-wide; the composer's editor uses none
// of them, so the two never fight.
func (s *VirtualListState) pollScrollKeys(env *Env, gtx *layout.Context, viewport int) bool {
	moved := false
	for _, name := range []key.Name{key.NamePageUp, key.NamePageDown, key.NameHome, key.NameEnd} {
		for {
			e, ok := gtx.Event(key.Filter{Name: name})
			if !ok {
				break
			}
			ke, isKey := e.(key.Event)
			if !isKey || ke.State != key.Press {
				continue
			}
			s.scrollByKey(name, viewport)
			s.bar.lastAct = env.Now()
			moved = true
		}
	}
	return moved
}

func (s *VirtualListState) scrollByKey(name key.Name, viewport int) {
	// A scroll-key act lifts the marking drag's hold like a wheel does.
	if name != key.NamePageUp && name != key.NamePageDown && name != key.NameHome && name != key.NameEnd {
		return
	}
	s.dragHeld = false
	if name == key.NamePageUp || name == key.NameHome {
		if name == key.NameHome {
			s.scrollOffset = 0
		} else {
			s.scrollOffset = max(s.scrollOffset-viewport, 0)
		}
		s.following = false
		return
	}
	// PageDown and End: only landing at the newest edge re-engages the pin.
	if name == key.NameEnd {
		s.scrollOffset = s.maxOffset
	} else {
		s.scrollOffset += viewport
	}
	s.following = s.maxOffset > 0 && s.scrollOffset >= s.maxOffset
}

// dragBreakFollow handles a drag over the list: without selectable text a
// drag marks content ("bôi chữ", v0.9), so it is a reader act and ends the
// bottom pin. The drag moves no offset, so the break also holds the
// landing-at-the-bottom rule off until a scroll act lifts it.
func (s *VirtualListState) dragBreakFollow(pe pointer.Event) {
	if pe.Kind != pointer.Drag {
		return
	}
	s.following = false
	s.dragHeld = true
}
