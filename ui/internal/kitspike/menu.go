package kitspike

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
)

// MenuItem is one entry of a Menu (§6.15). Items are props (UK-02); the
// activated ones come back as MenuSelected events (UK-06).
type MenuItem struct {
	Label     string
	Icon      string
	Shortcut  string
	Danger    bool
	Disabled  bool
	Separator bool // a divider above this item
	Submenu   []MenuItem
}

// MenuSelected reports an activated item as its index path: outer index,
// then submenu indices.
type MenuSelected []int

// menuPanel is one open floating level: the items, their state, the panel
// rectangle in window coordinates and the open/close animation. A level
// owns at most one submenu, so the whole overlay holds at most two levels
// (UK-07).
type menuPanel struct {
	items     []MenuItem
	clicks    []widget.Clickable
	scrim     widget.Clickable
	itemRects []image.Rectangle
	panel     image.Rectangle
	presence  motion.Presence
	anchor    image.Rectangle
	opener    event.Tag
	// pathBase is the index path from the root menu to this level.
	pathBase []int
	// kbd is the keyboard highlight index, -1 for none.
	kbd int
	// typeahead accumulates typed runes; cleared on any move or selection.
	typeahead string
	// sub is the open submenu, nil for none.
	sub       *menuPanel
	ownerItem int
	// grabbedFocus is set once the level has taken keyboard focus.
	grabbedFocus bool
}

// Overlay is the shared floating layer owned by the window (UK-07). The
// spike implements the menu on it; tooltip, popover, toast and dialog would
// draw through the same layer.
//
// Layout it LAST in the frame, with the transform at the window origin:
// later-registered pointer areas sit on top, and a deferred macro paints the
// panels over content drawn after them (Q2). Anchors and rects are window
// coordinates; the caller computes them from its own known offsets.
type Overlay struct {
	top *menuPanel
	// selected collects this frame's activations.
	selected []MenuSelected
	// focusBack is the opener to refocus at the end of the frame.
	focusBack event.Tag
	// w, h is the window size the panels flip against.
	w, h int
}

// NewOverlay builds the window's overlay layer.
func NewOverlay() *Overlay { return &Overlay{} }

// SetSize tells the overlay the window size for flipping.
func (o *Overlay) SetSize(w, h int) { o.w, o.h = w, h }

// Open opens a menu anchored at rect (window coordinates). opener is the
// tag Escape returns focus to.
func (o *Overlay) Open(items []MenuItem, anchor image.Rectangle, opener event.Tag) {
	o.top = &menuPanel{
		items: items, clicks: make([]widget.Clickable, len(items)),
		presence: motion.NewPresence(motion.Snappy(), motion.Exit()),
		anchor:   anchor, opener: opener, kbd: -1,
	}
	o.top.presence.Show(true)
}

// CloseAll closes every floating level; the exit animation still runs.
func (o *Overlay) CloseAll() {
	if o.top != nil {
		o.top.presence.Show(false)
	}
}

// Shown reports whether a menu is open or still animating out.
func (o *Overlay) Shown() bool { return o.top != nil }

// Selected drains the frame's activations.
func (o *Overlay) Selected() []MenuSelected {
	sel := o.selected
	o.selected = nil
	return sel
}

// Layout draws every open level and runs their input. It returns whether
// any level is still animating. Call with the transform at the window
// origin.
func (o *Overlay) Layout(env *Env, gtx *layout.Context) bool {
	// Defer the whole stack to the end of the frame so the menus paint over
	// anything laid out after them and their pointer areas register on top
	// (Q2).
	m := op.Record(gtx.Ops)
	moving := false
	if o.top != nil {
		moving = o.layoutLevel(env, gtx, o.top, true)
	}
	op.Defer(gtx.Ops, m.Stop())
	if o.focusBack != nil && !o.Shown() {
		gtx.Execute(key.FocusCmd{Tag: o.focusBack})
		o.focusBack = nil
	}
	return moving
}

