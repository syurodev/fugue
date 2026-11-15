package main

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The two component pages of the gallery (UK-09): every variant × size ×
// state in the window's mode. Every row is placed from measured sizes —
// never a fixed row height, the spike's overlapping-pages bug — and every
// drawn text run, control body and matrix cell is recorded for the
// evidence tests (UK-E03 covers the cells, UK-E09 the bounds).

// The state columns, in §4.1 order. Busy is a Button column, toggled an
// IconButton one (§6.2, §6.3).
const (
	stRest     = "rest"
	stHover    = "hover"
	stPressed  = "pressed"
	stFocus    = "focus"
	stDisabled = "disabled"
	stBusy     = "busy"
	stToggled  = "toggled"
)

// buttonStates are the Button matrix columns.
func buttonStates() []string {
	return []string{stRest, stHover, stPressed, stFocus, stDisabled, stBusy}
}

// iconStates are the IconButton matrix columns.
func iconStates() []string {
	return []string{stRest, stHover, stPressed, stFocus, stDisabled, stToggled}
}

// component names the matrix's two pages.
type component int

const (
	compButton component = iota
	compIconButton
)

// cell identifies one matrix cell of the inventory (UK-E03).
type cell struct {
	comp    component
	variant int
	size    int
	state   string
}

// pages owns the components, the nav and the per-frame recordings.
type pages struct {
	env   *kit.Env
	btns  buttonsPage
	icons iconsPage
	nav   []*kit.ButtonState
	runs  []image.Rectangle
	ctl   []image.Rectangle
	cells map[cell]bool
	// origin is the window position the page column is laid at; the
	// recorders add it, so every recorded rect is in window coordinates
	// alongside the nav's.
	origin image.Point
	// err carries the first draw error of the frame out of the layout.
	err error
}

// newPages builds the page tree over env.
func newPages(env *kit.Env) *pages {
	p := &pages{env: env, btns: newButtonsPage(), icons: newIconsPage(), cells: map[cell]bool{}}
	for range pageNames() {
		p.nav = append(p.nav, kit.NewButtonState())
	}
	return p
}

// beginFrame drops the previous frame's recordings.
func (p *pages) beginFrame() {
	p.runs = p.runs[:0]
	p.ctl = p.ctl[:0]
	clear(p.cells)
}

// caption draws one measured caption and records its text bounds.
func (p *pages) caption(gtx *layout.Context, pt image.Point, txt string, c color.NRGBA) image.Rectangle {
	dims := at(gtx, pt, func(gtx *layout.Context) layout.Dimensions {
		return kit.Label(p.env, gtx, theme.TypeSmall(), c, txt)
	})
	r := image.Rectangle{Min: pt.Add(p.origin), Max: pt.Add(p.origin).Add(dims.Size)}
	p.runs = append(p.runs, r)
	return r
}

// put lays one control at pt and records its body rect for the hit-area
// check of UK-E09.
func (p *pages) put(gtx *layout.Context, pt, box image.Point, fn func(*layout.Context)) {
	defer op.Offset(pt).Push(gtx.Ops).Pop()
	fn(gtx)
	p.ctl = append(p.ctl, image.Rectangle{Min: pt.Add(p.origin), Max: pt.Add(p.origin).Add(box)})
}

// measure lays fn into a throwaway macro with a dead input source: nothing
// paints, no event is consumed, and the size comes back for the measured
// layout.
func measure(gtx *layout.Context, fn func(*layout.Context) image.Point) image.Point {
	m := op.Record(gtx.Ops)
	g := *gtx
	var dead input.Source
	g.Source = dead
	g.Constraints = layout.Constraints{Min: image.Point{}, Max: image.Pt(1<<20, 1<<20)}
	size := fn(&g)
	m.Stop()
	return size
}

// buttonsPage is the §6.2 matrix: one ButtonState per cell — the table is
// fixed, so the indexes are the stable IDs (UK-02) — plus an icon row per
// variant with the optional icon and the busy-with-icon swap.
type buttonsPage struct {
	states [numVariants][numSizes][numButtonStates]*kit.ButtonState
	icon   [numVariants][2]*kit.ButtonState
}

// The fixed table's sizes.
const (
	numVariants     = 4
	numSizes        = 3
	numButtonStates = 6
)

func newButtonsPage() buttonsPage {
	var b buttonsPage
	for v := range b.states {
		for s := range b.states[v] {
			for c := range b.states[v][s] {
				b.states[v][s][c] = kit.NewButtonState()
			}
		}
	}
	for v := range b.icon {
		for i := range b.icon[v] {
			b.icon[v][i] = kit.NewButtonState()
		}
	}
	return b
}

// iconsPage is the §6.3 matrix: one IconButtonState per variant × state.
type iconsPage struct {
	states [2][6]*kit.IconButtonState
}

func newIconsPage() iconsPage {
	var ip iconsPage
	for v := range ip.states {
		for c := range ip.states[v] {
			ip.states[v][c] = kit.NewIconButtonState()
		}
	}
	return ip
}

