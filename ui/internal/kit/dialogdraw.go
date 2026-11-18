package kit

import (
	"fmt"
	"image"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// The drawing half of Dialog (§6.17): the frame's metrics, the scrim and
// frame painting, the body run once per frame inside its ScrollArea (§6.29)
// with room for every control's §3.4 ring, and the footer. The state and
// input half is dialog.go; the split follows menu.go and menudraw.go.

// dialogMetrics are the frame's resolved metrics for the frame.
type dialogMetrics struct {
	pad, headH, descH, footH, innerW int
	// maxBody is the tallest the visible body slot may be, so the frame
	// never grows past the window.
	maxBody int
}

// measure derives the frame metrics from the window and the props.
func (d *DialogState) measure(env *Env, gtx *layout.Context, props *DialogProps, win image.Rectangle) dialogMetrics {
	m := dialogMetrics{
		pad:    gtx.Dp(theme.SpaceXL),
		innerW: gtx.Dp(theme.DialogWidth) - 2*gtx.Dp(theme.SpaceXL),
	}
	m.headH = labelLine(env, gtx, theme.TypeTitle())
	if props.Description != "" {
		m.descH = wrappedTextHeight(env, gtx, props.Description, m.innerW) + gtx.Dp(theme.SpaceXS)
	}
	m.footH = gtx.Dp(theme.ControlLG)
	// The body gets what the window still has, at most: the frame keeps a
	// margin of space.xl to each window edge, and a body taller than that
	// scrolls instead of growing the frame past the window.
	chrome := 2*m.pad + m.headH + gtx.Dp(theme.SpaceSM) + m.descH + gtx.Dp(theme.SpaceLG) + m.footH
	m.maxBody = max(win.Dy()-chrome-2*gtx.Dp(theme.SpaceXL), gtx.Dp(theme.ControlMD))
	return m
}

// place resolves the frame's box and draws it. On the overlay everything is
// recorded and handed to op.Defer (UK-07); inline, it is placed in the flow.
// A frame on its way out (Visible but not Shown) still draws, taking no
// input (§3.7).
func (d *DialogState) place(env *Env, gtx *layout.Context, props *DialogProps, res *DialogResult) error {
	if !d.open.Visible() {
		return nil
	}
	if props.Inline {
		d.rect = image.Rectangle{}
		d.drawSpecimenScrim(env, gtx, props)
	} else {
		d.drawScrim(env, gtx, props)
	}
	m := d.measure(env, gtx, props, env.windowRect())
	return d.drawFrame(env, gtx, props, res, m)
}

// drawSpecimenScrim paints the dim behind an inline specimen's frame
// (§6.17), at once: a specimen is a still, so the scrim has no motion of
// its own.
func (d *DialogState) drawSpecimenScrim(env *Env, gtx *layout.Context, props *DialogProps) {
	if props.ScrimRect.Empty() {
		return
	}
	paint.FillShape(gtx.Ops, env.Th.Chrome().Scrim, clip.Rect(props.ScrimRect).Op())
}

// drawScrim paints the dim behind the frame (§3.5): over the whole window
// for a live dialog.
func (d *DialogState) drawScrim(env *Env, gtx *layout.Context, props *DialogProps) {
	if d.scrim.Value() <= 0 {
		return
	}
	c := env.Th.Chrome()
	rect := env.windowRect()
	if !props.ScrimRect.Empty() {
		rect = props.ScrimRect
	}
	paint.FillShape(gtx.Ops, fade(c.Scrim, unit01(d.scrim.Value())), clip.Rect(rect).Op())
}

// drawFrame paints the frame — surface, title, description, body, footer —
// at the box the metrics resolve, inside the §3.7 motion: the frame fades
// and grows from 0.95 on Snappy.
func (d *DialogState) drawFrame(env *Env, gtx *layout.Context, props *DialogProps, res *DialogResult, m dialogMetrics) error {
	macro := op.Record(gtx.Ops)
	err := d.paintFrame(env, gtx, props, res, m)
	call := macro.Stop()
	if props.Inline {
		call.Add(gtx.Ops)
	} else {
		op.Defer(gtx.Ops, call)
	}
	return err
}

// paintFrame does the frame's drawing for one frame, at the current
// transform.
func (d *DialogState) paintFrame(env *Env, gtx *layout.Context, props *DialogProps, res *DialogResult, m dialogMetrics) error {
	bodyH, bodyOps, bands, scrollMax := d.runBody(env, gtx, props, m)
	if scrollMax > 0 {
		d.scroll.SetOffset(min(d.scroll.Offset(), scrollMax))
	}

	w := m.innerW + 2*m.pad
	h := 2*m.pad + m.headH + gtx.Dp(theme.SpaceSM) + m.descH + min(bodyH, m.maxBody) + gtx.Dp(theme.SpaceLG) + m.footH
	pos := image.Point{}
	if !props.Inline {
		win := env.windowRect()
		pos = clampInto(image.Pt((win.Dx()-w)/2, (win.Dy()-h)/3), image.Pt(w, h), win)
	}
	d.rect = image.Rectangle{Min: pos, Max: pos.Add(image.Pt(w, h))}
	d.bands, d.viewLenPx = bands, min(bodyH, m.maxBody)

	prog := float32(1)
	if !props.Inline {
		prog = d.open.Progress()
	}
	// A frame on its way out takes no input (§3.7): the body and the footer
	// buttons draw, but nothing under them receives events.
	active := d.open.Shown() || props.Inline
	if err := d.drawChrome(env, gtx, props, m, prog); err != nil {
		return err
	}
	d.drawBody(env, gtx, m, bodyOps, bodyH, active)
	return d.drawFooter(env, gtx, props, res, m, active)
}

// runBody runs the caller's Body once, at the body's own offset inside the
// frame — the recorded ops replay under the scroll's clip later, so the
// offset must be part of them — and returns its full height, the recorded
// ops, the bands it declared and the scroll maximum. The body's content
// takes the inner width minus the ring allowance, and a frame on its way out
// runs it disabled, so it takes no input (§3.7).
func (d *DialogState) runBody(env *Env, gtx *layout.Context, props *DialogProps, m dialogMetrics) (bodyH int, bodyOps op.CallOp, bands []dialogBand, scrollMax int) {
	if props.Body == nil {
		return 0, op.CallOp{}, nil, 0
	}
	var handle DialogBands
	bmacro := op.Record(gtx.Ops)
	bg := *gtx
	if !d.open.Shown() && !props.Inline {
		bg = gtx.Disabled()
	}
	ring := dialogRing(gtx)
	bodyTop := m.pad + m.headH + gtx.Dp(theme.SpaceSM) + m.descH
	at(&bg, image.Pt(m.pad+ring, bodyTop), func(bg *layout.Context) {
		g := *bg
		g.Constraints = layout.Exact(image.Pt(m.innerW-2*ring, 1<<20))
		bodyH = props.Body(&g, &handle).Y
	})
	bodyOps = bmacro.Stop()
	scrollMax = max(bodyH-m.maxBody, 0)
	return bodyH, bodyOps, handle.bands, scrollMax
}

// drawChrome paints the frame's surface, title and description, under the
// §3.7 motion transforms.
func (d *DialogState) drawChrome(env *Env, gtx *layout.Context, props *DialogProps, m dialogMetrics, prog float32) error {
	center := f32.Pt(float32(d.rect.Min.X+d.rect.Max.X)/2, float32(d.rect.Min.Y+d.rect.Max.Y)/2)
	defer scaleAbout(gtx, center, 0.95+0.05*prog).Pop()
	defer paint.PushOpacity(gtx.Ops, unit01(prog)).Pop()

	c := env.Th.Chrome()
	radius := gtx.Dp(theme.RadiusXL)
	DrawFloatShadow(gtx, d.rect, radius)
	fillRRect(gtx.Ops, d.rect, radius, c.SurfaceRaised)
	strokeRRect(gtx.Ops, d.rect, radius, float32(gtx.Dp(theme.Hairline)), c.BorderSubtle)
	inner := d.rect.Inset(m.pad)

	drawMenuText(env, gtx, theme.TypeTitle(), c.Fg, props.Title, image.Pt(inner.Min.X, inner.Min.Y), m.headH)
	if props.Description != "" {
		at(gtx, image.Pt(inner.Min.X, inner.Min.Y+m.headH+gtx.Dp(theme.SpaceXS)), func(gtx *layout.Context) {
			g := *gtx
			g.Constraints.Max.X = m.innerW
			dims := drawText(env, gtx, theme.TypeSmall(), c.FgMuted, props.Description, true)
			recordRun(env, image.Rectangle{Max: dims.Size}.Add(image.Pt(inner.Min.X, inner.Min.Y+m.headH+gtx.Dp(theme.SpaceXS))))
		})
	}
	return nil
}

// drawBody draws the body: its ScrollArea over the body slot grown by the
// ring allowance on every side — so a control's §3.4 ring at any edge, of a
// still or a scrolled body, keeps its whole ink — with the recorded body ops
// replayed under the scroll's clip, inset by the allowance, and a trailing
// allowance of the same size, so the last control's ring shows when fully
// scrolled.
func (d *DialogState) drawBody(env *Env, gtx *layout.Context, m dialogMetrics, bodyOps op.CallOp, bodyH int, active bool) {
	if bodyOps == (op.CallOp{}) {
		return
	}
	ring := dialogRing(gtx)
	bodyTop := d.rect.Min.Y + m.pad + m.headH + gtx.Dp(theme.SpaceSM) + m.descH
	bodyRect := image.Rect(d.rect.Min.X+m.pad, bodyTop, d.rect.Max.X-m.pad, bodyTop+min(bodyH, m.maxBody))
	viewport := growClipped(bodyRect, ring, d.rect.Inset(gtx.Dp(theme.Hairline)))

	at(gtx, viewport.Min, func(gtx *layout.Context) {
		g := *gtx
		g.Constraints = layout.Exact(viewport.Size())
		if !active {
			g = gtx.Disabled()
		}
		d.scroll.Layout(env, &g, ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
			at(gtx, image.Pt(ring, ring), func(gtx *layout.Context) {
				bodyOps.Add(gtx.Ops)
			})
			return layout.Dimensions{Size: image.Pt(viewport.Dx()-2*ring, bodyH+2*ring)}
		})
	})
}

