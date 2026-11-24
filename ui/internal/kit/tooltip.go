package kit

import (
	"fmt"
	"image"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Tooltip, ui-kit §6.7: floating label (type.small) with an optional Kbd
// shortcut (§6.31) on SurfaceRaised, BorderSubtle, radius.md, elevation.float
// (§3.6). It appears after 500 ms of hover (Driver.WakeAt) or at once on
// keyboard focus, hops between neighbouring controls within 300 ms by sliding
// on Snappy instead of closing and reopening, sits above the control and
// flips below near the edge, never covers the pointer, takes no input (no
// event area of its own), and hides on Escape.

const (
	tooltipHoverDelay = 500 * time.Millisecond
	tooltipHopGrace   = 300 * time.Millisecond
)

// TooltipProps are the per-frame, caller-owned properties (UK-02).
type TooltipProps struct {
	Text     string
	Shortcut Shortcut
	Inline   bool
}

type tooltipManager struct {
	ready      bool
	open       motion.Presence
	posX, posY motion.Follower

	activeTag    event.Tag
	activeProps  TooltipProps
	activeAnchor image.Rectangle
	activeHasPtr bool
	activePtr    image.Point

	frameSeen    bool
	frameTag     event.Tag
	frameProps   TooltipProps
	frameAnchor  image.Rectangle
	frameHovered bool
	frameFocused bool
	frameHasPtr  bool
	framePtr     image.Point

	hoverTag   event.Tag
	hoverSince time.Time
	closedAt   time.Time
	dismissed  event.Tag
	snapPos    bool
	rect       image.Rectangle
	flipped    bool
}

func (m *tooltipManager) ensureInit() {
	if m.ready {
		return
	}
	m.ready = true
	m.open = motion.NewPresence(motion.Snappy(), motion.Exit())
	m.posX = motion.NewFollower(motion.Snappy())
	m.posY = motion.NewFollower(motion.Snappy())
}

func (m *tooltipManager) beginFrame() {
	m.frameSeen = false
	m.frameTag = nil
	m.frameProps = TooltipProps{}
	m.frameAnchor = image.Rectangle{}
	m.frameHovered = false
	m.frameFocused = false
	m.frameHasPtr = false
	m.framePtr = image.Point{}
}

// TooltipAnchor registers a control's tooltip for the current frame when
// hovered or keyboard-focused. anchor and ptr are in the caller's cursor
// coordinates.
func (e *Env) TooltipAnchor(tag event.Tag, anchor image.Rectangle, hovered, focused, hasPtr bool, ptr image.Point, p TooltipProps) {
	if (!hovered && !focused) || p.Text == "" || tag == nil {
		return
	}
	if e.tip.frameSeen && e.tip.frameHovered && !hovered {
		return
	}
	e.tip.frameSeen = true
	e.tip.frameTag = tag
	e.tip.frameProps = p
	e.tip.frameAnchor = anchor.Add(e.origin)
	e.tip.frameHovered = hovered
	e.tip.frameFocused = focused
	e.tip.frameHasPtr = hasPtr
	e.tip.framePtr = ptr.Add(e.origin)
}

// TooltipRect returns the tooltip's last laid-out rectangle in window
// coordinates, or empty if not visible.
func (e *Env) TooltipRect() image.Rectangle {
	if !e.tip.ready || !e.tip.open.Visible() {
		return image.Rectangle{}
	}
	return e.tip.rect
}

// TooltipText returns the active tooltip's text, or empty if not visible.
func (e *Env) TooltipText() string {
	if !e.tip.ready || !e.tip.open.Visible() {
		return ""
	}
	return e.tip.activeProps.Text
}

// TooltipFlipped reports whether the active tooltip flipped below its anchor.
func (e *Env) TooltipFlipped() bool {
	return e.tip.ready && e.tip.open.Visible() && e.tip.flipped
}

// TooltipLayout draws an inline tooltip specimen at the current cursor
// offset (for the gallery).
func TooltipLayout(env *Env, gtx *layout.Context, p TooltipProps) (layout.Dimensions, error) {
	size, err := tooltipSize(env, gtx, p)
	if err != nil {
		return layout.Dimensions{}, err
	}
	if err := drawTooltipBox(env, gtx, image.Rectangle{Max: size}, p, 1, false); err != nil {
		return layout.Dimensions{}, err
	}
	return layout.Dimensions{Size: size}, nil
}

func (m *tooltipManager) endFrame(env *Env, gtx *layout.Context) {
	m.ensureInit()
	m.checkEscape(gtx)
	m.resolveTarget(env, gtx)
	if m.open.Shown() {
		gtx.Event(key.Filter{Name: key.NameEscape})
	}

	if env.ReducedMotion {
		m.open.Snap(m.open.Shown())
	} else if m.open.Advance(env.DT()) {
		env.Keep()
	}
	if !m.open.Visible() || m.activeProps.Text == "" {
		m.rect = image.Rectangle{}
		m.flipped = false
		return
	}

	size, err := tooltipSize(env, gtx, m.activeProps)
	if err != nil {
		return
	}
	win := image.Rectangle{Max: env.win}
	gap := gtx.Dp(theme.SpaceXS)
	pos, flipped := placeTooltip(m.activeAnchor, size, win, gap, m.activeHasPtr, m.activePtr)
	m.flipped = flipped

	var x, y float32
	if m.snapPos || env.ReducedMotion {
		m.posX.Snap(float32(pos.X))
		m.posY.Snap(float32(pos.Y))
		x, y = float32(pos.X), float32(pos.Y)
		m.snapPos = false
	} else {
		x = followTo(env, &m.posX, float32(pos.X))
		y = followTo(env, &m.posY, float32(pos.Y))
	}
	m.rect = image.Rectangle{
		Min: image.Pt(int(x+0.5), int(y+0.5)),
		Max: image.Pt(int(x+0.5), int(y+0.5)).Add(size),
	}

	macro := op.Record(gtx.Ops)
	drawErr := drawTooltipBox(env, gtx, m.rect, m.activeProps, m.open.Progress(), m.flipped)
	call := macro.Stop()
	if drawErr == nil {
		op.Defer(gtx.Ops, call)
	}
}

func (m *tooltipManager) checkEscape(gtx *layout.Context) {
	if !m.open.Shown() {
		return
	}
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			return
		}
		if ke, is := ev.(key.Event); is && ke.State == key.Press {
			m.dismissed = m.activeTag
			m.closedAt = gtx.Now
			m.open.Show(false)
			return
		}
	}
}

