package kit

import (
	"fmt"
	"image"
	"math"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Select, ui-kit §6.13: a read-only field (TextField look) showing the
// selected option's label or a clear unselected placeholder, and a trailing
// chevron that rotates 180° on open (Snappy). It opens a kit Menu (§6.15)
// anchored below it, flipping at the window edge. Enter, Space or Down open
// it; typeahead while open is the Menu's; Escape closes and returns focus to
// the Select. Disabled uses DisabledOpacity (§4.1).

// SelectOption is one selectable choice in a Select.
type SelectOption struct {
	ID       string
	Label    string
	Icon     IconName
	Disabled bool
}

// SelectProps are the per-frame, caller-owned properties (UK-02).
type SelectProps struct {
	Label       string
	Placeholder string
	Helper      string
	Error       string
	Options     []SelectOption
	Value       string
	Disabled    bool
}

// SelectResult reports what happened in the frame (UK-06).
type SelectResult struct {
	Changed bool
	Value   string
}

// SelectState is the long-lived state of one Select (UK-02).
type SelectState struct {
	trigger  struct{}
	menu     *MenuState
	hoverF   motion.Follower
	focusF   motion.Follower
	swapF    motion.Follower
	lineF    motion.Follower
	chevronF motion.Follower
	hover    bool
	hadFocus bool
	err      fieldErrorState
}

// NewSelectState builds the state for one Select.
func NewSelectState() *SelectState {
	snappy := motion.Snappy()
	m := NewMenuState()
	s := &SelectState{
		menu:     m,
		hoverF:   motion.NewFollower(snappy),
		focusF:   motion.NewFollower(snappy),
		swapF:    motion.NewFollower(snappy),
		lineF:    motion.NewFollower(motion.Smooth()),
		chevronF: motion.NewFollower(snappy),
	}
	m.SetOpener(&s.trigger)
	return s
}

// Tag returns the Select's focus tag.
func (s *SelectState) Tag() event.Tag { return &s.trigger }

// IsOpen reports whether the Select's menu is open.
func (s *SelectState) IsOpen() bool { return s.menu.IsOpen() }

// MarkSubmitted opens the §6.8 error gate on the Select.
func (s *SelectState) MarkSubmitted() { s.err.submitted = true }

// Measure returns the Select's natural box.
func (s *SelectState) Measure(env *Env, gtx *layout.Context, p *SelectProps) layout.Dimensions {
	sz := fieldMetrics(env, gtx, p.Label)
	w := selectWidth(env, gtx, p)
	line := fieldLineHeight(env, gtx, p.Helper, p.Error, p.Error != "", w)
	return layout.Dimensions{
		Size:     image.Pt(w, sz.total(line)),
		Baseline: sz.labelH + sz.boxPadY + labelLine(env, gtx, editorType())/2,
	}
}

// Layout draws the read-only field, handles keyboard/pointer triggers, and
// lays out the Menu when open.
func (s *SelectState) Layout(env *Env, gtx *layout.Context, p *SelectProps) (SelectResult, error) {
	var res SelectResult
	sz := fieldMetrics(env, gtx, p.Label)
	w := selectWidth(env, gtx, p)
	box := image.Rect(0, sz.labelH, w, sz.labelH+sz.boxH)

	focusLost := s.input(gtx, box, p.Disabled)
	if p.Disabled && s.menu.IsOpen() {
		s.menu.Close()
	}

	errored := s.err.visible(p.Error, false, FieldEvents{FocusLost: focusLost})
	fm, chev := s.resolveMotion(env, gtx, p, w, errored)
	if err := s.draw(env, gtx, p, w, sz, errored, fm, chev); err != nil {
		return res, err
	}

	if !p.Disabled {
		mres, err := s.menu.Layout(env, gtx, MenuProps{
			Items:  selectMenuItems(p.Options),
			Anchor: box,
		})
		if err != nil {
			return res, err
		}
		if mres.Chosen != "" && mres.Chosen != p.Value {
			res.Changed = true
			res.Value = mres.Chosen
		}
	}
	return res, nil
}

func (s *SelectState) resolveMotion(env *Env, gtx *layout.Context, p *SelectProps, w int, errored bool) (fieldMotion, float32) {
	focused := !p.Disabled && (env.FocusVisible(gtx, &s.trigger) || env.previewFocus(&s.trigger))
	hovered := !p.Disabled && (s.hover || s.menu.IsOpen() || env.previewHover(&s.trigger))
	fm := fieldMotion{
		hover:  followState(env, &s.hoverF, hovered),
		focus:  followState(env, &s.focusF, focused),
		swap:   followState(env, &s.swapF, errored),
		linePx: followSmooth(env, &s.lineF, fieldLineHeight(env, gtx, p.Helper, p.Error, errored, w)),
	}
	return fm, followState(env, &s.chevronF, s.menu.IsOpen())
}

func (s *SelectState) input(gtx *layout.Context, box image.Rectangle, disabled bool) bool {
	ig := *gtx
	if disabled {
		ig = gtx.Disabled()
	}
	defer clip.Rect(box).Push(ig.Ops).Pop()
	event.Op(ig.Ops, &s.trigger)
	if disabled {
		s.hover = false
		s.hadFocus = false
		return false
	}
	s.pointerInput(&ig)
	s.keyInput(&ig)
	lost := s.hadFocus && !ig.Focused(&s.trigger) && !s.menu.IsOpen()
	s.hadFocus = ig.Focused(&s.trigger)
	return lost
}

func (s *SelectState) pointerInput(ig *layout.Context) {
	for {
		e, ok := ig.Event(pointer.Filter{
			Target: &s.trigger,
			Kinds:  pointer.Enter | pointer.Leave | pointer.Press,
		})
		if !ok {
			return
		}
		pe, is := e.(pointer.Event)
		if !is {
			continue
		}
		switch pe.Kind {
		case pointer.Enter:
			s.hover = true
		case pointer.Leave:
			s.hover = false
		case pointer.Press:
			ig.Execute(key.FocusCmd{Tag: &s.trigger})
			if s.menu.IsOpen() {
				s.menu.Close()
			} else {
				s.menu.Open()
			}
		case pointer.Cancel, pointer.Release, pointer.Move, pointer.Drag, pointer.Scroll:
		}
	}
}

func (s *SelectState) keyInput(ig *layout.Context) {
	filters := []event.Filter{
		key.FocusFilter{Target: &s.trigger},
		key.Filter{Focus: &s.trigger, Name: key.NameReturn},
		key.Filter{Focus: &s.trigger, Name: key.NameEnter},
		key.Filter{Focus: &s.trigger, Name: key.NameSpace},
		key.Filter{Focus: &s.trigger, Name: key.NameDownArrow},
	}
	for {
		e, ok := ig.Event(filters...)
		if !ok {
			return
		}
		if ke, is := e.(key.Event); is && ke.State == key.Press && !s.menu.IsOpen() {
			s.menu.OpenKeyboard()
			return
		}
	}
}

func (s *SelectState) draw(env *Env, gtx *layout.Context, p *SelectProps, w int, sz fieldSizes, errored bool, fm fieldMotion, chev float32) error {
	c := env.Th.Chrome()
	look := fieldLookOf(env, p.Disabled, errored)
	if p.Label != "" {
		at(gtx, image.Pt(0, 0), func(gtx *layout.Context) {
			dims := drawText(env, gtx, theme.TypeSmall(), fade(c.FgMuted, dimOf(p.Disabled)), p.Label, false)
			recordRun(env, image.Rectangle{Max: dims.Size})
		})
	}
	box := image.Rect(0, sz.labelH, w, sz.labelH+sz.boxH)
	paintBox(gtx, box, look, fm)

	display, selected := selectDisplayText(p)
	textCol := fade(c.Placeholder, dimOf(p.Disabled))
	if selected {
		textCol = look.fg
	}
	textX := sz.padX
	if selected && selectedIcon(p) != "" {
		iconPx := gtx.Dp(theme.IconMD)
		pt := image.Pt(sz.padX, sz.labelH+(sz.boxH-iconPx)/2)
		if err := drawIconAt(env, gtx, pt, selectedIcon(p), theme.IconMD, fade(c.IconMuted, dimOf(p.Disabled))); err != nil {
			return fmt.Errorf("draw select icon: %w", err)
		}
		textX += iconPx + gtx.Dp(theme.SpaceSM)
	}
	if display != "" {
		at(gtx, image.Pt(textX, sz.labelH+sz.boxPadY), func(gtx *layout.Context) {
			dims := drawText(env, gtx, editorType(), textCol, display, false)
			recordRun(env, image.Rectangle{Max: dims.Size}.Add(image.Pt(textX, sz.labelH+sz.boxPadY)))
		})
	}

	chevPx := gtx.Dp(theme.IconMD)
	cx := w - gtx.Dp(theme.SpaceMD) - chevPx
	cy := sz.labelH + (sz.boxH-chevPx)/2
	center := f32.Pt(float32(cx)+float32(chevPx)/2, float32(cy)+float32(chevPx)/2)
	rot := op.Affine(f32.AffineId().Rotate(center, chev*math.Pi)).Push(gtx.Ops)
	err := drawIconAt(env, gtx, image.Pt(cx, cy), IconChevronDown, theme.IconMD, fade(c.IconMuted, dimOf(p.Disabled)))
	rot.Pop()
	if err != nil {
		return fmt.Errorf("draw select chevron: %w", err)
	}

	lineY := sz.labelH + sz.boxH + int(sz.lineGapPx+0.5)
	var lineBounds image.Rectangle
	at(gtx, image.Pt(0, lineY), func(gtx *layout.Context) {
		lineBounds = belowLine(env, gtx, w, p.Helper, p.Error, c.FgSubtle, env.Th.Status().Danger, fm)
	})
	recordRun(env, lineBounds.Add(image.Pt(0, lineY)))
	return nil
}

func selectDisplayText(p *SelectProps) (string, bool) {
	if p.Value != "" {
		for _, opt := range p.Options {
			if opt.ID == p.Value {
				return opt.Label, true
			}
		}
	}
	return p.Placeholder, false
}

func selectedIcon(p *SelectProps) IconName {
	if p.Value == "" {
		return ""
	}
	for _, opt := range p.Options {
		if opt.ID == p.Value {
			return opt.Icon
		}
	}
	return ""
}

func selectWidth(env *Env, gtx *layout.Context, p *SelectProps) int {
	longest := p.Placeholder
	hasIcon := false
	for _, opt := range p.Options {
		if opt.Icon != "" {
			hasIcon = true
		}
		if len(opt.Label) > len(longest) {
			longest = opt.Label
		}
	}
	slotW := gtx.Dp(theme.IconMD) + gtx.Dp(theme.SpaceSM)
	if hasIcon {
		slotW += gtx.Dp(theme.IconMD) + gtx.Dp(theme.SpaceSM)
	}
	return fieldMinWidth(env, gtx, p.Label, longest, slotW)
}

func selectMenuItems(opts []SelectOption) []MenuItem {
	items := make([]MenuItem, len(opts))
	for i, o := range opts {
		items[i] = MenuItem{
			ID:       o.ID,
			Label:    o.Label,
			Icon:     o.Icon,
			Disabled: o.Disabled,
		}
	}
	return items
}
