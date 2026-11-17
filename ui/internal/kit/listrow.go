package kit

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// ListRowTone selects the semantic status dot color for a ListRow's object
// state (§6.20: object state is shown as a tag or labelled dot, never as the
// row's background).
type ListRowTone int

// ListRowTone values for object state badges.
const (
	ToneNeutral ListRowTone = iota
	ToneInfo
	ToneSuccess
	ToneWarning
	ToneDanger
)

// ListAction describes one inline row action revealed on hover or keyboard
// focus (§6.20). Important actions should also appear in ContextMenu.
type ListAction struct {
	ID       string
	Icon     IconName
	Label    string
	Shortcut Shortcut
	Disabled bool
}

// ListRowProps configures one ListRow for the current frame (§6.20).
type ListRowProps struct {
	Icon        IconName
	Label       string
	Secondary   string
	Trailing    string
	StateLabel  string
	StateTone   ListRowTone
	Actions     []ListAction
	ContextMenu []MenuItem
	Selected    bool
	Draggable   bool
	Dragging    bool
	Disabled    bool
}

// ListRowEvents reports what happened on a ListRow in the current frame.
type ListRowEvents struct {
	Clicked       bool
	Opened        bool
	Action        string
	ContextChosen string
	MenuOpened    bool
	Hovered       bool
	Focused       bool
	Err           error
}

// ListRowState holds the long-lived interaction and motion state of one ListRow.
type ListRowState struct {
	tag      bool
	hovered  bool
	pressed  bool
	hoverF   motion.Follower
	focusF   motion.Follower
	actionF  motion.Follower
	presence motion.Presence
	removing bool
	actions  []*IconButtonState
	menu     *MenuState
	menuPos  image.Point
}

// NewListRowState builds a ListRowState at rest.
func NewListRowState() *ListRowState {
	return &ListRowState{
		hoverF:   motion.NewFollower(motion.Snappy()),
		focusF:   motion.NewFollower(motion.Snappy()),
		actionF:  motion.NewFollower(motion.Snappy()),
		presence: motion.NewPresence(motion.Smooth(), motion.Exit()),
		menu:     NewMenuState(),
	}
}

// Tag returns the row's event/focus tag.
func (s *ListRowState) Tag() event.Tag { return &s.tag }

// Enter starts the row's insert animation (fade + height grow on Smooth).
func (s *ListRowState) Enter() {
	s.removing = false
	s.presence.Snap(false)
	s.presence.Show(true)
}

// Remove starts the row's removal animation (fade then collapse on Exit).
func (s *ListRowState) Remove() {
	s.removing = true
	s.presence.Show(false)
}

// Removed reports true once Remove has been called and the Exit motion settled.
func (s *ListRowState) Removed() bool {
	return s.removing && !s.presence.Visible()
}

func (s *ListRowState) ensureActions(n int) {
	for len(s.actions) < n {
		s.actions = append(s.actions, NewIconButtonState())
	}
}

func (s *ListRowState) toneColor(env *Env, t ListRowTone) color.NRGBA {
	st := env.Th.Status()
	switch t {
	case ToneInfo:
		return st.Info
	case ToneSuccess:
		return st.Success
	case ToneWarning:
		return st.Warning
	case ToneDanger:
		return st.Danger
	case ToneNeutral:
		return env.Th.Chrome().IconMuted
	default:
		return env.Th.Chrome().IconMuted
	}
}

// Layout draws a standalone ListRow and returns its dimensions and events.
func (s *ListRowState) Layout(env *Env, gtx *layout.Context, p *ListRowProps) (layout.Dimensions, ListRowEvents) {
	return s.layoutRow(env, gtx, p, false)
}

