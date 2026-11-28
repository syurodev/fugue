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
// state in the window's mode. A page is a flow of variant blocks: each block
// is measured first, then placed left to right and wrapped to the next line
// when it would pass the page width, so a page never runs off its column
// whatever the zoom — it grows downward instead. Every row is placed from
// measured sizes, never a fixed height (the spike's overlapping-pages bug),
// and every drawn text run, control and matrix cell is recorded for the
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
	compTextField
	compTextArea
	compMenu
	compKbd
	compCombobox
	compSelect
	compTooltip
	compListRow
	compSectionHeader
	compScrollArea
	compVirtualList
	compTreeView
	compTag
	compLineRow
	compProgress
	compDivider
	compSplitPane
	compPopover
	compDialog
	compToast
	compCheckbox
	compSwitch
	compSegmented
	compCard
	compTabs
	compBanner
	compEmptyState
	compFilterBar
	compCompareView
)

// cell identifies one matrix cell of the inventory (UK-E03).
type cell struct {
	comp    component
	variant int
	size    int
	state   string
}

// The fixed tables of the matrices.
const (
	numVariants     = 5
	numIconVariants = 3
	numSizes        = 4
	numStates       = 6
)

// buttonVariantNames and iconVariantNames label the blocks, in enum order.
func buttonVariantNames() []string {
	return []string{"secondary (default)", "primary", "bordered", "ghost", "danger"}
}
func iconVariantNames() []string { return []string{"ghost (default)", "secondary", "bordered"} }

// pages owns the components, the nav and the per-frame recordings.
type pages struct {
	env      *kit.Env
	btns     [numVariants][numSizes][numStates]*kit.ButtonState
	icon     [numVariants][2]*kit.ButtonState
	icons    [numIconVariants][numStates]*kit.IconButtonState
	text     [numFieldVariants][numFieldStates]*kit.TextFieldState
	area     [numAreaStates]*kit.TextAreaState
	menu     menuPage
	combobox comboboxPage
	sel      selectPage
	tip      tooltipPage
	list     listRowPage
	section  sectionHeaderPage
	scroll   scrollAreaPage
	vl       virtualListPage
	tree     treeViewPage
	f1012    f1012Pages
	pop      popoverPage
	dlg      dialogPage
	tst      toastPage
	ctrl     controlsPage
	tf       tabsFeedbackPage
	fb       filterBarPage
	cv       compareViewPage
	nav      []*kit.ButtonState
	// runs are the page-level text runs and ctl the control boxes of the
	// frame, in window coordinates; ctlRuns are the runs drawn inside a
	// control — labels, placeholders, helper and error lines — which the
	// strict bounds check holds inside their control's box (UK-E09).
	// cells is the matrix inventory.
	runs    []image.Rectangle
	ctlRuns []image.Rectangle
	ctl     []image.Rectangle
	cells   map[cell]bool
	// origin is the window position of the page column; base is the window
	// position of the block being drawn. Recorders add base.
	origin, base image.Point
	// err carries the first draw error of the frame out of the layout.
	err error
}

// newPages builds the page tree over env: one state per matrix cell — the
// tables are fixed, so the indexes are the stable IDs (UK-02).
func newPages(env *kit.Env) *pages {
	p := &pages{env: env, cells: map[cell]bool{}}
	p.newButtonStates()
	p.newFieldStates()
	p.menu = newMenuPage()
	p.combobox = newComboboxPage()
	p.sel = newSelectPage()
	p.tip = newTooltipPage()
	p.list = newListRowPage()
	p.section = newSectionHeaderPage()
	p.scroll = newScrollAreaPage()
	p.vl = newVirtualListPage()
	p.tree = newTreeViewPage()
	p.f1012 = newF1012Pages()
	p.pop = newPopoverPage()
	p.dlg = newDialogPage()
	p.tst = newToastPage()
	p.ctrl = newControlsPage()
	p.tf = newTabsFeedbackPage()
	p.fb = newFilterBarPage()
	p.cv = newCompareViewPage()
	for range pageNames() {
		p.nav = append(p.nav, kit.NewButtonState())
	}
	return p
}

