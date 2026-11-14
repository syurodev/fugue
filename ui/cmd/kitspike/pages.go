package main

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/ui/internal/kitspike"
)

// keyFocusCmd builds a focus command for a tag.
func keyFocusCmd(tag any) key.FocusCmd { return key.FocusCmd{Tag: tag} }

// pageID names one gallery page.
type pageID int

const (
	pageButtons pageID = iota
	pageFields
	pageMenu
	pageSplit
	pageType
	pageIcons
	pageShadow
)

var pageNames = []string{"Button", "TextField", "Menu", "SplitPane", "Type", "Icons", "Shadow"}

// pages owns the environment, the overlay and every page's component state.
type pages struct {
	env     *kitspike.Env
	overlay *kitspike.Overlay
	cur     pageID
	nav     []*kitspike.ButtonState
	// buttons page
	btn   buttonsPage
	field fieldsPage
	menu  menuPage
	split splitPage
	typ   typ
	icons iconPage
	shad  shad
	// status line of the menu page.
	menuStatus string
}

func newPages(env *kitspike.Env) *pages {
	p := &pages{env: env, overlay: kitspike.NewOverlay(), cur: pageButtons}
	for range pageNames {
		p.nav = append(p.nav, kitspike.NewButtonState())
	}
	p.btn = newButtonsPage()
	p.field = newFieldsPage()
	p.menu = newMenuPage(env)
	p.split = newSplitPage()
	p.shad.only = -1
	return p
}

// navWidth is the sidebar width in dp.
const (
	navWidth = 140
	pagePad  = 20
)

// layout draws the whole window: nav, current page, then the overlay last.
func (p *pages) layout(gtx *layout.Context, focusField *bool, focusedOnce *bool) {
	if *focusField && !*focusedOnce {
		*focusedOnce = true
		gtx.Execute(keyFocusCmd(p.field.text.Tag()))
	}
	p.overlay.SetSize(gtx.Constraints.Max.X, gtx.Constraints.Max.Y)
	c := p.env.Th.Chrome()
	kitspike.FillRect(gtx, image.Rectangle{Max: gtx.Constraints.Max}, c.AppBg)

	// Sidebar: one button per page, stacked.
	navW := gtx.Dp(navWidth)
	for i, name := range pageNames {
		cur := p.cur == pageID(i)
		props := kitspike.ButtonProps{Text: name, Variant: kitspike.Secondary, Size: kitspike.SizeMD}
		if cur {
			// The current page reads as selected (§3.5 SelectedWash).
			props.Variant = kitspike.Primary
		}
		row := i * gtx.Dp(36)
		clicked := false
		at(gtx, image.Pt(0, gtx.Dp(pagePad)+row), func(gtx *layout.Context) layout.Dimensions {
			clicked = p.nav[i].Layout(p.env, gtx, props)
			return layout.Dimensions{}
		})
		if clicked {
			p.cur = pageID(i)
		}
		_ = navW
	}

	// Content area.
	cx := navW + gtx.Dp(pagePad)
	cw := gtx.Constraints.Max.X - cx - gtx.Dp(pagePad)
	at(gtx, image.Pt(cx, gtx.Dp(pagePad)), func(gtx *layout.Context) layout.Dimensions {
		g := *gtx
		g.Constraints = layout.Constraints{Min: image.Point{}, Max: image.Pt(cw, gtx.Constraints.Max.Y)}
		switch p.cur {
		case pageButtons:
			p.btn.layout(p.env, &g)
		case pageFields:
			p.field.layout(p.env, &g)
		case pageMenu:
			p.menuStatus = p.menu.layout(p.env, &g, p.overlay, p.menuStatus)
		case pageSplit:
			p.split.layout(p.env, &g)
		case pageType:
			p.typ.layout(p.env, &g)
		case pageIcons:
			p.icons.layout(p.env, &g)
		case pageShadow:
			p.shad.layout(p.env, &g)
		}
		return layout.Dimensions{}
	})

	// The overlay draws last, over everything (UK-07, Q2).
	p.overlay.Layout(p.env, gtx)
	if sel := p.overlay.Selected(); len(sel) > 0 {
		parts := make([]string, len(sel))
		for i, s := range sel {
			parts[i] = fmt.Sprint([]int(s))
		}
		p.menuStatus = "selected: " + strings.Join(parts, ", ")
	}
}