func (s *ListRowState) layoutRow(env *Env, gtx *layout.Context, p *ListRowProps, suppressSelectedFill bool) (layout.Dimensions, ListRowEvents) {
	var ev ListRowEvents
	w := gtx.Constraints.Max.X
	fullH := gtx.Dp(theme.Row)
	pres := s.stepRowPresence(env)
	h := rowHeight(fullH, pres)
	if h <= 0 {
		return layout.Dimensions{Size: image.Pt(w, 0)}, ev
	}
	rowBox := image.Rect(0, 0, w, h)
	pill := pillRect(gtx, rowBox)
	radius := gtx.Dp(theme.RadiusMD)

	s.pollEvents(gtx, pill, p, &ev)
	hov := !p.Disabled && (s.hovered || env.previewHover(&s.tag))
	foc := !p.Disabled && (env.FocusVisible(gtx, &s.tag) || env.previewFocus(&s.tag))
	ev.Hovered, ev.Focused = hov, foc

	hovA := followSmooth(env, &s.hoverF, boolFloat(hov))
	focA := followSmooth(env, &s.focusF, boolFloat(foc))
	actA := followSmooth(env, &s.actionF, boolFloat(hov || foc))

	if pres < 1 {
		defer clip.Rect(rowBox).Push(gtx.Ops).Pop()
		defer op.Offset(image.Pt(0, (h-fullH)/2)).Push(gtx.Ops).Pop()
	}

	s.paintRowChrome(env, gtx, pill, radius, p, suppressSelectedFill, pres, hovA)
	s.drawContent(env, gtx, pill, fullH, p, pres, actA, &ev)
	if focA > 0 {
		insetFocusRing(gtx, pill, radius, env.Th.Chrome().FocusBorder, focA*pres)
	}
	s.layoutContextMenu(env, gtx, p, &ev)
	return layout.Dimensions{Size: image.Pt(w, h)}, ev
}

func (s *ListRowState) stepRowPresence(env *Env) float32 {
	if !s.removing && !s.presence.Visible() {
		s.presence.Snap(true)
		s.presence.Show(true)
	}
	return stepPresence(env, &s.presence)
}

func rowHeight(fullH int, pres float32) int {
	if pres >= 1 {
		return fullH
	}
	return int(float32(fullH)*pres + 0.5)
}

func (s *ListRowState) paintRowChrome(env *Env, gtx *layout.Context, pill image.Rectangle, radius int, p *ListRowProps, suppressSelectedFill bool, pres, hovA float32) {
	c := env.Th.Chrome()
	if p.Dragging {
		DrawFloatShadow(gtx, pill, radius)
	}
	if !suppressSelectedFill && p.Selected {
		fillLayer(gtx, pill, radius, c.SelectedWash, pres)
	}
	if hovA > 0 {
		fillLayer(gtx, pill, radius, c.HoverWash, hovA*pres)
	}
	if p.Dragging {
		strokeRRect(gtx.Ops, pill, radius, float32(gtx.Dp(theme.Hairline)), fade(c.BorderSubtle, pres))
	}
}

func (s *ListRowState) layoutContextMenu(env *Env, gtx *layout.Context, p *ListRowProps, ev *ListRowEvents) {
	if len(p.ContextMenu) == 0 || (!s.menu.IsOpen() && !ev.MenuOpened) {
		return
	}
	anchor := image.Rectangle{Min: s.menuPos, Max: s.menuPos}
	mr, err := s.menu.Layout(env, gtx, MenuProps{Items: p.ContextMenu, Anchor: anchor})
	if err != nil && ev.Err == nil {
		ev.Err = err
	}
	if mr.Chosen != "" {
		ev.ContextChosen = mr.Chosen
	}
}

func (s *ListRowState) pollEvents(gtx *layout.Context, pill image.Rectangle, p *ListRowProps, ev *ListRowEvents) {
	if p.Disabled {
		s.hovered, s.pressed = false, false
		return
	}
	defer clip.Rect(pill).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &s.tag)
	addRowCursor(gtx, p.Draggable, p.Dragging)
	for {
		e, ok := gtx.Event(
			pointer.Filter{Target: &s.tag, Kinds: pointer.Enter | pointer.Leave | pointer.Press | pointer.Release | pointer.Cancel},
			key.FocusFilter{Target: &s.tag},
			key.Filter{Focus: &s.tag, Name: key.NameReturn},
			key.Filter{Focus: &s.tag, Name: key.NameEnter},
			key.Filter{Focus: &s.tag, Name: key.Name("Menu")},
			key.Filter{Focus: &s.tag, Name: key.NameF10, Required: key.ModShift},
		)
		if !ok {
			return
		}
		switch e := e.(type) {
		case pointer.Event:
			s.handlePointer(gtx, e, p, ev)
		case key.Event:
			s.handleKey(gtx, e, pill, p, ev)
		}
	}
}