// layoutLevel draws one level and its submenu chain. isRoot selects the
// placement rule: root panels flip against the window at their anchor,
// submenus sit beside their parent item.
func (o *Overlay) layoutLevel(env *Env, gtx *layout.Context, p *menuPanel, isRoot bool) bool {
	moving := p.presence.Advance(env.DT())
	env.Keep(moving)

	// Measure and place, then remember the rects: input and drawing both
	// need them.
	w, h := o.measurePanel(env, gtx, p)
	if isRoot {
		p.panel = o.placeRoot(gtx, p, w, h)
	}

	c := env.Th.Chrome()

	if p.presence.Visible() {
		prog := p.presence.Progress()

		// The root level gets the full-window click-catcher under the panel
		// (§6.15: click outside closes). While leaving, no input at all: a
		// click must pass to whatever is under the dying panel.
		if isRoot && p.presence.Shown() {
			far := 1 << 14
			s := op.Offset(image.Pt(-far, -far)).Push(gtx.Ops)
			big := *gtx
			big.Constraints = layout.Exact(image.Pt(2*far, 2*far))
			p.scrim.Layout(big, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: gtx.Constraints.Min, Baseline: 0}
			})
			s.Pop()
			if p.scrim.Clicked(*gtx) {
				o.CloseAll()
				o.focusBack = p.opener
			}
		}

		if p.presence.Shown() {
			// Take keyboard focus once, so arrows and typeahead reach the
			// menu (§6.15).
			if !p.grabbedFocus {
				p.grabbedFocus = true
				gtx.Execute(key.FocusCmd{Tag: p})
			}
			// Panel surface, fading and sliding a little from the anchor
			// (§6.15: open Snappy, close Exit).
			defer op.Offset(image.Pt(0, int(float32(dp(gtx, SpaceXS))*(1-prog)))).Push(gtx.Ops).Pop()
			defer paint.PushOpacity(gtx.Ops, min(prog*1.6, 1)).Pop()
			fillRRect(gtx.Ops, p.panel, dp(gtx, RadiusXL), c.SurfaceRaised)
			strokeRRect(gtx.Ops, p.panel, dp(gtx, RadiusXL), float32(dp(gtx, Hairline)), c.Border)

			// Items: the clickable IS the row, laid out at the item rect so
			// its pointer and focus area land on the row.
			for i := range p.items {
				it := p.items[i]
				ir := p.itemRects[i]
				if it.Separator && i > 0 {
					sy := p.panel.Min.Y + ir.Min.Y - dp(gtx, SpaceXXS)/2
					fillRect(gtx.Ops, image.Rect(p.panel.Min.X+dp(gtx, SpaceXS), sy, p.panel.Max.X-dp(gtx, SpaceXS), sy+1), c.Border)
				}
				hovered := p.clicks[i].Hovered() || p.kbd == i
				bg := transparent
				if hovered && !it.Disabled {
					bg = HoverWash(env.Th)
				}
				inset := dp(gtx, SpaceXS)
				innerR := max(dp(gtx, RadiusXL)-inset, dp(gtx, RadiusSM)) // §3.2 nested corners
				at(gtx, p.panel.Min.Add(ir.Min), func(gtx *layout.Context) layout.Dimensions {
					return p.clicks[i].Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
						fillRRect(gtx.Ops, image.Rect(0, 0, ir.Dx(), ir.Dy()), innerR, bg)
						o.drawItem(env, &gtx, p, i, ir)
						return layout.Dimensions{Size: image.Pt(ir.Dx(), ir.Dy()), Baseline: 0}
					})
				})
			}
			o.handlePointer(env, gtx, p)
			o.handleKeys(env, gtx, p)
		}
	}
	if p.sub != nil {
		sw, sh := o.measurePanel(env, gtx, p.sub)
		p.sub.panel = o.placeSub(gtx, p, p.sub, sw, sh)
		moving = o.layoutLevel(env, gtx, p.sub, false) || moving
	}
	return moving
}

// drawItem paints one row: icon, label, shortcut, submenu arrow. It runs
// under a transform at the item's top-left corner.
func (o *Overlay) drawItem(env *Env, gtx *layout.Context, p *menuPanel, i int, ir image.Rectangle) layout.Dimensions {
	c := env.Th.Chrome()
	st := env.Th.Status()
	it := p.items[i]
	fg := c.Fg
	if it.Disabled {
		fg = FgDisabled(env.Th)
	} else if it.Danger {
		fg = st.Danger
	}
	x := dp(gtx, SpaceMD)
	if it.Icon != "" {
		iconBox := dp(gtx, IconMD)
		at(gtx, image.Pt(x, (ir.Dy()-iconBox)/2), func(gtx *layout.Context) layout.Dimensions {
			_ = drawIconFresh(gtx, it.Icon, IconMD, fg)
			return layout.Dimensions{Size: image.Pt(iconBox, iconBox), Baseline: 0}
		})
		x += iconBox + dp(gtx, SpaceSM)
	}
	at(gtx, image.Pt(x, verticalCenter(gtx, TypeBody, ir.Dy())), func(gtx *layout.Context) layout.Dimensions {
		return drawText(gtx, env.Sh, TypeBody, ir.Dx()-x-dp(gtx, SpaceMD), it.Label, fg)
	})
	if it.Shortcut != "" {
		sw := measureText(gtx, env.Sh, TypeCodeSmall, 1<<20, it.Shortcut)
		at(gtx, image.Pt(ir.Dx()-dp(gtx, SpaceMD)-sw.X, verticalCenter(gtx, TypeCodeSmall, ir.Dy())), func(gtx *layout.Context) layout.Dimensions {
			return drawText(gtx, env.Sh, TypeCodeSmall, sw.X, it.Shortcut, c.FgSubtle)
		})
	}
	if len(it.Submenu) > 0 {
		iconBox := dp(gtx, IconSM)
		at(gtx, image.Pt(ir.Dx()-dp(gtx, SpaceMD)-iconBox, (ir.Dy()-iconBox)/2), func(gtx *layout.Context) layout.Dimensions {
			_ = drawIconFresh(gtx, "chevron-right", IconSM, fg)
			return layout.Dimensions{Size: image.Pt(iconBox, iconBox), Baseline: 0}
		})
	}
	return layout.Dimensions{Size: ir.Max.Sub(ir.Min), Baseline: 0}
}