// buttonsPage is the variant × size × state matrix.
type buttonsPage struct {
	// states[variant][size] is the rest button; the state cells share it
	// per variant+size so the matrix stays small.
	states [4][3]*kitspike.ButtonState
}

func newButtonsPage() buttonsPage {
	var b buttonsPage
	for v := range b.states {
		for s := range b.states[v] {
			b.states[v][s] = kitspike.NewButtonState()
		}
	}
	return b
}

var cellLabels = []string{"rest", "hover", "pressed", "focus", "disabled", "busy", "error"}

// layout draws every variant × size × state (§4.1), light or dark per the
// window's theme.
func (b *buttonsPage) layout(env *kitspike.Env, gtx *layout.Context) {
	variants := []struct {
		name    string
		variant kitspike.ButtonVariant
	}{{"primary", kitspike.Primary}, {"secondary", kitspike.Secondary}, {"ghost", kitspike.Ghost}, {"danger", kitspike.Danger}}
	sizes := []struct {
		name string
		size kitspike.ButtonSize
	}{{"sm", kitspike.SizeSM}, {"md", kitspike.SizeMD}, {"lg", kitspike.SizeLG}}

	c := env.Th.Chrome()
	y := 0
	y += drawLabel(env, gtx, image.Pt(0, y), "Every variant × size × state of §4.1. Enter and Space activate; the ring shows only for keyboard focus.", c.FgMuted) + gtx.Dp(12)

	for vi, v := range variants {
		y += drawLabel(env, gtx, image.Pt(0, y), v.name, c.FgSubtle) + gtx.Dp(6)
		for si, s := range sizes {
			st := b.states[vi][si]
			props := kitspike.ButtonProps{Text: "Save", Icon: "", Variant: v.variant, Size: s.size}
			x := gtx.Dp(16)
			// The size tag sits left of the row.
			at(gtx, image.Pt(0, y), func(gtx *layout.Context) layout.Dimensions {
				drawLabel(env, gtx, image.Pt(0, 0), s.name, c.FgSubtle)
				return layout.Dimensions{}
			})
			slot := 100
			for ci := range cellLabels {
				cell := ci
				w := 0
				draw := func() { w = b.drawOne(env, gtx, image.Pt(x, y), st, props, cell) }
				// Simulate the state cells that a headless shot cannot
				// reach with a real pointer.
				switch ci {
				case 1: // hover
					old := env.SimHover
					env.SimHover = st.Tag()
					draw()
					env.SimHover = old
				case 2: // pressed
					old := env.SimPressed
					env.SimPressed = st.Tag()
					draw()
					env.SimPressed = old
				case 3: // keyboard focus
					old := env.SimFocus
					env.SimFocus = st.Tag()
					draw()
					env.SimFocus = old
				default:
					draw()
				}
				x += max(w, slot)
			}
			y += gtx.Dp(36)
		}
		y += gtx.Dp(4)
	}
}

// drawOne draws one matrix cell with the given state forced.
func (b *buttonsPage) drawOne(env *kitspike.Env, gtx *layout.Context, atP image.Point, st *kitspike.ButtonState, props kitspike.ButtonProps, cell int) int {
	var w int
	at(gtx, atP, func(gtx *layout.Context) layout.Dimensions {
		p := props
		switch cellLabels[cell] {
		case "disabled":
			p.Disabled = true
		case "busy":
			p.Busy = true
		case "error":
			p.Error = true
		}
		_ = st.Layout(env, gtx, p)
		w = 90
		return layout.Dimensions{}
	})
	return w
}

// drawLabel draws small helper text and returns its height.
func drawLabel(env *kitspike.Env, gtx *layout.Context, at image.Point, txt string, col kitspike.Color) int {
	d := kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Constraints.Max.X, txt, col)
	return d.Size.Y
}

// at is the cmd-side offset helper (mirrors kitspike's).
func at(gtx *layout.Context, p image.Point, fn func(*layout.Context) layout.Dimensions) layout.Dimensions {
	defer op.Offset(p).Push(gtx.Ops).Pop()
	return fn(gtx)
}