func addRowCursor(gtx *layout.Context, draggable, dragging bool) {
	if !draggable {
		pointer.CursorPointer.Add(gtx.Ops)
		return
	}
	if dragging {
		pointer.CursorGrabbing.Add(gtx.Ops)
		return
	}
	pointer.CursorGrab.Add(gtx.Ops)
}

func (s *ListRowState) handlePointer(gtx *layout.Context, e pointer.Event, p *ListRowProps, ev *ListRowEvents) {
	switch e.Kind {
	case pointer.Enter:
		s.hovered = true
	case pointer.Leave, pointer.Cancel:
		s.hovered, s.pressed = false, false
	case pointer.Press:
		s.handlePress(gtx, e, p, ev)
	case pointer.Release:
		if s.pressed {
			s.pressed = false
			ev.Clicked = true
		}
	case pointer.Move, pointer.Drag, pointer.Scroll:
	}
}

func (s *ListRowState) handlePress(gtx *layout.Context, e pointer.Event, p *ListRowProps, ev *ListRowEvents) {
	if e.Buttons.Contain(pointer.ButtonSecondary) && len(p.ContextMenu) > 0 {
		s.menuPos = e.Position.Round()
		s.menu.SetOpener(&s.tag)
		s.menu.Open()
		ev.MenuOpened = true
		return
	}
	if e.Buttons.Contain(pointer.ButtonPrimary) {
		s.pressed = true
		gtx.Execute(key.FocusCmd{Tag: &s.tag})
	}
}

func (s *ListRowState) handleKey(gtx *layout.Context, e key.Event, pill image.Rectangle, p *ListRowProps, ev *ListRowEvents) {
	if e.State != key.Press {
		return
	}
	if e.Name == key.NameReturn || e.Name == key.NameEnter {
		ev.Opened = true
		return
	}
	if (e.Name == key.Name("Menu") || e.Name == key.NameF10) && len(p.ContextMenu) > 0 {
		s.menuPos = image.Pt(pill.Min.X+gtx.Dp(theme.SpaceMD), pill.Max.Y)
		s.menu.SetOpener(&s.tag)
		s.menu.OpenKeyboard()
		ev.MenuOpened = true
	}
}

func (s *ListRowState) drawContent(env *Env, gtx *layout.Context, pill image.Rectangle, h int, p *ListRowProps, pres, actA float32, ev *ListRowEvents) {
	fg, muted, iconInk := s.contentColors(env, p.Disabled, pres)
	padX := gtx.Dp(theme.SpaceMD)
	gap := gtx.Dp(theme.SpaceSM)
	x := pill.Min.X + padX
	right := pill.Max.X - padX

	right = s.drawActions(env, gtx, pill, h, right, gap, p, actA, ev)
	right = drawRowTrailing(env, gtx, pill, h, right, gap, p.Trailing, muted)
	right = s.drawRowState(env, gtx, pill, h, right, gap, p, muted, pres)
	x = drawRowIcon(env, gtx, pill, h, x, gap, p.Icon, iconInk, ev)
	drawRowLabels(env, gtx, pill, h, x, right, gap, p.Label, p.Secondary, fg, muted)
}

func (s *ListRowState) contentColors(env *Env, disabled bool, pres float32) (fg, muted, iconInk color.NRGBA) {
	c := env.Th.Chrome()
	fg, muted, iconInk = c.Fg, c.FgMuted, c.IconMuted
	if disabled {
		fg, muted, iconInk = c.FgDisabled, c.FgDisabled, c.FgDisabled
	}
	return fade(fg, pres), fade(muted, pres), fade(iconInk, pres)
}

