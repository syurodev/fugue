package mdview

import (
	"image"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"golang.org/x/image/math/fixed"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	defaultMaxCodeLines      = 30
	maxExpandedViewportLines = 60
	copiedFlashDuration      = 1500 * time.Millisecond
)

// CodeBlockProps configures one code block, Output card, or file_change diff
// for the current frame (desktop-app v0.9 §5.4).
type CodeBlockProps struct {
	Title           string
	Format          string
	Icon            kit.IconName
	IsOutputCard    bool
	IsDiff          bool
	Raw             string
	Lines           [][]markdown.Span
	Truncated       bool
	MaxLines        int
	ShowLineNumbers bool
}

// CodeBlockResult reports user actions triggered on a code block this frame.
type CodeBlockResult struct {
	Copied      bool
	WrapToggled bool
	RequestFull bool
}

// CodeBlockState holds persistent UI state for one code block across frames
// and item revisions: wrap preference, expansion, scroll offsets, copy flash,
// hover/focus state, and button states.
type CodeBlockState struct {
	Wrapped     bool
	Expanded    bool
	ScrollX     int
	ScrollY     int
	CopiedUntil time.Time

	hovered    bool
	focusTag   bool
	copyBtn    *kit.ButtonState
	wrapBtn    *kit.IconButtonState
	expandBtn  *kit.ButtonState
	maxScrollX int
	maxScrollY int
	viewW      int
	viewH      int

	cachedFormat string
	cachedRaw    string
	cachedLines  [][]markdown.Span
	cachedDiff   []DiffLine
}

// NewCodeBlockState builds a CodeBlockState at its default unwrapped, collapsed state.
func NewCodeBlockState() *CodeBlockState {
	return &CodeBlockState{
		copyBtn:   kit.NewButtonState(),
		wrapBtn:   kit.NewIconButtonState(),
		expandBtn: kit.NewButtonState(),
	}
}

func (s *CodeBlockState) ensureButtons() {
	if s.copyBtn == nil {
		s.copyBtn = kit.NewButtonState()
	}
	if s.wrapBtn == nil {
		s.wrapBtn = kit.NewIconButtonState()
	}
	if s.expandBtn == nil {
		s.expandBtn = kit.NewButtonState()
	}
}

// FocusTag returns the code block's keyboard focus tag for tests and focus control.
func (s *CodeBlockState) FocusTag() event.Tag { return &s.focusTag }

// Hovered reports whether the pointer is currently over the code block.
func (s *CodeBlockState) Hovered() bool { return s.hovered }

// ActionsVisible reports whether Copy and Wrap controls are currently shown
// (on hover, keyboard focus, or active Copied confirmation).
func (s *CodeBlockState) ActionsVisible(env *kit.Env, gtx *layout.Context) bool {
	s.ensureButtons()
	return s.hovered ||
		gtx.Focused(&s.focusTag) ||
		gtx.Focused(s.copyBtn.Tag()) ||
		gtx.Focused(s.wrapBtn.Tag()) ||
		s.copyBtn.Hovered() ||
		s.wrapBtn.Hovered() ||
		(!s.CopiedUntil.IsZero() && env.Now().Before(s.CopiedUntil))
}

// LayoutCodeBlock renders a code block, Output card, or diff with v0.9 rules:
// unwrapped horizontal scroll + remembered wrap toggle, separate +/- column on
// diffs, Copy excluding line numbers and diff markers with in-place "Copied"
// confirmation, height cap with "Show full output", keyboard scroll on focus,
// and Copy/wrap visible on hover or focus.
func LayoutCodeBlock(env *kit.Env, gtx *layout.Context, st *CodeBlockState, p *CodeBlockProps) (layout.Dimensions, CodeBlockResult) {
	var mdSt State
	ch := env.Th.Chrome()
	mdSt.sync(nil, env.Sh, gtx.Metric, &ch)
	v := &view{env: env, th: env.Th, st: &mdSt, shaper: env.Sh, locale: gtx.Locale}
	return v.layoutCodeBlock(env, gtx, st, p)
}

