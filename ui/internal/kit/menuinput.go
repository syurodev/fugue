package kit

import (
	"image"
	"strings"
	"time"
	"unicode"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
)

// The input side of Menu (§6.15, §7): pointer, diagonal-travel dwell,
// keyboard navigation and IME-safe typeahead. Events are read before the
// frame lays out, against the panels of the previous frame — the ones the
// pointer was actually over — and the state they change is what the frame
// then resolves, places and draws. A menu on its way out takes no input
// (§3.7).

const (
	// diagonalDwell is how long a pointer may rest inside the diagonal-travel
	// triangle over another item before that item takes over and closes the
	// stale submenu.
	diagonalDwell = 300 * time.Millisecond
	// typeaheadReset clears the accumulated typeahead prefix after an idle
	// pause between committed characters.
	typeaheadReset = 700 * time.Millisecond
)

// openerInput watches the trigger for Down while focused, opening the menu
// on the first enabled item (§6.15).
func (m *MenuState) openerInput(gtx *layout.Context, opener event.Tag) {
	for {
		ev, ok := gtx.Event(key.Filter{Focus: opener, Name: key.NameDownArrow})
		if !ok {
			return
		}
		if ke, is := ev.(key.Event); is && ke.State == key.Press {
			m.OpenKeyboard()
			return
		}
	}
}

// input reads the frame's pointer events, the absorbing area under the root
// panel, the diagonal-travel dwell deadline and keyboard events.
func (m *MenuState) input(env *Env, gtx *layout.Context, res *MenuResult) {
	if !m.lv[0].open.Shown() {
		return
	}
	for k := range m.lv {
		if m.lv[k].open.Shown() {
			m.levelEvents(gtx, k, res)
		}
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &m.abs, Kinds: pointer.Press})
		if !ok {
			break
		}
		if pe, is := ev.(pointer.Event); is && pe.Kind == pointer.Press {
			m.Close()
			res.Closed = true
		}
	}
	m.stepDwell(env, gtx)
	m.keyInput(gtx, res)
}

// levelEvents applies panel k's pointer events in order.
func (m *MenuState) levelEvents(gtx *layout.Context, k int, res *MenuResult) {
	lv := &m.lv[k]
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: lv, Kinds: pointer.Move | pointer.Enter | pointer.Leave | pointer.Press})
		if !ok {
			return
		}
		pe, is := ev.(pointer.Event)
		if !is {
			continue
		}
		at := image.Pt(int(pe.Position.X), int(pe.Position.Y))
		switch pe.Kind {
		case pointer.Move, pointer.Enter:
			m.pointerAt(k, at, gtx.Now)
		case pointer.Leave:
			if m.dwellLevel == k {
				m.dwellLevel = -1
			}
			// The highlight stays on the parent of an open submenu while the
			// pointer travels to it.
			if lv.sub < 0 {
				lv.hi = -1
			}
		case pointer.Press:
			m.press(k, at, res)
		case pointer.Release, pointer.Cancel, pointer.Drag, pointer.Scroll:
		}
	}
}

// pointerAt highlights the item under the pointer and opens its submenu, or
// closes the open one — unless the pointer is on its way to that submenu, in
// which case a dwell deadline is armed so stopping over another item still
// hands over after diagonalDwell.
func (m *MenuState) pointerAt(k int, p image.Point, now time.Time) {
	m.active = k
	prev := m.last
	m.last = p
	if m.towardSub(k, p, prev) {
		m.dwellLevel, m.dwellPos, m.dwellAt = k, p, now.Add(diagonalDwell)
		return
	}
	m.dwellLevel = -1
	m.applyPointer(k, p)
}

// applyPointer commits the item under p in panel k as active and updates its
// submenu.
func (m *MenuState) applyPointer(k int, p image.Point) {
	lv := &m.lv[k]
	idx := lv.itemAt(p)
	if idx >= 0 && !itemEnabled(&lv.items[idx]) {
		idx = -1
	}
	lv.hi, lv.sub = idx, -1
	if idx >= 0 && len(lv.items[idx].Sub) > 0 {
		lv.sub = idx
	}
}

// stepDwell fires the armed diagonal-travel dwell once gtx.Now reaches
// dwellAt, or asks the driver to wake at dwellAt.
func (m *MenuState) stepDwell(env *Env, gtx *layout.Context) {
	if m.dwellLevel < 0 {
		return
	}
	if !gtx.Now.Before(m.dwellAt) {
		m.applyPointer(m.dwellLevel, m.dwellPos)
		m.dwellLevel = -1
		return
	}
	env.Driver.WakeAt(m.dwellAt)
}