// measurePanel computes the panel size and the item rects from its items.
func (o *Overlay) measurePanel(env *Env, gtx *layout.Context, p *menuPanel) (int, int) {
	inset := dp(gtx, SpaceXS)
	w := dp(gtx, 180)
	itemH := dp(gtx, Row)
	for i, it := range p.items {
		iw := 2*dp(gtx, SpaceMD) + measureText(gtx, env.Sh, TypeBody, 1<<20, it.Label).X
		if it.Icon != "" {
			iw += dp(gtx, IconMD) + dp(gtx, SpaceSM)
		}
		if it.Shortcut != "" {
			iw += measureText(gtx, env.Sh, TypeCodeSmall, 1<<20, it.Shortcut).X + dp(gtx, SpaceLG)
		}
		if len(it.Submenu) > 0 {
			iw += dp(gtx, IconSM) + dp(gtx, SpaceSM)
		}
		w = max(w, iw+2*inset)
		if i > 0 && it.Separator {
			itemH += dp(gtx, SpaceXXS)
		}
	}
	rects := make([]image.Rectangle, len(p.items))
	y := 0
	for i, it := range p.items {
		if i > 0 && it.Separator {
			y += dp(gtx, SpaceXXS)
		}
		rects[i] = image.Rect(inset, y, w-inset, y+dp(gtx, Row))
		y += dp(gtx, Row)
	}
	p.itemRects = rects
	return w, y + inset
}

// placeRoot positions a root panel at its anchor, flipping at the window
// edge (§6.15).
func (o *Overlay) placeRoot(gtx *layout.Context, p *menuPanel, w, h int) image.Rectangle {
	x := p.anchor.Min.X
	if x+w > o.w && p.anchor.Max.X-w >= 0 {
		x = p.anchor.Max.X - w
	}
	y := p.anchor.Max.Y + dp(gtx, SpaceXS)
	if y+h > o.h && p.anchor.Min.Y-h-dp(gtx, SpaceXS) >= 0 {
		y = p.anchor.Min.Y - h - dp(gtx, SpaceXS)
	}
	return image.Rect(x, y, x+w, y+h)
}

// placeSub positions a submenu beside its parent item, flipping sides.
func (o *Overlay) placeSub(gtx *layout.Context, parent, sub *menuPanel, w, h int) image.Rectangle {
	owner := parent.itemRects[sub.ownerItem]
	x := parent.panel.Max.X - 1 // overlap the border hairline
	if x+w > o.w && parent.panel.Min.X-w+1 >= 0 {
		x = parent.panel.Min.X - w + 1
	}
	y := parent.panel.Min.Y + owner.Min.Y - dp(gtx, SpaceXXS)
	if y+h > o.h {
		y = o.h - h
	}
	if y < 0 {
		y = 0
	}
	return image.Rect(x, y, x+w, y+h)
}

// handlePointer runs the pointer side of one level: item clicks, submenu
// toggling, and diagonal travel (§6.15: moving from the parent item to the
// submenu does not close it).
func (o *Overlay) handlePointer(env *Env, gtx *layout.Context, p *menuPanel) {
	hovered := -1
	for i := range p.items {
		if p.clicks[i].Hovered() {
			hovered = i
		}
	}
	for i := range p.items {
		it := p.items[i]
		if it.Disabled || !p.clicks[i].Clicked(*gtx) {
			continue
		}
		switch {
		case len(it.Submenu) > 0:
			if p.sub != nil && p.ownerItem == i && p.sub.presence.Shown() {
				o.closeSub(p, gtx)
			} else {
				o.openSub(p, i, gtx)
			}
		default:
			o.activate(p, i)
		}
	}
	// Diagonal travel: the submenu closes only when the pointer rests on a
	// different parent item (a trapezoid corridor is a kit-v1 refinement).
	if p.sub != nil && p.sub.presence.Shown() && hovered >= 0 && hovered != p.ownerItem {
		if len(p.items[hovered].Submenu) > 0 {
			o.openSub(p, hovered, gtx)
		} else {
			o.closeSub(p, gtx)
		}
	}
}