type codeBlockMetrics struct {
	w, pad     int
	codeAvailW int
	viewH      int
	gutterW    int
	markerW    int
	lineH      int
	visCount   int
	capped     bool
	flows      []*flow
	diffLines  []DiffLine
}

func (v *view) layoutCodeBlock(env *kit.Env, gtx *layout.Context, st *CodeBlockState, p *CodeBlockProps) (layout.Dimensions, CodeBlockResult) {
	st.ensureButtons()
	var res CodeBlockResult
	st.syncCache(p)
	v.pollBlockEvents(env, gtx, st)
	m := v.measureCodeBlock(gtx, st, p)

	rec := op.Record(gtx.Ops)
	y := v.drawCodeBlockContents(env, gtx, st, p, &m, &res)
	call := rec.Stop()

	box := image.Rectangle{Max: image.Pt(m.w, y)}
	v.paintCodeBlockFrame(gtx, st, p, box, func() {
		v.registerBlockInput(gtx, st, box)
		call.Add(gtx.Ops)
	})
	return layout.Dimensions{Size: image.Pt(m.w, y)}, res
}

func (v *view) measureCodeBlock(gtx *layout.Context, st *CodeBlockState, p *CodeBlockProps) codeBlockMetrics {
	w := gtx.Constraints.Max.X
	pad := gtx.Dp(theme.SpaceMD)
	bodyMaxW := max(w-2*pad, 0)
	lines := st.resolvedLines(p)
	diffLines := st.cachedDiff
	totalLines := len(lines)
	if p.IsDiff {
		totalLines = len(diffLines)
	}
	capped, visCount := capLineCount(p.MaxLines, st.Expanded, p.Truncated, totalLines)
	fs := flowStyle{px: fixed.I(gtx.Sp(theme.TypeCode().Size)), mono: true, whole: !st.Wrapped}
	lineH := max(gtx.Sp(theme.TypeCode().Line), fs.lineHeight())
	gutterW, markerW := v.gutterWidths(gtx, totalLines, p.IsDiff, p.ShowLineNumbers || p.IsDiff)
	codeAvailW := max(bodyMaxW-gutterW-markerW, 0)
	flows, contentW, contentH := v.measureCodeRows(lines, diffLines, visCount, codeAvailW, fs, p.IsDiff, st.Wrapped, lineH)
	viewH := contentH
	if st.Expanded && visCount > maxExpandedViewportLines {
		viewH = min(contentH, maxExpandedViewportLines*lineH)
	}
	st.updateScrollBounds(codeAvailW, contentW, viewH, contentH)
	return codeBlockMetrics{
		w: w, pad: pad, codeAvailW: codeAvailW, viewH: viewH,
		gutterW: gutterW, markerW: markerW, lineH: lineH,
		visCount: visCount, capped: capped, flows: flows, diffLines: diffLines,
	}
}

func capLineCount(maxLines int, expanded, truncated bool, totalLines int) (capped bool, visCount int) {
	if maxLines == 0 {
		maxLines = defaultMaxCodeLines
	}
	capped = maxLines > 0 && !expanded && (totalLines > maxLines || truncated)
	visCount = totalLines
	if capped && totalLines > maxLines {
		visCount = maxLines
	}
	return capped, visCount
}

func (s *CodeBlockState) updateScrollBounds(codeAvailW, contentW, viewH, contentH int) {
	s.viewW = codeAvailW
	s.viewH = viewH
	s.maxScrollX = 0
	if !s.Wrapped && contentW > codeAvailW {
		s.maxScrollX = contentW - codeAvailW
	}
	s.maxScrollY = max(contentH-viewH, 0)
	s.ScrollX = min(max(s.ScrollX, 0), s.maxScrollX)
	s.ScrollY = min(max(s.ScrollY, 0), s.maxScrollY)
}