// press chooses the item under the pointer, or opens its submenu; a press on
// padding, a separator or a disabled item changes nothing and goes no further
// than the panel.
func (m *MenuState) press(k int, p image.Point, res *MenuResult) {
	m.active = k
	m.dwellLevel = -1
	lv := &m.lv[k]
	idx := lv.itemAt(p)
	if idx < 0 || !itemEnabled(&lv.items[idx]) {
		return
	}
	if len(lv.items[idx].Sub) > 0 {
		lv.hi, lv.sub = idx, idx
		return
	}
	res.Chosen, res.Closed = lv.items[idx].ID, true
	m.Close()
}

// keyInput drains focus, IME and navigation events for the open menu.
// Committed text arrives as key.EditEvent through key.FocusFilter so IMEs
// (including Vietnamese Telex) work; active composition is ignored.
func (m *MenuState) keyInput(gtx *layout.Context, res *MenuResult) {
	filters := []event.Filter{
		key.FocusFilter{Target: m},
		key.Filter{Focus: m, Name: key.NameUpArrow},
		key.Filter{Focus: m, Name: key.NameDownArrow},
		key.Filter{Focus: m, Name: key.NameLeftArrow},
		key.Filter{Focus: m, Name: key.NameRightArrow},
		key.Filter{Focus: m, Name: key.NameHome},
		key.Filter{Focus: m, Name: key.NameEnd},
		key.Filter{Focus: m, Name: key.NameReturn},
		key.Filter{Focus: m, Name: key.NameEnter},
		key.Filter{Focus: m, Name: key.NameSpace},
		key.Filter{Focus: m, Name: key.NameEscape},
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		switch ke := ev.(type) {
		case key.FocusEvent:
			m.composing = false
		case key.CompositionEvent:
			m.composing = ke.Start >= 0 && ke.End > ke.Start
		case key.EditEvent:
			if !m.composing && ke.Text != "" {
				m.typeahead(gtx.Now, ke.Text)
			}
		case key.Event:
			if ke.State == key.Press {
				m.handleKey(gtx, ke.Name, res)
			}
		}
	}
}

// handleKey dispatches one pressed navigation or action key.
func (m *MenuState) handleKey(gtx *layout.Context, name key.Name, res *MenuResult) {
	switch string(name) {
	case string(key.NameUpArrow):
		m.moveActive(-1)
	case string(key.NameDownArrow):
		m.moveActive(1)
	case string(key.NameHome):
		m.jumpEdge(true)
	case string(key.NameEnd):
		m.jumpEdge(false)
	case string(key.NameRightArrow):
		m.openSub()
	case string(key.NameLeftArrow):
		m.closeSub()
	case string(key.NameEscape):
		m.escape(gtx, res)
	case string(key.NameReturn), string(key.NameEnter), string(key.NameSpace):
		m.chooseActive(gtx, res)
	}
}

// moveActive steps the highlight by step (+1 or -1) on the active level,
// wrapping around and skipping disabled items and separators.
func (m *MenuState) moveActive(step int) {
	lv := &m.lv[m.active]
	n := len(lv.items)
	if n == 0 {
		return
	}
	idx := 0
	if step < 0 {
		idx = n - 1
	}
	if lv.hi >= 0 {
		idx = (lv.hi + step + n) % n
	}
	for range n {
		if itemEnabled(&lv.items[idx]) {
			lv.hi, lv.sub = idx, -1
			return
		}
		idx = (idx + step + n) % n
	}
}

// jumpEdge moves the highlight to the first or last enabled item.
func (m *MenuState) jumpEdge(first bool) {
	lv := &m.lv[m.active]
	if idx := edgeEnabled(lv.items, first); idx >= 0 {
		lv.hi, lv.sub = idx, -1
	}
}

// openSub opens the active item's submenu and moves keyboard focus into its
// first enabled item.
func (m *MenuState) openSub() {
	lv := &m.lv[m.active]
	if lv.hi < 0 || lv.hi >= len(lv.items) || m.active+1 >= menuDepth {
		return
	}
	it := &lv.items[lv.hi]
	if !itemEnabled(it) || len(it.Sub) == 0 {
		return
	}
	lv.sub = lv.hi
	m.active++
	m.lv[m.active].hi, m.lv[m.active].sub = edgeEnabled(it.Sub, true), -1
}

// closeSub closes the innermost open submenu and returns to its parent item.
func (m *MenuState) closeSub() {
	if m.active <= 0 {
		return
	}
	m.active--
	m.lv[m.active].sub = -1
}

// escape closes the innermost open panel; when the root closes, focus returns
// to the control that opened the menu (§6.15, UK-05).
func (m *MenuState) escape(gtx *layout.Context, res *MenuResult) {
	if m.active > 0 {
		m.closeSub()
		return
	}
	opener := m.opener
	m.Close()
	res.Closed = true
	if opener != nil {
		gtx.Execute(key.FocusCmd{Tag: opener})
	}
}