func (m *tooltipManager) resolveTarget(env *Env, gtx *layout.Context) {
	if !m.frameSeen {
		if m.open.Shown() {
			m.closedAt = gtx.Now
			m.open.Show(false)
		}
		m.hoverTag = nil
		m.hoverSince = time.Time{}
		m.dismissed = nil
		return
	}
	if m.frameTag == m.dismissed {
		return
	}
	m.dismissed = nil

	withinHop := !m.closedAt.IsZero() && !gtx.Now.After(m.closedAt.Add(tooltipHopGrace))
	hop := (m.open.Shown() && m.activeTag != nil && m.activeTag != m.frameTag) || withinHop

	if m.frameFocused || hop {
		m.activate(!m.open.Shown() && !withinHop)
		return
	}
	m.resolveHover(env, gtx, withinHop)
}

func (m *tooltipManager) resolveHover(env *Env, gtx *layout.Context, withinHop bool) {
	if m.hoverTag != m.frameTag {
		m.hoverTag = m.frameTag
		m.hoverSince = gtx.Now
	}
	readyAt := m.hoverSince.Add(tooltipHoverDelay)
	if !gtx.Now.Before(readyAt) {
		m.activate(!m.open.Shown() && !withinHop)
		return
	}
	env.Driver.WakeAt(readyAt)
}

func (m *tooltipManager) activate(snap bool) {
	m.open.Show(true)
	m.activeTag = m.frameTag
	m.activeProps = m.frameProps
	m.activeAnchor = m.frameAnchor
	m.activeHasPtr = m.frameHasPtr
	m.activePtr = m.framePtr
	if snap {
		m.snapPos = true
	}
}

