package kit

import (
	"fmt"
	"image"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Menu, ui-kit §6.15: a floating list of items — icon, label, shortcut, an
// arrow when the item opens a submenu — on SurfaceRaised with BorderSubtle,
// radius.xl, padding 6 and elevation.float (§3.6), rows of size.row. It is
// drawn on the window overlay (UK-07, overlay.go): opened from a trigger it
// sits below the anchor, flips above it and to the anchor's right edge where
// the window runs out, and a submenu opens sideways of its item.
//
// Pointer behavior: the item under the pointer is highlighted, a submenu
// opens under the pointer and stays open while the pointer crosses the items
// below its parent on the way to it (towardSub), a click chooses an item,
// and while a root menu is open a window-wide area under its panels takes
// every press that lands on no panel: it closes the menu and the press goes
// no further (§6.15). Because Gio hands a press to the topmost area only,
// every other area of the kit is clipped to its control's box (UK-E10).
// The keyboard side is F10.11b.
//
// Motion (§3.7): a panel opens on Snappy, fading in and growing from 0.96
// toward its anchor, and closes on Exit; a closing panel takes no input; the
// highlight slides between items on Snappy. A settled menu asks for no
// frame, and a closed one draws nothing.
//
// Props and state are separate (UK-02): the caller rebuilds MenuProps every
// frame and keeps one MenuState per logical menu, built with NewMenuState.
// Items carry a stable ID, which is what a choice reports (UK-06).

// MenuItem is one row. A separator has no other field; an item with Sub opens
// a submenu instead of being chosen; a disabled item shows no interaction.
type MenuItem struct {
	ID        string
	Label     string
	Icon      IconName
	Shortcut  Shortcut
	Disabled  bool
	Sub       []MenuItem
	separator bool
}

// MenuSeparator is the hairline between groups of items (§6.15).
func MenuSeparator() MenuItem { return MenuItem{separator: true} }

// MenuProps are the per-frame, caller-owned properties. Anchor is the box
// the menu opens from — the trigger's, or an empty one at the pointer for a
// context menu — in the coordinates of the cursor Layout is called at.
// Inline draws the menu where the cursor is, open and still, instead of on
// the overlay: the gallery's specimens of a menu's states, many at a time.
type MenuProps struct {
	Items  []MenuItem
	Anchor image.Rectangle
	Inline bool
}

// MenuResult is what happened in the frame. Chosen is the ID of the item
// the user chose, Closed that a choice or a press outside closed the menu.
// Size is the footprint of an inline menu with its open submenus.
type MenuResult struct {
	Chosen string
	Closed bool
	Size   image.Point
}

// menuDepth is how many levels of submenu a menu may nest, the root included.
const menuDepth = 4

// menuRow is one row's place inside its panel's padding.
type menuRow struct{ y, h int }

// menuLevel is one panel: the root menu or a submenu.
type menuLevel struct {
	items []MenuItem
	rows  []menuRow
	open  motion.Presence
	// hiY and hiA are the highlight's place and opacity; hi is the item the
	// pointer is on (-1 none) and sub the one whose submenu is open.
	hiY, hiA   motion.Follower
	hi, sub    int
	hiYv, hiAv float32
	// rect is the panel in cursor coordinates, grown from the point from;
	// dir is the side it shifts in from, toward the anchor.
	rect      image.Rectangle
	from, dir image.Point
	pad       int
	cols      menuCols
}

// MenuState is the long-lived state of one menu (UK-02).
type MenuState struct {
	lv [menuDepth]menuLevel
	// abs is the tag of the window-wide absorbing area.
	abs bool
	// claim is set by Open and taken by the next Layout, which claims the
	// window's one overlay for this menu.
	claim bool
	// last is the previous pointer position, for the diagonal travel.
	last   image.Point
	origin image.Point

	active      int
	opener      event.Tag
	focusOnOpen bool
	openFirst   bool
	composing   bool
	typeBuf     string
	typeAt      time.Time
	dwellLevel  int
	dwellPos    image.Point
	dwellAt     time.Time
}

// NewMenuState builds the state of one menu: a zero state has no springs.
func NewMenuState() *MenuState {
	m := &MenuState{dwellLevel: -1}
	for k := range m.lv {
		m.lv[k] = newMenuLevel()
	}
	return m
}

func newMenuLevel() menuLevel {
	return menuLevel{
		open: motion.NewPresence(motion.Snappy(), motion.Exit()),
		hiY:  motion.NewFollower(motion.Snappy()), hiA: motion.NewFollower(motion.Snappy()),
		hi: -1, sub: -1,
	}
}

// Open opens the menu. A path highlights the item at its first index, and
// when that item has a submenu opens it with the next index highlighted, and
// so on: Open(2, 0) is the first item of the submenu of the third.
func (m *MenuState) Open(path ...int) {
	for k := range m.lv {
		lv := &m.lv[k]
		lv.hi, lv.sub = -1, -1
		lv.hiY, lv.hiA = motion.NewFollower(motion.Snappy()), motion.NewFollower(motion.Snappy())
		if k < len(path) {
			lv.hi, lv.sub = path[k], path[k]
		}
	}
	m.lv[0].open.Show(true)
	m.claim = true
	m.focusOnOpen = true
	m.openFirst = false
	m.active = max(0, min(len(path)-1, menuDepth-1))
	m.dwellLevel = -1
	m.typeBuf = ""
	m.composing = false
}

// SetOpener records the trigger tag that opens the menu: while the menu is
// closed, Down on a focused opener opens it on the first enabled item; when
// Escape or a choice closes the root menu, focus returns to it (§6.15).
func (m *MenuState) SetOpener(tag event.Tag) { m.opener = tag }

// OpenKeyboard opens the root menu from a keyboard trigger (Enter, Space or
// Down) with the first enabled item active (§6.15).
func (m *MenuState) OpenKeyboard() {
	m.Open()
	m.openFirst = true
}

// Close closes the menu and its submenus on Exit.
func (m *MenuState) Close() {
	for k := range m.lv {
		m.lv[k].open.Show(false)
	}
	m.dwellLevel = -1
	m.typeBuf = ""
	m.composing = false
	m.openFirst = false
	m.focusOnOpen = false
}

// IsOpen reports whether the menu was last asked to be open.
func (m *MenuState) IsOpen() bool { return m.lv[0].open.Shown() }

// Panels are the visible panels of the last Layout, root first, in window
// coordinates.
func (m *MenuState) Panels() []image.Rectangle {
	var out []image.Rectangle
	for k := range m.lv {
		if m.lv[k].open.Visible() {
			out = append(out, m.lv[k].rect.Add(m.origin))
		}
	}
	return out
}

// Layout runs the menu's frame: input, motion, placement and drawing. A
// closed menu does nothing and asks for nothing. An unknown icon is a
// programming error and comes back as an error.
func (m *MenuState) Layout(env *Env, gtx *layout.Context, p MenuProps) (MenuResult, error) {
	var res MenuResult
	if !m.lv[0].open.Visible() {
		if !p.Inline && m.opener != nil {
			m.openerInput(gtx, m.opener)
		}
		if !m.lv[0].open.Visible() {
			env.releaseOverlay(m)
			return res, nil
		}
	}
	if !p.Inline {
		if m.focusOnOpen {
			gtx.Execute(key.FocusCmd{Tag: m})
			m.focusOnOpen = false
		}
		m.syncOverlay(env)
		m.input(env, gtx, &res)
	}
	m.resolve(p.Items)
	m.advance(env, p.Inline)
	if err := m.place(env, gtx, p); err != nil {
		return res, fmt.Errorf("place the menu: %w", err)
	}
	res.Size = m.footprint()
	if err := m.draw(env, gtx, p.Inline); err != nil {
		return res, fmt.Errorf("draw the menu: %w", err)
	}
	return res, nil
}

// syncOverlay takes the window's one overlay for a menu that was just opened
// and closes one that another menu has taken it from (UK-07).
func (m *MenuState) syncOverlay(env *Env) {
	if m.claim {
		env.claimOverlay(m)
		m.claim = false
	}
	if !env.holdsOverlay(m) {
		m.Close()
	}
}

// resolve points each level at its items and opens or closes the submenus:
// a submenu is open while its parent item is still the one asked for.
func (m *MenuState) resolve(items []MenuItem) {
	cur, live := items, m.lv[0].open.Shown()
	for k := range m.lv {
		lv := &m.lv[k]
		if live || k == 0 {
			lv.items = cur
		}
		if k == 0 && m.openFirst {
			lv.hi = edgeEnabled(lv.items, true)
			m.openFirst = false
		}
		sub := live && k+1 < menuDepth && levelHasSub(cur, lv.sub)
		if !sub {
			lv.sub = -1
		}
		if k+1 < menuDepth {
			m.lv[k+1].open.Show(sub)
		}
		if sub {
			cur = cur[lv.sub].Sub
		}
		live = sub
	}
}

func levelHasSub(items []MenuItem, s int) bool {
	return s >= 0 && s < len(items) && len(items[s].Sub) > 0 && !items[s].Disabled
}

// advance moves each panel's presence, or snaps it under reduced motion.
func (m *MenuState) advance(env *Env, inline bool) {
	if inline {
		return
	}
	for k := range m.lv {
		lv := &m.lv[k]
		if env.ReducedMotion {
			lv.open.Snap(lv.open.Shown())
		} else if lv.open.Advance(env.DT()) {
			env.Keep()
		}
	}
}

// progress is how present a panel is: always fully for an inline one.
func (lv *menuLevel) progress(inline bool) float32 {
	if inline {
		return 1
	}
	return lv.open.Progress()
}

// place sizes and places every open panel: the root under the anchor,
// each submenu beside its parent item, flipping at the window's edges. A
// panel on its way out keeps the place it had.
func (m *MenuState) place(env *Env, gtx *layout.Context, p MenuProps) error {
	m.origin = env.origin
	win := env.windowRect()
	if p.Inline {
		win = image.Rect(-1<<20, -1<<20, 1<<20, 1<<20)
	}
	gap := gtx.Dp(theme.SpaceXS)
	for k := range m.lv {
		lv := &m.lv[k]
		if !lv.open.Visible() {
			break
		}
		if lv.open.Shown() {
			size, err := lv.layoutRows(env, gtx)
			if err != nil {
				return err
			}
			switch {
			case k == 0 && p.Inline:
				lv.setRect(image.Point{}, size, image.Pt(0, -1))
			case k == 0:
				pos, up := placeBelow(p.Anchor, size, win, gap)
				lv.setRect(pos, size, image.Pt(0, dirOf(up)))
			default:
				pos, left := placeBeside(m.lv[k-1].parentItem(), size, win, 0)
				lv.setRect(pos, size, image.Pt(dirOf(left), 0))
			}
		}
		lv.follow(env)
	}
	return nil
}

// dirOf is the side a flipped panel shifts in from: +1 when flipped.
func dirOf(flipped bool) int {
	if flipped {
		return 1
	}
	return -1
}

// setRect places the panel and notes the point it grows from: the edge that
// touches the anchor, centered along it.
func (lv *menuLevel) setRect(pos, size, dir image.Point) {
	lv.rect = image.Rectangle{Min: pos, Max: pos.Add(size)}
	lv.dir = dir
	lv.from = lv.rect.Min.Add(size.Div(2))
	if dir.X != 0 {
		lv.from.X = lv.rect.Min.X + (dir.X+1)/2*size.X
	}
	if dir.Y != 0 {
		lv.from.Y = lv.rect.Min.Y + (dir.Y+1)/2*size.Y
	}
}

// parentItem is the box a submenu opens beside: the row of the open item
// across the panel's width. The submenu's panel is raised by its padding, so
// its first row lines up with the item's row.
func (lv *menuLevel) parentItem() image.Rectangle {
	row := lv.rows[lv.sub]
	top := lv.rect.Min.Y + row.y
	return image.Rect(lv.rect.Min.X, top, lv.rect.Max.X, top+row.h)
}

// layoutRows measures the items into rows and returns the panel's size: the
// widest item plus the padding, never under MenuMinWidth. When any item has
// an icon every item leaves the icon column, so the labels line up (§6.15).
func (lv *menuLevel) layoutRows(env *Env, gtx *layout.Context) (image.Point, error) {
	lv.pad = gtx.Dp(theme.SpaceSM)
	lv.rows = lv.rows[:0]
	cols, err := menuColumns(env, gtx, lv.items)
	if err != nil {
		return image.Point{}, err
	}
	lv.cols = cols
	y, w := 0, gtx.Dp(theme.MenuMinWidth)-2*lv.pad
	for i := range lv.items {
		h := gtx.Dp(theme.Row)
		if lv.items[i].separator {
			h = 2*gtx.Dp(theme.SpaceXS) + gtx.Dp(theme.Hairline)
		} else {
			w = max(w, menuItemWidth(env, gtx, &lv.items[i], cols))
		}
		lv.rows = append(lv.rows, menuRow{y: y, h: h})
		y += h
	}
	return image.Pt(w+2*lv.pad, y+2*lv.pad), nil
}

// follow moves the highlight toward the item under the pointer: it slides
// on Snappy between items and fades when the pointer leaves them (§6.15).
func (lv *menuLevel) follow(env *Env) {
	target := lv.hiY.Value()
	if lv.hi >= 0 && lv.hi < len(lv.rows) {
		target = float32(lv.rows[lv.hi].y)
	}
	lv.hiYv = followTo(env, &lv.hiY, target)
	lv.hiAv = followState(env, &lv.hiA, lv.hi >= 0)
}

// footprint is the box an inline menu and its open submenus fill.
func (m *MenuState) footprint() image.Point {
	var out image.Point
	for k := range m.lv {
		if m.lv[k].open.Visible() {
			out = image.Pt(max(out.X, m.lv[k].rect.Max.X), max(out.Y, m.lv[k].rect.Max.Y))
		}
	}
	return out
}

// draw records the panels — and, for a menu on the overlay, the absorbing
// area under them — and hands them to op.Defer, so they run after every other
// op of the frame (UK-07). An inline menu is placed in the flow instead.
func (m *MenuState) draw(env *Env, gtx *layout.Context, inline bool) error {
	macro := op.Record(gtx.Ops)
	if !inline && m.lv[0].open.Shown() {
		m.absorb(env, gtx)
		area := clip.Rect(m.lv[0].rect).Push(gtx.Ops)
		event.Op(gtx.Ops, m)
		area.Pop()
	}
	var err error
	for k := range m.lv {
		if m.lv[k].open.Visible() && err == nil {
			err = m.drawLevel(env, gtx, k, inline)
		}
	}
	call := macro.Stop()
	if inline {
		call.Add(gtx.Ops)
	} else {
		op.Defer(gtx.Ops, call)
	}
	return err
}

// absorb registers the window-wide area that takes every press landing on no
// panel (§6.15). It is added before the panels, so they stay above it.
func (m *MenuState) absorb(env *Env, gtx *layout.Context) {
	defer clip.Rect(env.windowRect()).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &m.abs)
}

