package kit

import (
	"image"
	"math"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// SectionHeaderProps configures one SectionHeader (§6.23).
type SectionHeaderProps struct {
	Label       string
	Collapsible bool
	Action      *IconButtonProps
	// Icon is an optional leading glyph, drawn muted after the chevron.
	Icon IconName
	// Inset is the left inset of the first glyph or label; zero keeps
	// theme.SpaceXS. A list that aligns the header with its rows passes the
	// row's own padding.
	Inset unit.Dp
}

// SectionHeaderEvents reports what happened on a SectionHeader in the frame.
type SectionHeaderEvents struct {
	Toggled     bool
	ActionClick bool
	Err         error
}

// SectionHeaderState holds the collapse and motion state of a SectionHeader.
type SectionHeaderState struct {
	tag     bool
	open    bool
	hovered bool
	pressed bool
	chevF   motion.Follower
	foldF   motion.Follower
	focusF  motion.Follower
	action  *IconButtonState
}

// NewSectionHeaderState builds a SectionHeaderState with initial open state.
func NewSectionHeaderState(open bool) *SectionHeaderState {
	init := boolFloat(open)
	chev := motion.NewFollower(motion.Snappy())
	chev.Snap(init)
	fold := motion.NewFollower(motion.Smooth())
	fold.Snap(init)
	return &SectionHeaderState{
		open:   open,
		chevF:  chev,
		foldF:  fold,
		focusF: motion.NewFollower(motion.Snappy()),
		action: NewIconButtonState(),
	}
}

// Tag returns the header's focus/event tag.
func (s *SectionHeaderState) Tag() event.Tag { return &s.tag }

// Open reports whether the section is expanded.
func (s *SectionHeaderState) Open() bool { return s.open }

// SetOpen sets the section's expanded state.
func (s *SectionHeaderState) SetOpen(open bool) { s.open = open }

// Layout draws the SectionHeader and, when content is non-nil, folds its
// content on Smooth as the chevron rotates 90° on Snappy (§6.23).
func (s *SectionHeaderState) Layout(env *Env, gtx *layout.Context, p *SectionHeaderProps, content func(*layout.Context) layout.Dimensions) (layout.Dimensions, SectionHeaderEvents) {
	var ev SectionHeaderEvents
	w := gtx.Constraints.Max.X
	h := gtx.Dp(theme.HitMin)
	box := image.Rect(0, 0, w, h)

	s.updateToggleInput(gtx, box, p.Collapsible, &ev)
	foc := p.Collapsible && (env.FocusVisible(gtx, &s.tag) || env.previewFocus(&s.tag))
	focA := followSmooth(env, &s.focusF, boolFloat(foc))
	target := float32(1)
	if p.Collapsible {
		target = boolFloat(s.open)
	}
	chevT := followSmooth(env, &s.chevF, target)
	foldT := followSmooth(env, &s.foldF, target)

	x := gtx.Dp(theme.SpaceXS)
	if p.Inset > 0 {
		x = gtx.Dp(p.Inset)
	}
	right := s.drawAction(env, gtx, w, h, p.Action, &ev)
	x = s.drawChevron(env, gtx, x, h, p.Collapsible, chevT, &ev)
	x = drawHeaderIcon(env, gtx, x, h, p.Icon, &ev)
	drawHeaderLabel(env, gtx, x, right, h, p.Label)
	if focA > 0 {
		insetFocusRing(gtx, box, gtx.Dp(theme.RadiusSM), env.Th.Chrome().FocusBorder, focA)
	}
	totalH := h + drawFoldedBody(gtx, w, h, foldT, content)
	return layout.Dimensions{Size: image.Pt(w, totalH)}, ev
}

func (s *SectionHeaderState) updateToggleInput(gtx *layout.Context, box image.Rectangle, collapsible bool, ev *SectionHeaderEvents) {
	if !collapsible {
		s.hovered, s.pressed = false, false
		return
	}
	s.pollToggle(gtx, box, ev)
}

func (s *SectionHeaderState) drawAction(env *Env, gtx *layout.Context, w, h int, act *IconButtonProps, ev *SectionHeaderEvents) int {
	right := w - gtx.Dp(theme.SpaceXS)
	if act == nil {
		return right
	}
	btnSz := gtx.Dp(theme.ControlSM)
	right -= btnSz
	pt := image.Pt(right, (h-btnSz)/2)
	at(gtx, pt, func(gtx *layout.Context) {
		g := *gtx
		g.Constraints = layout.Exact(image.Pt(btnSz, btnSz))
		clicked, err := s.action.Layout(env, &g, act)
		if err != nil && ev.Err == nil {
			ev.Err = err
		}
		if clicked {
			ev.ActionClick = true
		}
	})
	return right - gtx.Dp(theme.SpaceXS)
}

func (s *SectionHeaderState) drawChevron(env *Env, gtx *layout.Context, x, h int, collapsible bool, chevT float32, ev *SectionHeaderEvents) int {
	if !collapsible {
		return x
	}
	iconPx := gtx.Dp(theme.IconXS)
	if err := drawRotatedIcon(env, gtx, image.Pt(x, (h-iconPx)/2), IconChevronRight, theme.IconXS, env.Th.Chrome().FgSubtle, chevT*(math.Pi/2)); err != nil && ev.Err == nil {
		ev.Err = err
	}
	return x + iconPx + gtx.Dp(theme.SpaceXS)
}

func drawHeaderIcon(env *Env, gtx *layout.Context, x, h int, icon IconName, ev *SectionHeaderEvents) int {
	if icon == "" {
		return x
	}
	iconPx := gtx.Dp(theme.IconSM)
	if err := drawIconAt(env, gtx, image.Pt(x, (h-iconPx)/2), icon, theme.IconSM, env.Th.Chrome().IconMuted); err != nil && ev.Err == nil {
		ev.Err = err
	}
	return x + iconPx + gtx.Dp(theme.SpaceSM)
}

func drawHeaderLabel(env *Env, gtx *layout.Context, x, right, h int, label string) {
	if x >= right || label == "" {
		return
	}
	pt := image.Pt(x, centerLabelY(env, gtx, theme.TypeSmall(), h))
	drawRunAt(env, gtx, pt, max(right-x, 0), h, theme.TypeSmall(), env.Th.Chrome().FgSubtle, label, true)
}

func drawFoldedBody(gtx *layout.Context, w, h int, foldT float32, content func(*layout.Context) layout.Dimensions) int {
	if content == nil || foldT <= 0 {
		return 0
	}
	m := op.Record(gtx.Ops)
	var cdims layout.Dimensions
	at(gtx, image.Pt(0, h), func(gtx *layout.Context) {
		g := *gtx
		g.Constraints = layout.Constraints{Max: image.Pt(w, gtx.Constraints.Max.Y)}
		cdims = content(&g)
	})
	call := m.Stop()
	visH := cdims.Size.Y
	if foldT < 1 {
		visH = int(float32(cdims.Size.Y)*foldT + 0.5)
	}
	if visH <= 0 {
		return 0
	}
	defer clip.Rect(image.Rect(0, h, w, h+visH)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	return visH
}

func (s *SectionHeaderState) pollToggle(gtx *layout.Context, box image.Rectangle, ev *SectionHeaderEvents) {
	defer clip.Rect(box).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &s.tag)
	pointer.CursorPointer.Add(gtx.Ops)
	for {
		e, ok := gtx.Event(
			pointer.Filter{Target: &s.tag, Kinds: pointer.Enter | pointer.Leave | pointer.Press | pointer.Release | pointer.Cancel},
			key.FocusFilter{Target: &s.tag},
			key.Filter{Focus: &s.tag, Name: key.NameReturn},
			key.Filter{Focus: &s.tag, Name: key.NameEnter},
			key.Filter{Focus: &s.tag, Name: key.NameSpace},
		)
		if !ok {
			return
		}
		switch e := e.(type) {
		case pointer.Event:
			s.handlePointer(gtx, e, ev)
		case key.Event:
			if e.State == key.Press {
				s.open = !s.open
				ev.Toggled = true
			}
		}
	}
}

func (s *SectionHeaderState) handlePointer(gtx *layout.Context, e pointer.Event, ev *SectionHeaderEvents) {
	switch e.Kind {
	case pointer.Enter:
		s.hovered = true
	case pointer.Leave, pointer.Cancel:
		s.hovered, s.pressed = false, false
	case pointer.Press:
		if e.Buttons.Contain(pointer.ButtonPrimary) {
			s.pressed = true
			gtx.Execute(key.FocusCmd{Tag: &s.tag})
		}
	case pointer.Release:
		if s.pressed {
			s.pressed = false
			s.open = !s.open
			ev.Toggled = true
		}
	case pointer.Move, pointer.Drag, pointer.Scroll:
	}
}