// buttonCellProps are the props of one matrix cell: the state columns come
// from the props (disabled, busy) or from the preview (hover, pressed,
// focus), exactly as §4.1 stacks them.
func buttonCellProps(v kit.ButtonVariant, s kit.ButtonSize, st string) kit.ButtonProps {
	pr := kit.ButtonProps{Text: "Save", Variant: v, Size: s}
	switch st {
	case stDisabled:
		pr.Disabled = true
	case stBusy:
		pr.Busy = true
	}
	return pr
}

// layoutButtons draws the Button page from y and returns the measured end.
func (p *pages) layoutButtons(gtx *layout.Context, y int) int {
	c := p.env.Th.Chrome()
	y += p.paragraph(gtx, image.Pt(0, y), gtx.Constraints.Max.X, "Every variant × size × state of §4.1. Enter and Space activate; the ring shows only for keyboard focus.", c.FgMuted) + gtx.Dp(theme.SpaceXL)

	variants := []struct {
		name string
		v    kit.ButtonVariant
	}{{"primary", kit.Primary}, {"secondary", kit.Secondary}, {"ghost", kit.Ghost}, {"danger", kit.Danger}}
	sizes := []struct {
		name string
		s    kit.ButtonSize
	}{{"sm", kit.SizeSM}, {"md", kit.SizeMD}, {"lg", kit.SizeLG}}
	states := buttonStates()

	for vi, vr := range variants {
		y += p.caption(gtx, image.Pt(0, y), vr.name, c.FgSubtle).Dy() + gtx.Dp(theme.SpaceSM)
		colX, capH := p.measureButtonCols(gtx, vi, vr.v, sizes, states)

		// Header captions, then one measured row per size.
		for ci, st := range states {
			p.caption(gtx, image.Pt(colX[ci], y), st, c.FgSubtle)
		}
		y += capH + gtx.Dp(theme.SpaceXS)

		for _, sz := range sizes {
			y = p.buttonRow(gtx, vi, vr.v, sz.s, y, colX, states)
		}
		y = p.buttonIconRow(gtx, vi, vr.v, y)
	}
	return y
}

// measureButtonCols measures the state columns of one variant section: the
// widest of the state caption and the three size cells, so the header
// captions align with every row below.
func (p *pages) measureButtonCols(gtx *layout.Context, vi int, v kit.ButtonVariant, sizes []struct {
	name string
	s    kit.ButtonSize
}, states []string,
) ([]int, int) {
	c := p.env.Th.Chrome()
	colX := make([]int, len(states)+1)
	capH := 0
	for ci, st := range states {
		w := measure(gtx, func(gtx *layout.Context) image.Point {
			return kit.Label(p.env, gtx, theme.TypeSmall(), c.FgSubtle, st).Size
		}).X
		capH = max(capH, gtx.Sp(theme.TypeSmall().Line))
		for _, sz := range sizes {
			pr := buttonCellProps(v, sz.s, st)
			w = max(w, measure(gtx, func(gtx *layout.Context) image.Point {
				return p.btns.states[vi][sz.s][ci].Measure(p.env, gtx, pr).Size
			}).X)
		}
		colX[ci+1] = colX[ci] + w + gtx.Dp(theme.SpaceLG)
	}
	return colX, capH
}

// buttonRow draws one size row of the matrix at y and returns the next y.
func (p *pages) buttonRow(gtx *layout.Context, vi int, v kit.ButtonVariant, s kit.ButtonSize, y int, colX []int, states []string) int {
	rowH := 0
	for ci, st := range states {
		pr := buttonCellProps(v, s, st)
		box := measure(gtx, func(gtx *layout.Context) image.Point {
			return p.btns.states[vi][s][ci].Measure(p.env, gtx, pr).Size
		})
		stt := p.btns.states[vi][s][ci]
		p.force(st, stt.Tag())
		p.put(gtx, image.Pt(colX[ci], y), box, func(gtx *layout.Context) {
			if _, err := stt.Layout(p.env, gtx, pr); err != nil {
				p.err = err
			}
		})
		p.force(stRest, nil)
		p.cells[cell{compButton, vi, int(s), st}] = true
		rowH = max(rowH, box.Y)
	}
	return y + rowH + gtx.Dp(theme.SpaceMD)
}

// buttonIconRow draws the icon row of one variant section at y — the
// optional icon at rest, then busy with the icon swapped for the spinner
// inside the same width (§6.2) — and returns the next y.
func (p *pages) buttonIconRow(gtx *layout.Context, vi int, v kit.ButtonVariant, y int) int {
	iconX, iconH := 0, 0
	for i := range 2 {
		pr := kit.ButtonProps{Text: "Restart", Icon: kit.IconRestart, Variant: v, Size: kit.SizeMD}
		st := stRest
		if i == 1 {
			pr.Busy = true
			st = stBusy
		}
		box := measure(gtx, func(gtx *layout.Context) image.Point {
			return p.btns.icon[vi][i].Measure(p.env, gtx, pr).Size
		})
		p.put(gtx, image.Pt(iconX, y), box, func(gtx *layout.Context) {
			if _, err := p.btns.icon[vi][i].Layout(p.env, gtx, pr); err != nil {
				p.err = err
			}
		})
		p.cells[cell{compButton, vi, int(kit.SizeMD), st}] = true
		iconX += box.X + gtx.Dp(theme.SpaceLG)
		iconH = max(iconH, box.Y)
	}
	return y + iconH + gtx.Dp(theme.SpaceLG)
}

