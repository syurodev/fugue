package kit

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// SegmentedOption describes one segment in a SegmentedControl (§6.12).
type SegmentedOption struct {
	ID       string
	Label    string
	Icon     IconName
	Disabled bool
}

// SegmentedProps configures a SegmentedControl for one frame (§6.12).
type SegmentedProps struct {
	Options  []SegmentedOption
	Selected int
	Disabled bool
}

// SegmentedState holds the long-lived interaction and sliding-pill state of
// one SegmentedControl (§6.12).
type SegmentedState struct {
	tag       bool
	pill      slidingPill
	focusF    motion.Follower
	hoverIdx  int
	pressIdx  int
	rects     []image.Rectangle
	lastCount int
}

// NewSegmentedState builds a SegmentedState at rest.
func NewSegmentedState() *SegmentedState {
	return &SegmentedState{
		pill:     newSlidingPill(),
		focusF:   motion.NewFollower(motion.Snappy()),
		hoverIdx: -1,
		pressIdx: -1,
	}
}

// Tag returns the single tab-stop input tag for the SegmentedControl (§6.12).
func (s *SegmentedState) Tag() event.Tag { return &s.tag }

// PillRect returns the current animated pill rectangle, for motion evidence.
func (s *SegmentedState) PillRect() image.Rectangle { return s.pill.rect }

// Measure returns the SegmentedControl's dimensions without drawing or reading
// input.
func (s *SegmentedState) Measure(env *Env, gtx *layout.Context, pr SegmentedProps) layout.Dimensions {
	size, _ := s.measureGeometry(env, gtx, pr)
	return layout.Dimensions{Size: size}
}

func (s *SegmentedState) measureGeometry(env *Env, gtx *layout.Context, pr SegmentedProps) (image.Point, []image.Rectangle) {
	pad := gtx.Dp(theme.SpaceXXS)
	h := gtx.Dp(theme.ControlMD)
	segH := h - 2*pad
	segPadX := gtx.Dp(theme.SpaceMD)
	iconW := gtx.Dp(theme.IconSM)
	iconGap := gtx.Dp(theme.SpaceXS)

	rects := make([]image.Rectangle, len(pr.Options))
	x := pad
	for i, opt := range pr.Options {
		w := 2 * segPadX
		if opt.Icon != "" {
			w += iconW
			if opt.Label != "" {
				w += iconGap
			}
		}
		if opt.Label != "" {
			w += measureLabel(env, gtx, theme.TypeLabel(), opt.Label).X
		}
		w = max(w, gtx.Dp(theme.HitMin))
		rects[i] = image.Rect(x, pad, x+w, pad+segH)
		x += w
	}
	return image.Pt(x+pad, h), rects
}

// Layout lays out the SegmentedControl with a single tab stop, Left/Right
// arrow navigation, pointer selection, and the Smooth sliding pill (§6.12).
func (s *SegmentedState) Layout(env *Env, gtx *layout.Context, pr SegmentedProps) (changed bool, next int, err error) {
	size, rects := s.measureGeometry(env, gtx, pr)
	next = pr.Selected
	if next < 0 || next >= len(pr.Options) {
		next = 0
	}
	if len(s.rects) != len(rects) {
		s.hoverIdx = -1
		s.pressIdx = -1
	}
	s.rects = rects
	s.lastCount = len(pr.Options)

	if !pr.Disabled && len(pr.Options) > 0 {
		changed, next = s.readInput(gtx, pr, next)
	} else {
		s.hoverIdx = -1
		s.pressIdx = -1
	}

	s.registerArea(gtx, size, pr.Disabled)

	focused := !pr.Disabled && (env.FocusVisible(gtx, s.Tag()) || env.previewFocus(s.Tag()))
	focus := followState(env, &s.focusF, focused)
	var target image.Rectangle
	active := next >= 0 && next < len(rects) && !pr.Options[next].Disabled
	if active {
		target = rects[next]
	}
	s.pill.step(env, target, active)

	if err := s.draw(env, gtx, size, rects, pr, next, focus); err != nil {
		return changed, next, err
	}
	return changed, next, nil
}

func (s *SegmentedState) readInput(gtx *layout.Context, pr SegmentedProps, cur int) (bool, int) {
	changed := false
	next := cur

	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: s.Tag()},
			key.Filter{Focus: s.Tag(), Name: key.NameLeftArrow},
			key.Filter{Focus: s.Tag(), Name: key.NameRightArrow},
			pointer.Filter{Target: s.Tag(), Kinds: pointer.Move | pointer.Enter | pointer.Leave | pointer.Press | pointer.Release | pointer.Cancel},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.Event:
			if idx, ok := s.handleKey(pr, e, next); ok {
				next = idx
				changed = true
			}
		case pointer.Event:
			if idx, ok := s.handlePointer(gtx, pr, e, next); ok {
				next = idx
				changed = true
			}
		}
	}
	return changed, next
}

