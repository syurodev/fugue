package kit

import (
	"fmt"
	"image"

	"gioui.org/f32"
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

// Popover, ui-kit §6.16: a floating layer with an anchor holding small
// interactive content — filters, a column picker, a link preview. It has the
// shape of Menu (§6.15): SurfaceRaised, BorderSubtle, radius.xl, padding 6,
// elevation.float — and its motion: it opens on Snappy, fading in and
// growing from 0.96 toward its anchor, and closes on Exit. It sits below its
// anchor and flips above where the window runs out, like a menu panel.
//
// Closing: Escape, a click outside, or the header's close button; closing
// never cancels work in flight (§6.16). A pinned popover ignores the click
// outside — the caller owns the pinned flag and the header's pin toggle —
// and still closes on Escape or the close button. While the popover is up, a
// window-wide absorbing area under its panel takes the presses that land on
// no panel, exactly as a menu's does (§6.15): the press that closes a
// popover goes no further.
//
// Focus (§6.16, UK-05): opened from the keyboard, focus moves to the content
// control the caller names (FocusOnOpen); on Escape or the close button
// focus returns to the opener. Escape is the topmost layer's (§7): a dialog
// above the popover, or a newer floating layer such as a menu opened from
// it, owns it first.
//
// Props and state are separate (UK-02): the caller rebuilds PopoverProps
// every frame and keeps one PopoverState per logical popover. The content
// comes as a Measure/Content pair, so measuring touches no event source and
// the content lays out exactly once per frame — the kit fields keep state
// and must not run twice in a frame (UK-02).

// PopoverProps are the per-frame, caller-owned properties (UK-02).
type PopoverProps struct {
	// Anchor is the box the popover opens from — the trigger's — in the
	// coordinates of the cursor Layout is called at.
	Anchor image.Rectangle
	// Title names the popover; the header always carries the close button
	// and, when the caller passes a Pin state, the pin toggle.
	Title string
	// Measure returns the content's box without painting or touching the
	// input source — with the kit controls' Measure methods. The width it
	// is given is Width, or unbounded when Width is zero.
	Measure func(gtx *layout.Context) image.Point
	// Content draws the interactive content below the header, at the width
	// Measure was given.
	Content func(gtx *layout.Context)
	// Width fixes the content's width; zero takes the measured one.
	Width int
	// Pinned ignores a click outside (§6.16); the caller owns the flag and
	// mirrors it in the header's pin toggle.
	Pinned bool
	// Inline draws the panel where the cursor is, open and still, instead
	// of on the overlay: the gallery's specimens.
	Inline bool
	// FocusOnOpen is the tag focus moves to when the popover was opened
	// from the keyboard (§6.16).
	FocusOnOpen event.Tag
	// Pin, when non-nil, is the header's pin toggle: the popover draws it
	// toggled by Pinned and reports the click in the result.
	Pin *IconButtonState
}

// PopoverResult is what happened in the frame. Closed is any close; the
// flags say which one, so the caller can tell the pin from a dismissal.
type PopoverResult struct {
	Closed       bool
	CloseClicked bool
	PinClicked   bool
	Size         image.Point
}

// PopoverState is the long-lived state of one popover (UK-02).
type PopoverState struct {
	open        motion.Presence
	claim       bool
	focusOnOpen bool
	opener      event.Tag
	// abs is the tag of the window-wide absorbing area under the panel;
	// panel the hit area of the panel itself. They are pointers, so the
	// state must not move in memory.
	abs, panel struct{}
	rect       image.Rectangle
	origin     image.Point
	// from is the point the panel grows from and dir the side it shifts in
	// from, toward the anchor.
	from, dir image.Point
	pad       int
	closeB    IconButtonState
}

// NewPopoverState builds the state of one popover.
func NewPopoverState() *PopoverState {
	return &PopoverState{open: motion.NewPresence(motion.Snappy(), motion.Exit())}
}

// Open opens the popover.
func (p *PopoverState) Open() {
	p.open.Show(true)
	p.claim = true
	p.focusOnOpen = false
}

// OpenKeyboard opens the popover from a keyboard trigger, moving focus into
// the content (§6.16).
func (p *PopoverState) OpenKeyboard() {
	p.Open()
	p.focusOnOpen = true
}

// SetOpener records the trigger tag focus returns to when the popover closes
// by Escape or its close button (§6.16, UK-05).
func (p *PopoverState) SetOpener(tag event.Tag) { p.opener = tag }

// Close closes the popover on Exit.
func (p *PopoverState) Close() { p.open.Show(false) }

// IsOpen reports whether the popover was last asked to be open.
func (p *PopoverState) IsOpen() bool { return p.open.Shown() }

// Rect is the panel's last box in window coordinates, or empty when nothing
// is drawn.
func (p *PopoverState) Rect() image.Rectangle {
	if !p.open.Visible() {
		return image.Rectangle{}
	}
	return p.rect
}

// Layout runs the popover's frame. A closed popover does nothing and asks
// for nothing; one that another layer has pushed out of the floating slots
// closes on Exit.
func (p *PopoverState) Layout(env *Env, gtx *layout.Context, props *PopoverProps) (PopoverResult, error) {
	var res PopoverResult
	if !p.open.Visible() {
		env.releasePopover(p)
		return res, nil
	}
	p.claimLayer(env, gtx, props)
	p.input(env, gtx, props, &res)
	p.advance(env, props.Inline)
	p.place(env, gtx, props)
	res.Size = p.rect.Size()
	if err := p.draw(env, gtx, props, &res); err != nil {
		return res, fmt.Errorf("draw the popover: %w", err)
	}
	return res, nil
}

// claimLayer takes the popover slot, closes a popover another layer pushed
// out, and applies the open focus.
func (p *PopoverState) claimLayer(env *Env, gtx *layout.Context, props *PopoverProps) {
	if props.Inline {
		return
	}
	if p.claim {
		env.claimPopover(p)
		p.claim = false
	}
	if !env.holdsPopover(p) {
		p.Close()
	}
	if p.focusOnOpen {
		if props.FocusOnOpen != nil {
			gtx.Execute(key.FocusCmd{Tag: props.FocusOnOpen})
		}
		p.focusOnOpen = false
	}
}

// input reads the frame's Escape and outside presses, against the areas the
// last frame drew — the menu's order (menuinput.go). A panel on its way out
// takes no input (§3.7).
func (p *PopoverState) input(env *Env, gtx *layout.Context, props *PopoverProps, res *PopoverResult) {
	if !p.open.Shown() || props.Inline {
		return
	}
	p.escapeInput(env, gtx, res)
	p.absorbPress(env, gtx, props, res)
}

// escapeInput applies the topmost-layer Escape rule (§7): a dialog, or a
// newer float layer over this popover, owns Escape first.
func (p *PopoverState) escapeInput(env *Env, gtx *layout.Context, res *PopoverResult) {
	if !env.topPopover(p) {
		return
	}
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			return
		}
		if ke, is := ev.(key.Event); is && ke.State == key.Press {
			p.escape(gtx, res)
			return
		}
	}
}