// newButtonStates allocates the Button and IconButton matrices.
func (p *pages) newButtonStates() {
	for v := range p.btns {
		for s := range p.btns[v] {
			for c := range p.btns[v][s] {
				p.btns[v][s][c] = kit.NewButtonState()
			}
		}
		for i := range p.icon[v] {
			p.icon[v][i] = kit.NewButtonState()
		}
	}
	for v := range p.icons {
		for c := range p.icons[v] {
			p.icons[v][c] = kit.NewIconButtonState()
		}
	}
}

// beginFrame drops the previous frame's recordings.
func (p *pages) beginFrame() {
	p.runs = p.runs[:0]
	p.ctlRuns = p.ctlRuns[:0]
	p.ctl = p.ctl[:0]
	clear(p.cells)
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

// caption draws one caption at pt (block coordinates) and records its text
// bounds.
func (p *pages) caption(gtx *layout.Context, pt image.Point, txt string, c color.NRGBA) image.Rectangle {
	var dims layout.Dimensions
	at(gtx, pt, func(gtx *layout.Context) {
		dims = kit.Label(p.env, gtx, theme.TypeSmall(), c, txt)
	})
	r := image.Rectangle{Min: pt.Add(p.base), Max: pt.Add(p.base).Add(dims.Size)}
	p.runs = append(p.runs, r)
	return r
}

// captionWidth is the laid-out width of a caption.
func (p *pages) captionWidth(gtx *layout.Context, txt string) int {
	return measure(gtx, func(gtx *layout.Context) image.Point {
		return kit.Label(p.env, gtx, theme.TypeSmall(), p.env.Th.Chrome().FgSubtle, txt).Size
	}).X
}

// paragraph draws a wrapped description at the top of the column and returns
// its height; the block coordinates are the column's.
func (p *pages) paragraph(gtx *layout.Context, width int, txt string) int {
	var dims layout.Dimensions
	g := *gtx
	g.Constraints.Max.X = width
	dims = kit.Paragraph(p.env, &g, theme.TypeSmall(), p.env.Th.Chrome().FgMuted, txt)
	p.runs = append(p.runs, image.Rectangle{Min: p.base, Max: p.base.Add(dims.Size)})
	return dims.Size.Y
}

// cellDraw is one control of a matrix block: the column it sits in, its
// measured box, its inventory key, the tag the state preview aims at and
// the layout call.
type cellDraw struct {
	col int
	box image.Point
	key cell
	tag event.Tag
	lay func(*layout.Context)
}

// place lays one cell at pt (block coordinates) under its state's forced
// preview and records its control box. While the cell lays, a run recorder
// on the env collects the bounds of every text run the control draws,
// local to the cell; they are translated into window coordinates and held
// against the control's box by the strict bounds check (UK-E09).
func (p *pages) place(gtx *layout.Context, pt image.Point, cd *cellDraw) {
	p.force(cd.key.state, cd.tag)
	var cellRuns []image.Rectangle
	p.env.RunRecorder = func(r image.Rectangle) { cellRuns = append(cellRuns, r) }
	p.env.Offset(gtx, pt, cd.lay)
	p.env.RunRecorder = nil
	p.force(stRest, nil)
	box := image.Rectangle{Min: pt.Add(p.base), Max: pt.Add(p.base).Add(cd.box)}
	for _, r := range cellRuns {
		p.ctlRuns = append(p.ctlRuns, r.Add(pt).Add(p.base))
	}
	p.ctl = append(p.ctl, box)
	p.cells[cd.key] = true
}

// force aims the gallery's state preview at tag for the named column and
// clears it afterwards: one pointer cannot hover a whole matrix at once, so
// the preview renders the interaction cells (Env.Preview).
func (p *pages) force(st string, tag event.Tag) {
	p.env.Preview = kit.Preview{}
	switch st {
	case stHover:
		p.env.Preview.Hover = tag
	case stPressed:
		p.env.Preview.Press = tag
	case stFocus, dlgFormFocus, dlgSafeFocus:
		p.env.Preview.Focus = tag
	case selOpen:
		p.env.Preview.Open = tag
	}
}

// matrix lays one titled block: the title, the state captions over the
// columns, then the rows of cells, each column as wide as its widest cell or
// caption. With dry set it only measures and returns the block's size.
func (p *pages) matrix(gtx *layout.Context, title string, states []string, rows [][]cellDraw, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap, colGap := gtx.Dp(theme.SpaceLG), gtx.Dp(theme.SpaceLG)

	colW := make([]int, len(states))
	for ci, st := range states {
		colW[ci] = p.captionWidth(gtx, st)
	}
	for _, r := range rows {
		for _, cd := range r {
			colW[cd.col] = max(colW[cd.col], cd.box.X)
		}
	}
	colX := make([]int, len(states))
	x := 0
	for ci := range states {
		colX[ci] = x
		x += colW[ci] + colGap
	}
	w := max(x-colGap, p.captionWidth(gtx, title))

	y := 0
	if !dry {
		p.caption(gtx, image.Pt(0, y), title, c.FgMuted)
	}
	y += line + gtx.Dp(theme.SpaceSM)
	if !dry {
		for ci, st := range states {
			p.caption(gtx, image.Pt(colX[ci], y), st, c.FgSubtle)
		}
	}
	y += line + gtx.Dp(theme.SpaceSM)
	for _, r := range rows {
		rowH := 0
		for i := range r {
			rowH = max(rowH, r[i].box.Y)
			if !dry {
				p.place(gtx, image.Pt(colX[r[i].col], y), &r[i])
			}
		}
		y += rowH + gap
	}
	return image.Pt(w, y-gap)
}

// block lays one matrix block at the current offset, or measures it.
type block func(gtx *layout.Context, dry bool) image.Point

// flow lays the blocks left to right from y, wrapping to a new line when the
// next block would pass the column's width.
func (p *pages) flow(gtx *layout.Context, y int, blocks []block) {
	width, gap := gtx.Constraints.Max.X, gtx.Dp(theme.SpaceXXL)
	x, rowH := 0, 0
	for _, b := range blocks {
		size := b(gtx, true)
		if x > 0 && x+size.X > width {
			x, y, rowH = 0, y+rowH+gap, 0
		}
		p.base = p.origin.Add(image.Pt(x, y))
		p.env.Offset(gtx, image.Pt(x, y), func(gtx *layout.Context) { b(gtx, false) })
		x += size.X + gap
		rowH = max(rowH, size.Y)
	}
}

// layoutButtons draws the Button page: one block per variant.
func (p *pages) layoutButtons(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Every variant × size × state of §4.1. Enter and Space activate; the ring shows only for keyboard focus. A real press shrinks the drawing and sends a ripple: hold a button, in slow motion (S) to watch it.") + gtx.Dp(theme.SpaceXL)
	blocks := make([]block, 0, numVariants)
	for vi, name := range buttonVariantNames() {
		blocks = append(blocks, func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, name, buttonStates(), p.buttonRows(gtx, vi), dry)
		})
	}
	p.flow(gtx, y, blocks)
}