// handleKeys runs the keyboard side of one level (§6.15).
func (o *Overlay) handleKeys(env *Env, gtx *layout.Context, p *menuPanel) {
	for {
		e, ok := gtx.Event(key.Filter{Focus: p, Optional: key.ModShift | key.ModCtrl | key.ModAlt | key.ModCommand})
		if !ok {
			break
		}
		ke, isKey := e.(key.Event)
		if !isKey || ke.State != key.Press {
			continue
		}
		switch ke.Name {
		case key.NameUpArrow:
			o.highlight(p, o.nextIndex(p, p.kbd, -1), gtx)
		case key.NameDownArrow:
			o.highlight(p, o.nextIndex(p, p.kbd, +1), gtx)
		case key.NameRightArrow:
			if p.kbd >= 0 && len(p.items[p.kbd].Submenu) > 0 {
				o.openSub(p, p.kbd, gtx)
			}
		case key.NameLeftArrow:
			if p.sub != nil {
				o.closeSub(p, gtx)
			}
		case key.NameReturn, key.NameEnter:
			if p.kbd >= 0 && !p.items[p.kbd].Disabled {
				if len(p.items[p.kbd].Submenu) > 0 {
					o.openSub(p, p.kbd, gtx)
				} else {
					o.activate(p, p.kbd)
				}
			}
		case key.NameEscape:
			if p.sub != nil {
				o.closeSub(p, gtx)
			} else {
				o.CloseAll()
				o.focusBack = p.opener
			}
		default:
			// Typeahead: one-letter names jump to the next item whose label
			// starts with the typed prefix.
			if len(ke.Name) == 1 {
				p.typeahead += string(ke.Name)
				if i := o.typeaheadIndex(p); i >= 0 {
					o.highlight(p, i, gtx)
				}
			}
		}
	}
}

// activate selects item i and closes the menu.
func (o *Overlay) activate(p *menuPanel, i int) {
	full := append(append([]int{}, p.pathBase...), i)
	o.selected = append(o.selected, full)
	o.CloseAll()
	if p.opener != nil {
		o.focusBack = p.opener
	}
}

// nextIndex moves the highlight with wrap-around, skipping disabled items
// and separators (§6.15: arrows wrap).
func (o *Overlay) nextIndex(p *menuPanel, from, dir int) int {
	n := len(p.items)
	for step := 1; step <= n; step++ {
		i := ((from+dir*step)%n + n) % n
		if !p.items[i].Separator && !p.items[i].Disabled {
			return i
		}
	}
	return from
}

// typeaheadIndex finds the next item whose label starts with the typed
// prefix, searching from after the current highlight.
func (o *Overlay) typeaheadIndex(p *menuPanel) int {
	if p.typeahead == "" {
		return -1
	}
	n := len(p.items)
	start := 0
	if p.kbd >= 0 {
		start = p.kbd + 1
	}
	for step := 0; step < n; step++ {
		i := (start + step) % n
		it := p.items[i]
		if it.Disabled || it.Separator {
			continue
		}
		if len(it.Label) >= len(p.typeahead) && equalFold(it.Label[:len(p.typeahead)], p.typeahead) {
			return i
		}
	}
	return -1
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// highlight moves the keyboard highlight and focuses the panel so its key
// filters match.
func (o *Overlay) highlight(p *menuPanel, i int, gtx *layout.Context) {
	p.kbd = i
	p.typeahead = ""
	gtx.Execute(key.FocusCmd{Tag: p})
}

// openSub opens the submenu of item i.
func (o *Overlay) openSub(p *menuPanel, i int, gtx *layout.Context) {
	if p.sub != nil && p.ownerItem == i && p.sub.presence.Shown() {
		return
	}
	p.ownerItem = i
	p.sub = &menuPanel{
		items: p.items[i].Submenu, clicks: make([]widget.Clickable, len(p.items[i].Submenu)),
		presence: motion.NewPresence(motion.Snappy(), motion.Exit()),
		opener:   p.opener, kbd: -1, ownerItem: i,
		pathBase: append(append([]int{}, p.pathBase...), i),
	}
	p.sub.presence.Show(true)
	o.highlight(p, i, gtx)
}

// closeSub closes the submenu of a level and returns the highlight to its
// owner item.
func (o *Overlay) closeSub(p *menuPanel, gtx *layout.Context) {
	if p.sub != nil {
		p.sub.presence.Show(false)
		p.sub = nil
		o.highlight(p, p.ownerItem, gtx)
	}
}