func (v *view) drawCodeBlockContents(
	env *kit.Env, gtx *layout.Context, st *CodeBlockState, p *CodeBlockProps, m *codeBlockMetrics, res *CodeBlockResult,
) int {
	showActions := st.ActionsVisible(env, gtx)
	y := 0
	if p.IsOutputCard {
		y += v.drawBlockHeader(env, gtx, st, p, m.w, m.pad, showActions, res)
		y += v.drawDividerAt(env, gtx, m.w, y)
	}
	bodyTop := y + m.pad
	v.drawCodeBody(gtx, st, p, m.flows, m.diffLines, m.visCount, m.pad, bodyTop, m.codeAvailW, m.viewH, m.gutterW, m.markerW, m.lineH)
	y = bodyTop + m.viewH + m.pad
	if !p.IsOutputCard && showActions {
		v.drawFloatingActions(env, gtx, st, p, m.w, m.pad, res)
	}
	if m.capped {
		y += v.drawDividerAt(env, gtx, m.w, y)
		y += v.drawExpandFooter(env, gtx, st, m.w, m.pad, y, res, p.Truncated)
	}
	return y
}

func (v *view) paintCodeBlockFrame(gtx *layout.Context, st *CodeBlockState, p *CodeBlockProps, box image.Rectangle, drawInner func()) {
	surf, radius := v.th.CodeSurface(), gtx.Dp(theme.RadiusSM)
	if p.IsOutputCard {
		surf, radius = v.th.Chrome().SurfaceRaised, gtx.Dp(theme.RadiusLG)
	}
	paint.FillShape(gtx.Ops, surf, clip.UniformRRect(box, radius).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, v.th.Chrome().BorderSubtle, clip.Stroke{
		Path:  clip.UniformRRect(box, radius).Path(gtx.Ops),
		Width: float32(max(gtx.Dp(theme.Hairline), 1)),
	}.Op())
	drawInner()
	if gtx.Focused(&st.focusTag) {
		paint.FillShape(gtx.Ops, v.th.Chrome().FocusBorder, clip.Stroke{
			Path:  clip.UniformRRect(box, radius).Path(gtx.Ops),
			Width: float32(max(gtx.Dp(theme.FocusStroke), 1)),
		}.Op())
	}
}

func (s *CodeBlockState) syncCache(p *CodeBlockProps) {
	if s.cachedRaw == p.Raw && s.cachedFormat == p.Format && (len(s.cachedLines) > 0 || len(s.cachedDiff) > 0 || p.Raw == "") {
		return
	}
	s.cachedRaw = p.Raw
	s.cachedFormat = p.Format
	s.cachedLines = nil
	s.cachedDiff = nil
	if p.IsDiff {
		s.cachedDiff = ParseDiff(p.Raw)
		return
	}
	if len(p.Lines) > 0 {
		return
	}
	s.cachedLines = plainCodeLines(p.Raw)
}

func (s *CodeBlockState) resolvedLines(p *CodeBlockProps) [][]markdown.Span {
	if len(p.Lines) > 0 {
		return p.Lines
	}
	return s.cachedLines
}

func plainCodeLines(raw string) [][]markdown.Span {
	clean := strings.ReplaceAll(strings.TrimSuffix(raw, "\n"), "\t", tabSpaces)
	if clean == "" {
		return [][]markdown.Span{{}}
	}
	parts := strings.Split(clean, "\n")
	out := make([][]markdown.Span, len(parts))
	for i, part := range parts {
		if part != "" {
			out[i] = []markdown.Span{{Text: part}}
		}
	}
	return out
}