func (s *ListRowState) drawActions(env *Env, gtx *layout.Context, pill image.Rectangle, h, right, gap int, p *ListRowProps, actA float32, ev *ListRowEvents) int {
	if len(p.Actions) == 0 || actA <= 0 {
		return right
	}
	s.ensureActions(len(p.Actions))
	btnSize := gtx.Dp(theme.ControlSM)
	for i := len(p.Actions) - 1; i >= 0; i-- {
		act := p.Actions[i]
		right -= btnSize
		pt := image.Pt(right, pill.Min.Y+(h-btnSize)/2)
		at(gtx, pt, func(gtx *layout.Context) {
			g := *gtx
			g.Constraints = layout.Exact(image.Pt(btnSize, btnSize))
			clicked, err := s.actions[i].Layout(env, &g, &IconButtonProps{
				Icon: act.Icon, Label: act.Label, Shortcut: act.Shortcut, Disabled: p.Disabled || act.Disabled,
			})
			if err != nil && ev.Err == nil {
				ev.Err = err
			}
			if clicked {
				ev.Action = act.ID
			}
		})
		right -= gtx.Dp(theme.SpaceXXS)
	}
	return right - gap
}

func drawRowTrailing(env *Env, gtx *layout.Context, pill image.Rectangle, h, right, gap int, trailing string, muted color.NRGBA) int {
	if trailing == "" {
		return right
	}
	sz := measureLabel(env, gtx, theme.TypeSmall(), trailing)
	right -= sz.X
	pt := image.Pt(right, pill.Min.Y+centerLabelY(env, gtx, theme.TypeSmall(), h))
	drawRunAt(env, gtx, pt, 0, h, theme.TypeSmall(), muted, trailing, false)
	return right - gap
}

func (s *ListRowState) drawRowState(env *Env, gtx *layout.Context, pill image.Rectangle, h, right, gap int, p *ListRowProps, muted color.NRGBA, pres float32) int {
	if p.StateLabel == "" {
		return right
	}
	dotSz := gtx.Dp(theme.SpaceSM)
	sz := measureLabel(env, gtx, theme.TypeSmall(), p.StateLabel)
	right -= sz.X
	pt := image.Pt(right, pill.Min.Y+centerLabelY(env, gtx, theme.TypeSmall(), h))
	drawRunAt(env, gtx, pt, 0, h, theme.TypeSmall(), muted, p.StateLabel, false)
	right -= gtx.Dp(theme.SpaceXS) + dotSz
	dotRect := image.Rect(right, pill.Min.Y+(h-dotSz)/2, right+dotSz, pill.Min.Y+(h+dotSz)/2)
	paint.FillShape(gtx.Ops, fade(s.toneColor(env, p.StateTone), pres), clip.UniformRRect(dotRect, dotSz/2).Op(gtx.Ops))
	return right - gap
}

func drawRowIcon(env *Env, gtx *layout.Context, pill image.Rectangle, h, x, gap int, icon IconName, ink color.NRGBA, ev *ListRowEvents) int {
	if icon == "" {
		return x
	}
	iconPx := gtx.Dp(theme.IconSM)
	pt := image.Pt(x, pill.Min.Y+(h-iconPx)/2)
	if err := drawIconAt(env, gtx, pt, icon, theme.IconSM, ink); err != nil && ev.Err == nil {
		ev.Err = err
	}
	return x + iconPx + gap
}

func drawRowLabels(env *Env, gtx *layout.Context, pill image.Rectangle, h, x, right, gap int, label, secondary string, fg, muted color.NRGBA) {
	if x < right && label != "" {
		pt := image.Pt(x, pill.Min.Y+centerLabelY(env, gtx, theme.TypeBody(), h))
		drawRunAt(env, gtx, pt, max(right-x, 0), h, theme.TypeBody(), fg, label, true)
		x += measureLabel(env, gtx, theme.TypeBody(), label).X + gap
	}
	if x < right && secondary != "" {
		pt := image.Pt(x, pill.Min.Y+centerLabelY(env, gtx, theme.TypeSmall(), h))
		drawRunAt(env, gtx, pt, max(right-x, 0), h, theme.TypeSmall(), muted, secondary, true)
	}
}