func placeTooltip(anchor image.Rectangle, size image.Point, win image.Rectangle, gap int, hasPtr bool, ptr image.Point) (pos image.Point, flipped bool) {
	pos.X = anchor.Min.X + (anchor.Dx()-size.X)/2
	pos.Y = anchor.Min.Y - gap - size.Y
	if pos.Y < win.Min.Y && anchor.Max.Y+gap+size.Y <= win.Max.Y {
		pos.Y = anchor.Max.Y + gap
		flipped = true
	}
	pos = clampInto(pos, size, win)
	if hasPtr && ptr.In(image.Rectangle{Min: pos, Max: pos.Add(size)}) {
		if !flipped && anchor.Max.Y+gap+size.Y <= win.Max.Y {
			pos.Y = anchor.Max.Y + gap
			flipped = true
		} else if flipped && anchor.Min.Y-gap-size.Y >= win.Min.Y {
			pos.Y = anchor.Min.Y - gap - size.Y
			flipped = false
		}
		if ptr.In(image.Rectangle{Min: pos, Max: pos.Add(size)}) {
			pos.Y = max(win.Min.Y, ptr.Y-gap-size.Y)
		}
	}
	return clampInto(pos, size, win), flipped
}

func tooltipSize(env *Env, gtx *layout.Context, p TooltipProps) (image.Point, error) {
	padX := gtx.Dp(theme.SpaceMD)
	padY := gtx.Dp(theme.SpaceXS)
	txtSz := measureLabel(env, gtx, theme.TypeSmall(), p.Text)
	w := 2*padX + txtSz.X
	baseKbd, err := KbdSize(env, gtx, Shortcut{Key: Char('A')})
	if err != nil {
		return image.Point{}, fmt.Errorf("tooltip height: %w", err)
	}
	h := max(txtSz.Y, baseKbd.Y)
	if p.Shortcut.Key != "" {
		ksz, err := KbdSize(env, gtx, p.Shortcut)
		if err != nil {
			return image.Point{}, fmt.Errorf("tooltip shortcut: %w", err)
		}
		w += gtx.Dp(theme.SpaceSM) + ksz.X
		h = max(h, ksz.Y)
	}
	return image.Pt(w, h+2*padY), nil
}

func drawTooltipBox(env *Env, gtx *layout.Context, rect image.Rectangle, p TooltipProps, prog float32, flipped bool) error {
	fromY := rect.Max.Y
	if flipped {
		fromY = rect.Min.Y
	}
	from := f32.Pt(float32(rect.Min.X+rect.Dx()/2), float32(fromY))
	defer scaleAbout(gtx, from, 0.96+0.04*prog).Pop()
	defer paint.PushOpacity(gtx.Ops, unit01(prog)).Pop()

	c := env.Th.Chrome()
	radius := gtx.Dp(theme.RadiusMD)
	DrawFloatShadow(gtx, rect, radius)
	fillRRect(gtx.Ops, rect, radius, c.SurfaceRaised)
	strokeRRect(gtx.Ops, rect, radius, float32(gtx.Dp(theme.Hairline)), c.BorderSubtle)

	padX := gtx.Dp(theme.SpaceMD)
	x := rect.Min.X + padX
	drawMenuText(env, gtx, theme.TypeSmall(), c.Fg, p.Text, image.Pt(x, rect.Min.Y), rect.Dy())
	if p.Shortcut.Key != "" {
		x += measureLabel(env, gtx, theme.TypeSmall(), p.Text).X + gtx.Dp(theme.SpaceSM)
		ksz, err := KbdSize(env, gtx, p.Shortcut)
		if err != nil {
			return err
		}
		pt := image.Pt(x, rect.Min.Y+(rect.Dy()-ksz.Y)/2)
		var kerr error
		at(gtx, pt, func(gtx *layout.Context) {
			_, kerr = drawKbd(env, gtx, p.Shortcut, false, pt)
		})
		return kerr
	}
	return nil
}