func (s *SegmentedState) handleKey(pr SegmentedProps, e key.Event, cur int) (int, bool) {
	if e.State != key.Press {
		return cur, false
	}
	step := 1
	if e.Name == key.NameLeftArrow {
		step = -1
	}
	idx, ok := nextEnabledSegment(pr.Options, cur, step)
	return idx, ok && idx != cur
}

func (s *SegmentedState) handlePointer(gtx *layout.Context, pr SegmentedProps, e pointer.Event, cur int) (int, bool) {
	idx := segmentAt(s.rects, image.Pt(int(e.Position.X), int(e.Position.Y)))
	switch e.Kind {
	case pointer.Move, pointer.Enter:
		s.hoverIdx = idx
	case pointer.Leave, pointer.Cancel:
		s.hoverIdx = -1
		s.pressIdx = -1
	case pointer.Press:
		if segEnabled(pr, idx) {
			s.pressIdx = idx
			gtx.Execute(key.FocusCmd{Tag: s.Tag()})
		}
	case pointer.Release:
		hit := s.pressIdx >= 0 && idx == s.pressIdx && segEnabled(pr, idx) && idx != cur
		s.pressIdx = -1
		if hit {
			return idx, true
		}
	case pointer.Drag, pointer.Scroll:
	default:
	}
	return cur, false
}

func segEnabled(pr SegmentedProps, idx int) bool {
	return idx >= 0 && !pr.Options[idx].Disabled
}

func (s *SegmentedState) registerArea(gtx *layout.Context, size image.Point, disabled bool) {
	if disabled {
		return
	}
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, s.Tag())
}

func (s *SegmentedState) draw(env *Env, gtx *layout.Context, size image.Point, rects []image.Rectangle, pr SegmentedProps, selected int, focus float32) error {
	c := env.Th.Chrome()
	opScale := float32(1)
	if pr.Disabled {
		opScale = theme.DisabledOpacity
	}
	outerR := image.Rectangle{Max: size}
	outerRad := gtx.Dp(theme.RadiusMD)
	pillRad := max(outerRad-gtx.Dp(theme.SpaceXXS), gtx.Dp(theme.RadiusXS))

	fillLayer(gtx, outerR, outerRad, fade(c.FillSoft, opScale), 1)
	for i, r := range rects {
		if i == selected || pr.Options[i].Disabled {
			continue
		}
		if s.pressIdx == i {
			fillLayer(gtx, r, pillRad, fade(c.PressedWash, opScale), 1)
		} else if s.hoverIdx == i {
			fillLayer(gtx, r, pillRad, fade(c.HoverWash, opScale), 1)
		}
	}
	s.pill.draw(gtx, pillRad, fade(c.CanvasBg, opScale))
	if !s.pill.rect.Empty() {
		strokeRRect(gtx.Ops, s.pill.rect, pillRad, float32(max(gtx.Dp(theme.Hairline), 1)), fade(c.Border, opScale))
	}

	for i, opt := range pr.Options {
		if err := drawSegmentContent(env, gtx, rects[i], opt, i == selected, pr.Disabled || opt.Disabled, opScale); err != nil {
			return err
		}
	}
	if focus > 0 {
		focusRing(gtx, outerR, outerRad, fade(c.FocusBorder, opScale), focus)
	}
	return nil
}

func drawSegmentContent(env *Env, gtx *layout.Context, r image.Rectangle, opt SegmentedOption, selected, disabled bool, opScale float32) error {
	c := env.Th.Chrome()
	ink := c.FgMuted
	if selected {
		ink = c.Fg
	}
	if disabled {
		ink = c.FgDisabled
	}
	ink = fade(ink, opScale)

	iconPx := gtx.Dp(theme.IconSM)
	gap := gtx.Dp(theme.SpaceXS)
	var lblSize image.Point
	if opt.Label != "" {
		lblSize = measureLabel(env, gtx, theme.TypeLabel(), opt.Label)
	}
	contentW := lblSize.X
	if opt.Icon != "" {
		contentW += iconPx
		if opt.Label != "" {
			contentW += gap
		}
	}
	x := r.Min.X + (r.Dx()-contentW)/2
	if opt.Icon != "" {
		iy := r.Min.Y + (r.Dy()-iconPx)/2
		if err := drawIconAt(env, gtx, image.Pt(x, iy), opt.Icon, theme.IconSM, ink); err != nil {
			return err
		}
		x += iconPx + gap
	}
	if opt.Label != "" {
		ly := r.Min.Y + centerLabelY(env, gtx, theme.TypeLabel(), r.Dy())
		drawRunAt(env, gtx, image.Pt(x, ly), r.Max.X-x, r.Dy(), theme.TypeLabel(), ink, opt.Label, false)
	}
	return nil
}

func segmentAt(rects []image.Rectangle, pt image.Point) int {
	for i, r := range rects {
		if pt.In(r) {
			return i
		}
	}
	return -1
}

func nextEnabledSegment(opts []SegmentedOption, cur, step int) (int, bool) {
	n := len(opts)
	for i := 1; i <= n; i++ {
		idx := (cur + step*i + n*2) % n
		if !opts[idx].Disabled {
			return idx, true
		}
	}
	return cur, false
}