// chooseActive chooses the active leaf item or opens its submenu.
func (m *MenuState) chooseActive(gtx *layout.Context, res *MenuResult) {
	lv := &m.lv[m.active]
	if lv.hi < 0 || lv.hi >= len(lv.items) || !itemEnabled(&lv.items[lv.hi]) {
		return
	}
	if len(lv.items[lv.hi].Sub) > 0 {
		m.openSub()
		return
	}
	res.Chosen, res.Closed = lv.items[lv.hi].ID, true
	opener := m.opener
	m.Close()
	if opener != nil {
		gtx.Execute(key.FocusCmd{Tag: opener})
	}
}

// typeahead appends committed text (resetting after typeaheadReset) and jumps
// to the next enabled item whose folded label starts with the prefix.
func (m *MenuState) typeahead(now time.Time, txt string) {
	if m.typeAt.IsZero() || now.Sub(m.typeAt) > typeaheadReset {
		m.typeBuf = ""
	}
	m.typeBuf += txt
	m.typeAt = now
	prefix := foldTypeahead(m.typeBuf)
	if prefix == "" {
		return
	}
	lv := &m.lv[m.active]
	n := len(lv.items)
	if n == 0 {
		return
	}
	if len([]rune(prefix)) > 1 && lv.hi >= 0 && lv.hi < n && matchesPrefix(&lv.items[lv.hi], prefix) {
		lv.sub = -1
		return
	}
	start := (lv.hi + 1) % n
	for i := range n {
		idx := (start + i) % n
		if matchesPrefix(&lv.items[idx], prefix) {
			lv.hi, lv.sub = idx, -1
			return
		}
	}
}

func matchesPrefix(it *MenuItem, prefix string) bool {
	return itemEnabled(it) && strings.HasPrefix(foldTypeahead(it.Label), prefix)
}

// foldTypeahead normalizes labels and typed prefixes for menu typeahead:
// lowercased, combining marks (unicode.Mn) dropped, 'đ'/'Đ' folded to 'd',
// and precomposed Vietnamese and Latin accented letters mapped to their ASCII
// base letter (so both "do" and "đổ" match "Đổi tên").
func foldTypeahead(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(foldRune(unicode.ToLower(r)))
	}
	return b.String()
}

// foldRune maps a lowercased Vietnamese or Latin accented rune to its base
// ASCII letter; unmapped runes are returned unchanged.
func foldRune(r rune) rune {
	for g := range strings.SplitSeq(foldedGroups, "|") {
		if strings.ContainsRune(g[2:], r) {
			return rune(g[0])
		}
	}
	return r
}

// foldedGroups pairs each ASCII base letter with the lowercased precomposed
// Vietnamese and common Latin variants that fold to it.
const foldedGroups = "a:àáảãạăằắẳẵặâầấẩẫậäå|c:çćč|d:đð|e:èéẻẽẹêềếểễệë|i:ìíỉĩịîï|n:ñń|o:òóỏõọôồốổỗộơờớởỡợöø|s:śš|u:ùúủũụưừứửữựûü|y:ỳýỷỹỵÿ|z:źž"

func itemEnabled(it *MenuItem) bool { return !it.Disabled && !it.separator }

func edgeEnabled(items []MenuItem, first bool) int {
	for i := range items {
		idx := i
		if !first {
			idx = len(items) - 1 - i
		}
		if itemEnabled(&items[idx]) {
			return idx
		}
	}
	return -1
}

// itemAt is the row under p, or -1 over the padding.
func (lv *menuLevel) itemAt(p image.Point) int {
	inner := lv.rect.Inset(lv.pad)
	if !p.In(inner) {
		return -1
	}
	for i, r := range lv.rows {
		if p.Y-inner.Min.Y < r.y+r.h {
			return i
		}
	}
	return -1
}

// towardSub reports whether p lies on the way from prev to the open submenu
// of level k: inside the triangle of prev and the submenu's near edge. A
// pointer crossing the items below the parent on a diagonal toward the
// submenu then keeps it open instead of closing it (§6.15).
func (m *MenuState) towardSub(k int, p, prev image.Point) bool {
	if k+1 >= menuDepth || m.lv[k].sub < 0 || !m.lv[k+1].open.Shown() {
		return false
	}
	sub := &m.lv[k+1]
	x := sub.rect.Min.X
	if sub.dir.X > 0 {
		x = sub.rect.Max.X
	}
	return inTriangle(p, prev, image.Pt(x, sub.rect.Min.Y), image.Pt(x, sub.rect.Max.Y))
}

// inTriangle reports whether p is inside or on the triangle abc, by the sign
// of p against each edge.
func inTriangle(p, a, b, c image.Point) bool {
	d1, d2, d3 := edgeSide(p, a, b), edgeSide(p, b, c), edgeSide(p, c, a)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !neg || !pos
}

// edgeSide is the sign of the cross product that tells which side of the edge
// ab the point p is on.
func edgeSide(p, a, b image.Point) int {
	return (p.X-b.X)*(a.Y-b.Y) - (a.X-b.X)*(p.Y-b.Y)
}