func (v *view) pollBlockEvents(env *kit.Env, gtx *layout.Context, st *CodeBlockState) {
	if !st.CopiedUntil.IsZero() {
		if env.Now().Before(st.CopiedUntil) {
			env.Driver.WakeAt(st.CopiedUntil)
		} else {
			st.CopiedUntil = time.Time{}
		}
	}
	for {
		ev, ok := gtx.Event(
			pointer.Filter{Target: &st.focusTag, Kinds: pointer.Enter | pointer.Leave | pointer.Press | pointer.Cancel | pointer.Scroll, ScrollX: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}},
			key.FocusFilter{Target: &st.focusTag},
			key.Filter{Focus: &st.focusTag, Name: key.NameLeftArrow},
			key.Filter{Focus: &st.focusTag, Name: key.NameRightArrow},
			key.Filter{Focus: &st.focusTag, Name: key.NameUpArrow},
			key.Filter{Focus: &st.focusTag, Name: key.NameDownArrow},
			key.Filter{Focus: &st.focusTag, Name: key.NamePageUp},
			key.Filter{Focus: &st.focusTag, Name: key.NamePageDown},
			key.Filter{Focus: &st.focusTag, Name: key.NameHome},
			key.Filter{Focus: &st.focusTag, Name: key.NameEnd},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case pointer.Event:
			v.handleBlockPointer(gtx, st, e)
		case key.Event:
			if e.State == key.Press {
				v.handleBlockKey(gtx, st, e)
			}
		}
	}
}

func (v *view) handleBlockPointer(gtx *layout.Context, st *CodeBlockState, e pointer.Event) {
	switch e.Kind {
	case pointer.Enter:
		st.hovered = true
	case pointer.Leave, pointer.Cancel:
		st.hovered = false
	case pointer.Press:
		if e.Buttons.Contain(pointer.ButtonPrimary) {
			gtx.Execute(key.FocusCmd{Tag: &st.focusTag})
		}
	case pointer.Scroll:
		if st.maxScrollX > 0 && e.Scroll.X != 0 {
			st.ScrollX = min(max(st.ScrollX+int(e.Scroll.X), 0), st.maxScrollX)
		}
		if st.maxScrollY > 0 && e.Scroll.Y != 0 {
			st.ScrollY = min(max(st.ScrollY+int(e.Scroll.Y), 0), st.maxScrollY)
		}
	case pointer.Move, pointer.Release, pointer.Drag:
	}
}

func (v *view) handleBlockKey(gtx *layout.Context, st *CodeBlockState, e key.Event) {
	stepX := max(gtx.Dp(theme.SpaceXXL), 1)
	stepY := max(gtx.Sp(theme.TypeCode().Line), 1)
	switch string(e.Name) {
	case string(key.NameLeftArrow):
		st.ScrollX = max(st.ScrollX-stepX, 0)
	case string(key.NameRightArrow):
		st.ScrollX = min(st.ScrollX+stepX, st.maxScrollX)
	case string(key.NameUpArrow):
		st.ScrollY = max(st.ScrollY-stepY, 0)
	case string(key.NameDownArrow):
		st.ScrollY = min(st.ScrollY+stepY, st.maxScrollY)
	case string(key.NamePageUp):
		st.ScrollY = max(st.ScrollY-max(st.viewH, stepY), 0)
	case string(key.NamePageDown):
		st.ScrollY = min(st.ScrollY+max(st.viewH, stepY), st.maxScrollY)
	case string(key.NameHome):
		st.ScrollX = 0
	case string(key.NameEnd):
		st.ScrollX = st.maxScrollX
	}
}

func (v *view) registerBlockInput(gtx *layout.Context, st *CodeBlockState, box image.Rectangle) {
	area := clip.Rect(box).Push(gtx.Ops)
	event.Op(gtx.Ops, &st.focusTag)
	area.Pop()
}

func (v *view) gutterWidths(gtx *layout.Context, totalLines int, isDiff, showLineNums bool) (gutterW, markerW int) {
	charW := max(v.shape("0", font.Font{Typeface: fonts.Mono}, fixed.I(gtx.Sp(theme.TypeCodeSmall().Size))).w, 1)
	if showLineNums {
		digits := len(strconv.Itoa(max(totalLines, 1)))
		gutterW = digits*charW + gtx.Dp(theme.SpaceMD)
	}
	if isDiff {
		markerW = charW + gtx.Dp(theme.SpaceSM)
	}
	return gutterW, markerW
}