// drawLevel draws one panel: its hit area first, at its final box, which the
// motion never scales; then the surface, the highlight and the items under
// the fade, the grow from the anchor and the shift of §3.7.
func (m *MenuState) drawLevel(env *Env, gtx *layout.Context, k int, inline bool) error {
	lv := &m.lv[k]
	if lv.open.Shown() && !inline {
		area := clip.Rect(lv.rect).Push(gtx.Ops)
		event.Op(gtx.Ops, lv)
		area.Pop()
	}
	p := lv.progress(inline)
	shift := lv.dir.Mul(int(float32(gtx.Dp(theme.SpaceXS)) * (1 - p)))
	defer op.Offset(shift).Push(gtx.Ops).Pop()
	defer scaleAbout(gtx, f32.Pt(float32(lv.from.X), float32(lv.from.Y)), 0.96+0.04*p).Pop()
	defer paint.PushOpacity(gtx.Ops, unit01(p)).Pop()

	inner := drawMenuPanelSurface(env, gtx, lv.rect, lv.pad, lv.hiYv, lv.hiAv)
	for j := range lv.items {
		row := image.Rect(inner.Min.X, inner.Min.Y+lv.rows[j].y, inner.Max.X, inner.Min.Y+lv.rows[j].y+lv.rows[j].h)
		if err := drawMenuItem(env, gtx, &lv.items[j], row, lv.cols); err != nil {
			return err
		}
	}
	return nil
}