// growClipped grows r by grow on every side, then clamps the result inside
// frame, so the ring allowance never draws past the frame's own surface.
func growClipped(r image.Rectangle, grow int, frame image.Rectangle) image.Rectangle {
	grown := image.Rect(r.Min.X-grow, r.Min.Y-grow, r.Max.X+grow, r.Max.Y+grow)
	return grown.Intersect(frame)
}

// drawFooter lays the Cancel and primary buttons out at their boxes and
// reports the outcome; a frame on its way out lays them out disabled, so it
// takes no input (§3.7).
func (d *DialogState) drawFooter(env *Env, gtx *layout.Context, props *DialogProps, res *DialogResult, m dialogMetrics, active bool) error {
	if props.Cancel == nil || props.Primary == nil {
		return nil
	}
	footTop := d.rect.Max.Y - m.pad - m.footH
	cancelPr := ButtonProps{Text: "Cancel", Variant: Secondary, Size: SizeLG}
	primaryPr := ButtonProps{Text: props.PrimaryLabel, Variant: props.PrimaryVariant, Size: SizeLG, Busy: props.Busy}
	gap := gtx.Dp(theme.SpaceMD)
	inner := d.rect.Inset(m.pad)

	x := inner.Max.X - props.Primary.Measure(env, gtx, primaryPr).Size.X
	if d.footButton(env, gtx, props.Primary, primaryPr, image.Pt(x, footTop), active) {
		res.Submitted, res.Closed = true, true
		d.close(gtx)
		return nil
	}
	x -= props.Cancel.Measure(env, gtx, cancelPr).Size.X + gap
	if d.footButton(env, gtx, props.Cancel, cancelPr, image.Pt(x, footTop), active) {
		d.cancel(gtx, res)
		return nil
	}
	return nil
}

// footButton lays one footer button out at p and reports the click; a frame
// on its way out lays the button out disabled, so it takes no input (§3.7).
func (d *DialogState) footButton(env *Env, gtx *layout.Context, st *ButtonState, pr ButtonProps, p image.Point, active bool) bool {
	var clicked bool
	var err error
	at(gtx, p, func(gtx *layout.Context) {
		ig := *gtx
		if !active {
			ig = gtx.Disabled()
		}
		clicked, err = st.Layout(env, &ig, pr)
	})
	if err != nil {
		err = fmt.Errorf("draw the footer button: %w", err)
		return false
	}
	return clicked
}