// absorbPress reads the window-wide absorbing area under the panel; a
// pinned popover registers none, so a click outside falls through (§6.16).
func (p *PopoverState) absorbPress(env *Env, gtx *layout.Context, props *PopoverProps, res *PopoverResult) {
	if !props.Pinned {
		a := clip.Rect(env.windowRect()).Push(gtx.Ops)
		event.Op(gtx.Ops, &p.abs)
		a.Pop()
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &p.abs, Kinds: pointer.Press})
		if !ok {
			return
		}
		if pe, is := ev.(pointer.Event); is && pe.Kind == pointer.Press {
			p.Close()
			res.Closed = true
			return
		}
	}
}

// escape closes the popover and returns focus to the opener (§6.16, UK-05).
func (p *PopoverState) escape(gtx *layout.Context, res *PopoverResult) {
	opener := p.opener
	p.Close()
	res.Closed = true
	if opener != nil {
		gtx.Execute(key.FocusCmd{Tag: opener})
	}
}

// advance moves the presence, or snaps it under reduced motion (UK-04).
func (p *PopoverState) advance(env *Env, inline bool) {
	if inline {
		return
	}
	if env.ReducedMotion {
		p.open.Snap(p.open.Shown())
	} else if p.open.Advance(env.DT()) {
		env.Keep()
	}
}

// contentBox measures the content once per frame into a throwaway macro,
// dead events included: Measure draws nothing and must touch no input
// source, and the macro discards whatever it recorded.
func (p *PopoverState) contentBox(gtx *layout.Context, props *PopoverProps) image.Point {
	var box image.Point
	at(gtx, image.Point{}, func(gtx *layout.Context) {
		m := op.Record(gtx.Ops)
		g := *gtx
		wide := 1 << 20
		if props.Width > 0 {
			wide = props.Width
		}
		g.Constraints = layout.Constraints{Min: image.Point{}, Max: image.Pt(wide, 1<<20)}
		box = props.Measure(&g)
		m.Stop()
	})
	if props.Width > 0 {
		box.X = props.Width
	}
	return box
}

// place measures the panel — the header over the content box — and puts it
// below the anchor, flipping above where the window runs out: the menu's
// placement (§6.16 takes §6.15's shape). A panel on its way out keeps the
// place it had.
func (p *PopoverState) place(env *Env, gtx *layout.Context, props *PopoverProps) {
	p.origin = env.origin
	win := env.floatingWindowRect(gtx)
	if props.Inline {
		win = image.Rect(-1<<20, -1<<20, 1<<20, 1<<20)
	}
	box := p.contentBox(gtx, props)
	p.pad = gtx.Dp(theme.SpaceSM)
	head := popoverHeader(env, gtx, props)
	size := image.Pt(box.X+2*p.pad, 2*p.pad+head+gtx.Dp(theme.SpaceSM)+box.Y)
	if !p.open.Shown() {
		return
	}
	pos, up := placeBelow(props.Anchor, size, win, gtx.Dp(theme.SpaceXS))
	p.rect = image.Rectangle{Min: pos, Max: pos.Add(size)}
	p.dir = image.Pt(0, dirOf(up))
	p.from = p.rect.Min.Add(size.Div(2))
	if up {
		p.from.Y = p.rect.Max.Y
	}
}