// layoutIcons draws the IconButton page from y and returns the measured end.
func (p *pages) layoutIcons(gtx *layout.Context, y int) int {
	c := p.env.Th.Chrome()
	y += p.paragraph(gtx, image.Pt(0, y), gtx.Constraints.Max.X, "IconButton §6.3: one icon on a box of at least size.hit.min, a required Label, and the toggled state of a pin.", c.FgMuted) + gtx.Dp(theme.SpaceXL)
	variants := []struct {
		name string
		v    kit.IconButtonVariant
	}{{"ghost", kit.IconButtonGhost}, {"secondary", kit.IconButtonSecondary}}
	states := iconStates()
	icons := []kit.IconName{kit.IconSearch, kit.IconEye, kit.IconCopy, kit.IconClose, kit.IconHelp, kit.IconChevronDown}

	for vi, vr := range variants {
		y += p.caption(gtx, image.Pt(0, y), vr.name, c.FgSubtle).Dy() + gtx.Dp(theme.SpaceSM)

		// Measure the columns once: the widest of the state caption and the
		// hit-area box, so the captions never overlap their neighbors.
		colX := make([]int, len(states)+1)
		capH := 0
		for ci, st := range states {
			w := measure(gtx, func(gtx *layout.Context) image.Point {
				return kit.Label(p.env, gtx, theme.TypeSmall(), c.FgSubtle, st).Size
			}).X
			capH = max(capH, gtx.Sp(theme.TypeSmall().Line))
			w = max(w, measure(gtx, func(gtx *layout.Context) image.Point {
				return p.icons.states[vi][ci].Measure(p.env, gtx, iconCellProps(vr.v, st, icons[ci])).Size
			}).X)
			colX[ci+1] = colX[ci] + w + gtx.Dp(theme.SpaceLG)
		}

		for ci, st := range states {
			p.caption(gtx, image.Pt(colX[ci], y), st, c.FgSubtle)
		}
		top := y + capH + gtx.Dp(theme.SpaceXS)
		for ci, st := range states {
			pr := iconCellProps(vr.v, st, icons[ci])
			box := measure(gtx, func(gtx *layout.Context) image.Point {
				return p.icons.states[vi][ci].Measure(p.env, gtx, pr).Size
			})
			stt := p.icons.states[vi][ci]
			p.force(st, stt.Tag())
			p.put(gtx, image.Pt(colX[ci], top), box, func(gtx *layout.Context) {
				if _, err := stt.Layout(p.env, gtx, pr); err != nil {
					p.err = err
				}
			})
			p.force(stRest, nil)
			p.cells[cell{compIconButton, vi, 0, st}] = true
		}
		y = top + gtx.Dp(theme.HitMin) + gtx.Dp(theme.SpaceLG)
	}
	return y
}

// iconCellProps are the props of one IconButton matrix cell.
func iconCellProps(v kit.IconButtonVariant, st string, icon kit.IconName) kit.IconButtonProps {
	pr := kit.IconButtonProps{Icon: icon, Label: "Demo " + st, Variant: v}
	switch st {
	case stDisabled:
		pr.Disabled = true
	case stToggled:
		pr.Toggled = true
	}
	return pr
}

// force aims the gallery's state preview at tag for the named column and
// clears it afterwards: one pointer cannot hover a whole matrix at once,
// so the preview renders the interaction cells (Env.Preview).
func (p *pages) force(st string, tag event.Tag) {
	p.env.Preview = kit.Preview{}
	switch st {
	case stHover:
		p.env.Preview.Hover = tag
	case stPressed:
		p.env.Preview.Press = tag
	case stFocus:
		p.env.Preview.Focus = tag
	}
}

// paragraph draws a wrapped description and records its block bounds.
func (p *pages) paragraph(gtx *layout.Context, pt image.Point, width int, txt string, c color.NRGBA) int {
	var dims layout.Dimensions
	at(gtx, pt, func(gtx *layout.Context) layout.Dimensions {
		g := *gtx
		g.Constraints.Max.X = width
		dims = kit.Paragraph(p.env, &g, theme.TypeSmall(), c, txt)
		return dims
	})
	p.runs = append(p.runs, image.Rectangle{Min: pt.Add(p.origin), Max: pt.Add(p.origin).Add(dims.Size)})
	return dims.Size.Y
}

// at runs fn with the origin moved to p.
func at(gtx *layout.Context, p image.Point, fn func(*layout.Context) layout.Dimensions) layout.Dimensions {
	defer op.Offset(p).Push(gtx.Ops).Pop()
	return fn(gtx)
}
