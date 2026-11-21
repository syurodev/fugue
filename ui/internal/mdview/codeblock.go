package mdview

import (
	"image"
	"strconv"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/event"
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

// CodeBlockProps configures one code block, terminal block, Output card, or
// file_change diff for the current frame (desktop-app v0.9/v0.10 §5.4).
type CodeBlockProps struct {
	Title           string
	Format          string
	Icon            kit.IconName
	Command         string
	ExitCode        *int32
	Duration        string
	IsOutputCard    bool
	IsDiff          bool
	Raw             string
	Lines           [][]markdown.Span
	Truncated       bool
	MaxLines        int
	ShowLineNumbers bool
	// HighlightLine and HighlightEndLine mark a 1-based inclusive row range
	// (of the displayed code rows, not diff lines) the file viewer tints
	// behind the text (F10.4e); zero on both disables the tint.
	HighlightLine    int
	HighlightEndLine int
	// ScrollToLine scrolls the block once on this frame so the named
	// displayed row lands at the viewport top (F10.4e); the caller decides
	// when to stop passing it, so the reader's own scrolling wins after.
	ScrollToLine int
	// ViewportLines overrides how many rows the expanded block shows before
	// its inner scroll ends (F10.4e); zero keeps the shared default. The
	// file viewer sizes it to the pane so a jump to a line is actually on
	// screen.
	ViewportLines int
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
	cachedCmd    string
	cachedTitle  string
	cachedLines  [][]markdown.Span
	cachedCmdLn  [][]markdown.Span
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
	cmdH       int
	gutterW    int
	markerW    int
	lineH      int
	visCount   int
	capped     bool
	cmdFlows   []*flow
	flows      []*flow
	diffLines  []DiffLine
}

func (v *view) layoutCodeBlock(env *kit.Env, gtx *layout.Context, st *CodeBlockState, p *CodeBlockProps) (layout.Dimensions, CodeBlockResult) {
	st.ensureButtons()
	var res CodeBlockResult
	st.syncCache(env, p)
	v.pollBlockEvents(env, gtx, st)
	m := v.measureCodeBlock(gtx, st, p)
	scrollToLine(gtx, st, p, &m)

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

// scrollToLine moves the scroll offset once so the requested displayed row
// lands at the viewport top, clamped to the measured bounds (F10.4e). It
// runs after measure, where exact row heights are known.
func scrollToLine(_ *layout.Context, st *CodeBlockState, p *CodeBlockProps, m *codeBlockMetrics) {
	if p.ScrollToLine <= 1 || p.IsDiff {
		return
	}
	y := 0
	for i := range min(p.ScrollToLine-1, len(m.flows)) {
		y += max(m.flows[i].height, m.lineH)
	}
	st.ScrollY = min(y, max(st.maxScrollY, 0))
}

func (v *view) measureCodeBlock(gtx *layout.Context, st *CodeBlockState, p *CodeBlockProps) codeBlockMetrics {
	w := gtx.Constraints.Max.X
	pad := gtx.Dp(theme.SpaceMD)
	bodyMaxW := max(w-2*pad, 0)
	cmdFlows, cmdH := v.measureCommandBlock(gtx, st, bodyMaxW)
	lines := st.resolvedLines(p)
	diffLines := st.cachedDiff
	totalLines := len(lines)
	if p.IsDiff {
		totalLines = len(diffLines)
	}
	capped, visCount := capLineCount(p.MaxLines, st.Expanded, p.Truncated, totalLines)
	fs := flowStyle{px: fixed.I(gtx.Sp(theme.TypeCode().Size)), mono: true, whole: !st.Wrapped, muted: p.Command != ""}
	lineH := max(gtx.Sp(theme.TypeCode().Line), fs.lineHeight())
	showNums := showCodeLineNumbers(p, totalLines)
	gutterW, markerW := v.gutterWidths(gtx, maxCodeLineNumber(p, diffLines, totalLines), p.IsDiff, showNums)
	codeAvailW := max(bodyMaxW-gutterW-markerW, 0)
	flows, contentW, contentH := v.measureCodeRows(lines, diffLines, visCount, codeAvailW, fs, p.IsDiff, st.Wrapped, lineH)
	viewH := contentH
	viewportLines := maxExpandedViewportLines
	if p.ViewportLines > 0 {
		viewportLines = p.ViewportLines
	}
	if st.Expanded && visCount > viewportLines {
		viewH = min(contentH, viewportLines*lineH)
	}
	st.updateScrollBounds(codeAvailW, contentW, viewH, contentH)
	return codeBlockMetrics{
		w: w, pad: pad, codeAvailW: codeAvailW, viewH: viewH, cmdH: cmdH,
		gutterW: gutterW, markerW: markerW, lineH: lineH,
		visCount: visCount, capped: capped, cmdFlows: cmdFlows, flows: flows, diffLines: diffLines,
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
	y := v.drawBlockHeader(env, gtx, st, p, m.w, m.pad, showActions, res)
	y += v.drawDividerAt(env, gtx, m.w, y)
	if len(m.cmdFlows) > 0 {
		y = v.drawCommandHeader(env, gtx, m.cmdFlows, m.w, m.pad, y, m.lineH, m.viewH > 0)
	}
	if m.viewH > 0 {
		bodyTop := y + m.pad
		v.drawCodeBody(gtx, st, p, m.flows, m.diffLines, m.visCount, m.pad, bodyTop, m.codeAvailW, m.viewH, m.gutterW, m.markerW, m.lineH)
		y = bodyTop + m.viewH + m.pad
	}
	if m.capped {
		y += v.drawExpandAction(env, gtx, st, m.w, m.pad, y, res, p.Truncated)
	}
	return y
}

func (v *view) paintCodeBlockFrame(gtx *layout.Context, st *CodeBlockState, _ *CodeBlockProps, box image.Rectangle, drawInner func()) {
	radius := gtx.Dp(theme.RadiusLG)
	// SurfaceBase plus a hairline: on the light canvas a SurfaceRaised block reads
	// as a heavy gray slab; the border carries the edge instead.
	paint.FillShape(gtx.Ops, v.th.Chrome().SurfaceBase, clip.UniformRRect(box, radius).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, v.th.Chrome().Border, clip.Stroke{
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

type envClock struct{ env *kit.Env }

func (c envClock) Now() time.Time {
	if c.env != nil {
		return c.env.Now()
	}
	return time.Time{}
}

func (s *CodeBlockState) syncCache(env *kit.Env, p *CodeBlockProps) {
	if s.cacheValid(p) {
		return
	}
	s.cachedRaw, s.cachedFormat = p.Raw, p.Format
	s.cachedCmd, s.cachedTitle = p.Command, p.Title
	s.cachedLines, s.cachedCmdLn, s.cachedDiff = nil, nil, nil
	clk := envClock{env: env}
	s.syncCommandCache(p.Command, clk)
	s.syncBodyCache(p, clk)
}

func (s *CodeBlockState) cacheValid(p *CodeBlockProps) bool {
	if s.cachedRaw != p.Raw || s.cachedFormat != p.Format || s.cachedCmd != p.Command || s.cachedTitle != p.Title {
		return false
	}
	return len(s.cachedLines) > 0 || len(s.cachedDiff) > 0 || (p.Raw == "" && p.Command == "")
}

func (s *CodeBlockState) syncCommandCache(cmd string, clk markdown.Clock) {
	if cmd == "" {
		return
	}
	cmdSrc := normalizeCode("$ " + cmd)
	if hl, ok := markdown.Highlight("sh", cmdSrc, clk); ok {
		s.cachedCmdLn = hl
		return
	}
	s.cachedCmdLn = plainCodeLines(cmdSrc)
}

func (s *CodeBlockState) syncBodyCache(p *CodeBlockProps, clk markdown.Clock) {
	if p.IsDiff {
		s.cachedDiff = ParseDiff(p.Raw)
		HighlightDiff(p.Title, s.cachedDiff, clk)
		return
	}
	if len(p.Lines) > 0 || p.Raw == "" {
		return
	}
	if lang := codeLanguage(p); lang != "" {
		if hl, ok := markdown.Highlight(lang, normalizeCode(p.Raw), clk); ok {
			s.cachedLines = hl
			return
		}
	}
	s.cachedLines = plainCodeLines(p.Raw)
}

func codeLanguage(p *CodeBlockProps) string {
	lang := strings.ToLower(strings.TrimSpace(p.Format))
	if lang == "" || lang == "text" || lang == "plain" {
		return ""
	}
	return lang
}

func normalizeCode(raw string) string {
	return strings.ReplaceAll(strings.TrimSuffix(raw, "\n"), "\t", tabSpaces)
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

func showCodeLineNumbers(p *CodeBlockProps, totalLines int) bool {
	if p.Command != "" {
		return false
	}
	if p.IsDiff {
		return true
	}
	if p.IsOutputCard {
		return p.ShowLineNumbers
	}
	return p.ShowLineNumbers || totalLines >= 2
}

func maxCodeLineNumber(p *CodeBlockProps, diffLines []DiffLine, totalLines int) int {
	n := max(totalLines, 1)
	if !p.IsDiff {
		return n
	}
	for i := range diffLines {
		if diffLines[i].LineNo > n {
			n = diffLines[i].LineNo
		}
	}
	return n
}

func (v *view) gutterWidths(gtx *layout.Context, totalLines int, isDiff, showLineNums bool) (gutterW, markerW int) {
	charW := max(v.shape("0", font.Font{Typeface: fonts.Mono}, fixed.I(gtx.Sp(theme.TypeCodeSmall().Size))).w, 1)
	if showLineNums {
		digits := len(strconv.Itoa(max(totalLines, 1)))
		// SpaceSM before the number (it sits inside a hunk's tint) and SpaceMD after it.
		gutterW = digits*charW + gtx.Dp(theme.SpaceSM) + gtx.Dp(theme.SpaceMD)
	}
	if isDiff {
		// The +/− marker gets its own column with SpaceMD on each side, so the
		// line number, the marker and the code never crowd each other.
		markerW = charW + 2*gtx.Dp(theme.SpaceMD)
	}
	return gutterW, markerW
}