// popoverHeader is the header's height: the header buttons' square over the
// title line.
func popoverHeader(env *Env, gtx *layout.Context, props *PopoverProps) int {
	line := 0
	if props.Title != "" {
		line = labelLine(env, gtx, theme.TypeTitle())
	}
	return max(gtx.Dp(theme.ControlXS), line)
}

// draw records the panel — and, on the overlay, the absorbing area under it —
// and hands them to op.Defer (UK-07); an inline popover is placed in the
// flow instead.
func (p *PopoverState) draw(env *Env, gtx *layout.Context, props *PopoverProps, res *PopoverResult) error {
	macro := op.Record(gtx.Ops)
	if !props.Inline && p.open.Shown() {
		p.registerAreas(env, gtx, props)
	}
	prog := float32(1)
	if !props.Inline {
		prog = p.open.Progress()
	}
	shift := p.dir.Mul(int(float32(gtx.Dp(theme.SpaceXS)) * (1 - prog)))
	// The transform pushes pop before macro.Stop: a stack that crosses a
	// macro boundary is a Gio panic.
	bodyErr := func() error {
		defer op.Offset(shift).Push(gtx.Ops).Pop()
		defer scaleAbout(gtx, f32.Pt(float32(p.from.X), float32(p.from.Y)), 0.96+0.04*prog).Pop()
		defer paint.PushOpacity(gtx.Ops, unit01(prog)).Pop()
		return p.drawPanel(env, gtx, props, res)
	}()
	if bodyErr != nil {
		return bodyErr
	}
	call := macro.Stop()
	if props.Inline {
		call.Add(gtx.Ops)
	} else {
		op.Defer(gtx.Ops, call)
	}
	return nil
}

// registerAreas puts the absorbing area — under the panel, so the panel
// stays on top — and the panel's own hit area into the frame's ops. A
// pinned popover registers no absorbing area (§6.16).
func (p *PopoverState) registerAreas(env *Env, gtx *layout.Context, props *PopoverProps) {
	if !props.Pinned {
		a := clip.Rect(env.windowRect()).Push(gtx.Ops)
		event.Op(gtx.Ops, &p.abs)
		a.Pop()
	}
	area := clip.Rect(p.rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &p.panel)
	area.Pop()
}

// drawPanel draws the surface, the header — title, pin toggle, close — and
// the content. The header buttons lay out here, where they draw, so their
// hit areas sit at their boxes (UK-E10).
func (p *PopoverState) drawPanel(env *Env, gtx *layout.Context, props *PopoverProps, res *PopoverResult) error {
	head := popoverHeader(env, gtx, props)
	drawMenuPanelSurface(env, gtx, p.rect, p.pad, 0, 0)
	inner := p.rect.Inset(p.pad)

	var headErr error
	btn := max(gtx.Dp(theme.ControlXS), gtx.Dp(theme.HitMin))
	at(gtx, image.Pt(inner.Max.X-btn, inner.Min.Y), func(gtx *layout.Context) {
		clicked, err := p.closeB.Layout(env, gtx, &IconButtonProps{
			Icon: IconClose, Label: labelClose, Variant: IconButtonGhost, Size: SizeXS,
		})
		if err != nil {
			headErr = fmt.Errorf("draw the close button: %w", err)
			return
		}
		if clicked {
			res.CloseClicked = true
			p.escape(gtx, res)
		}
	})
	if headErr != nil {
		return headErr
	}
	x := inner.Max.X - btn
	if props.Pin != nil {
		x -= btn + gtx.Dp(theme.SpaceXS)
		at(gtx, image.Pt(x, inner.Min.Y), func(gtx *layout.Context) {
			clicked, err := props.Pin.Layout(env, gtx, &IconButtonProps{
				Icon: IconPin, Label: labelPin, Variant: IconButtonGhost, Size: SizeXS, Toggled: props.Pinned,
			})
			if err != nil {
				headErr = fmt.Errorf("draw the pin toggle: %w", err)
				return
			}
			if clicked {
				res.PinClicked = true
			}
		})
		if headErr != nil {
			return headErr
		}
	}
	if props.Title != "" {
		drawMenuText(env, gtx, theme.TypeTitle(), env.Th.Chrome().Fg, props.Title, image.Pt(inner.Min.X, inner.Min.Y), head)
	}
	at(gtx, image.Pt(inner.Min.X, inner.Min.Y+head+gtx.Dp(theme.SpaceSM)), func(gtx *layout.Context) {
		g := *gtx
		wide := p.rect.Dx() - 2*p.pad
		g.Constraints = layout.Constraints{Min: image.Point{}, Max: image.Pt(wide, 1<<20)}
		props.Content(&g)
	})
	return nil
}