// layoutIcons draws the IconButton page: one block per variant.
func (p *pages) layoutIcons(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "IconButton §6.3: one icon on a size.control square, a required Label, and the toggled state of a pin.") + gtx.Dp(theme.SpaceXL)
	blocks := make([]block, 0, numIconVariants)
	for vi, name := range iconVariantNames() {
		blocks = append(blocks, func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, name, iconStates(), [][]cellDraw{p.iconRow(gtx, vi)}, dry)
		})
	}
	p.flow(gtx, y, blocks)
}

// buttonRows are the rows of one variant block: a row per size with every
// state, then the icon row — the optional icon at rest and busy with the
// icon swapped for the spinner inside the same width (§6.2).
func (p *pages) buttonRows(gtx *layout.Context, vi int) [][]cellDraw {
	v := kit.ButtonVariant(vi)
	rows := make([][]cellDraw, 0, numSizes+1)
	for _, s := range []kit.ButtonSize{kit.SizeXS, kit.SizeSM, kit.SizeMD, kit.SizeLG} {
		row := make([]cellDraw, 0, numStates)
		for ci, st := range buttonStates() {
			row = append(row, p.buttonCell(gtx, p.btns[vi][s][ci], cell{compButton, vi, int(s), st}, ci,
				buttonCellProps(v, s, st, "")))
		}
		rows = append(rows, row)
	}
	row := make([]cellDraw, 0, 2)
	for i, col := range []int{0, 5} {
		st := buttonStates()[col]
		row = append(row, p.buttonCell(gtx, p.icon[vi][i], cell{compButton, vi, int(kit.SizeMD), st}, col,
			buttonCellProps(v, kit.SizeMD, st, kit.IconRestart)))
	}
	return append(rows, row)
}

// buttonCell measures one Button cell and wraps its layout.
func (p *pages) buttonCell(gtx *layout.Context, st *kit.ButtonState, key cell, col int, pr kit.ButtonProps) cellDraw {
	return cellDraw{
		col: col, key: key, tag: st.Tag(),
		box: measure(gtx, func(gtx *layout.Context) image.Point { return st.Measure(p.env, gtx, pr).Size }),
		lay: func(gtx *layout.Context) {
			if _, err := st.Layout(p.env, gtx, pr); err != nil {
				p.err = err
			}
		},
	}
}

// buttonCellProps are the props of one matrix cell: the state columns come
// from the props (disabled, busy) or from the preview (hover, pressed,
// focus), exactly as §4.1 stacks them.
func buttonCellProps(v kit.ButtonVariant, s kit.ButtonSize, st string, icon kit.IconName) kit.ButtonProps {
	pr := kit.ButtonProps{Text: "Save", Icon: icon, Variant: v, Size: s}
	if icon != "" {
		pr.Text = "Run"
	}
	switch st {
	case stDisabled:
		pr.Disabled = true
	case stBusy:
		pr.Busy = true
	}
	return pr
}

// iconRow is the one row of an IconButton block: every state of the variant.
func (p *pages) iconRow(gtx *layout.Context, vi int) []cellDraw {
	icons := []kit.IconName{kit.IconSearch, kit.IconEye, kit.IconCopy, kit.IconClose, kit.IconHelp, kit.IconChevronDown}
	row := make([]cellDraw, 0, numStates)
	for ci, st := range iconStates() {
		s := p.icons[vi][ci]
		pr := &kit.IconButtonProps{
			Icon: icons[ci], Label: "Demo " + st, Variant: kit.IconButtonVariant(vi),
			Disabled: st == stDisabled, Toggled: st == stToggled,
		}
		row = append(row, cellDraw{
			col: ci, key: cell{compIconButton, vi, 0, st}, tag: s.Tag(),
			box: measure(gtx, func(gtx *layout.Context) image.Point { return s.Measure(p.env, gtx, pr).Size }),
			lay: func(gtx *layout.Context) {
				if _, err := s.Layout(p.env, gtx, pr); err != nil {
					p.err = err
				}
			},
		})
	}
	return row
}

// at runs fn with the origin moved to p.
func at(gtx *layout.Context, p image.Point, fn func(*layout.Context)) {
	defer op.Offset(p).Push(gtx.Ops).Pop()
	fn(gtx)
}
