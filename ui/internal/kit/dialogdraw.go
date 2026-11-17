package kit

import (
	"fmt"
	"image"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// The drawing half of Dialog (§6.17): the frame's metrics, the scrim and
// frame painting, the body recorded once per frame and replayed under its
// clip, and the footer buttons. The state and input half is dialog.go;
// the split follows menu.go and menudraw.go.

// dialogMetrics are the frame's resolved metrics for the frame.
type dialogMetrics struct {
	pad, headH, descH, footH, innerW int
	maxBody                          int
}

// measure derives the frame metrics from the window and the props.
func (d *DialogState) measure(env *Env, gtx *layout.Context, props DialogProps, win image.Rectangle) dialogMetrics {
	m := dialogMetrics{
		pad:    gtx.Dp(theme.SpaceXL),
		innerW: gtx.Dp(theme.DialogWidth) - 2*gtx.Dp(theme.SpaceXL),
	}
	m.headH = labelLine(env, gtx, theme.TypeTitle())
	if props.Description != "" {
		m.descH = wrappedTextHeight(env, gtx, props.Description, m.innerW) + gtx.Dp(theme.SpaceXS)
	}
	m.footH = gtx.Dp(theme.ControlLG)
	if props.Primary != nil && props.Cancel != nil {
		m.footH = max(m.footH, gtx.Dp(theme.ControlLG))
	}
	// The body gets what the window still has, at most: the frame keeps a
	// margin of space.xl to each window edge, and a body taller than that
	// scrolls instead of growing the frame past the window.
	chrome := 2*m.pad + m.headH + gtx.Dp(theme.SpaceSM) + m.descH + gtx.Dp(theme.SpaceLG) + m.footH
	m.maxBody = max(win.Dy()-chrome-2*gtx.Dp(theme.SpaceXL), gtx.Dp(theme.ControlMD))
	return m
}

// place runs the body once, resolves the frame's box and reports the footer
// buttons' clicks — the buttons lay out here, where they draw, so their hit
// areas sit at their boxes (UK-E10). The whole frame goes into one macro the
// caller's draw step defers, so the input registered inside lands on the
// overlay with the pixels.
func (d *DialogState) place(env *Env, gtx *layout.Context, props DialogProps, res *DialogResult) error {
	if props.Inline {
		d.origin = env.origin
		d.rect = image.Rectangle{}
	} else {
		d.origin = image.Point{}
	}
	if !d.open.Shown() {
		return nil
	}
	win := env.windowRect()
	if props.Inline {
		win = image.Rect(-1<<20, -1<<20, 1<<20, 1<<20)
	}
	m := d.measure(env, gtx, props, win)
	return d.draw(env, gtx, props, res, m)
}

// draw paints scrim and frame and runs the body once inside the frame. On
// the overlay everything is recorded and handed to op.Defer (UK-07); inline,
// it is placed in the flow.
func (d *DialogState) draw(env *Env, gtx *layout.Context, props DialogProps, res *DialogResult, m dialogMetrics) error {
	macro := op.Record(gtx.Ops)
	if !props.Inline && d.scrim.Value() > 0 {
		c := env.Th.Chrome()
		paint.FillShape(gtx.Ops, fade(c.Scrim, unit01(d.scrim.Value())), clip.Rect(env.windowRect()).Op())
	}
	err := d.drawFrame(env, gtx, props, res, m)
	call := macro.Stop()
	if props.Inline {
		call.Add(gtx.Ops)
	} else {
		op.Defer(gtx.Ops, call)
	}
	return err
}

// drawFrame draws the frame — surface, title, description, body, footer —
// at the box the metrics resolve. Motion: the frame fades and grows from
// 0.95 on Snappy (§3.7).
func (d *DialogState) drawFrame(env *Env, gtx *layout.Context, props DialogProps, res *DialogResult, m dialogMetrics) error {
	// Run the body once, at the inner width and at the body's own offset
	// below the description — the recorded ops replay under the body clip
	// later, so the offset must be part of them — to learn its height; a
	// frame on its way out runs it disabled, so it takes no input (§3.7).
	var bodyH int
	var scrollMax int
	var bodyOps op.CallOp
	if props.Body != nil {
		bodyTop := m.pad + m.headH + gtx.Dp(theme.SpaceSM) + m.descH
		bmacro := op.Record(gtx.Ops)
		bg := *gtx
		if !d.open.Shown() && !props.Inline {
			bg = gtx.Disabled()
		}
		bg.Constraints = layout.Constraints{Min: image.Point{}, Max: image.Pt(m.innerW, 1<<20)}
		at(&bg, image.Pt(m.pad, bodyTop), func(bg *layout.Context) {
			bodyH = props.Body(bg).Y
		})
		bodyOps = bmacro.Stop()
	}
	if bodyH > m.maxBody {
		scrollMax = bodyH - m.maxBody
		bodyH = m.maxBody
	}
	if d.scroll > scrollMax {
		d.scroll = scrollMax
	}
	if d.scroll < 0 {
		d.scroll = 0
	}

	innerW := m.innerW
	h := 2*m.pad + m.headH + gtx.Dp(theme.SpaceSM) + m.descH + bodyH + gtx.Dp(theme.SpaceLG) + m.footH
	w := innerW + 2*m.pad
	pos := image.Point{}
	if !props.Inline {
		win := env.windowRect()
		pos = clampInto(image.Pt((win.Dx()-w)/2, (win.Dy()-h)/3), image.Pt(w, h), win)
	}
	d.rect = image.Rectangle{Min: pos, Max: pos.Add(image.Pt(w, h))}

	prog := float32(1)
	if !props.Inline {
		prog = d.open.Progress()
	}
	// A frame on its way out takes no input (§3.7): the body and the footer
	// buttons draw, but nothing under them receives events.
	active := d.open.Shown() || props.Inline
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
			g.Constraints.Max.X = innerW
			dims := drawText(env, gtx, theme.TypeSmall(), c.FgMuted, props.Description, true)
			recordRun(env, image.Rectangle{Max: dims.Size}.Add(image.Pt(inner.Min.X, inner.Min.Y+m.headH+gtx.Dp(theme.SpaceXS))))
		})
	}

	// The body: clipped to its slot, scrolled by the offset, the recorded
	// body ops replayed under the clip.
	bodyTop := inner.Min.Y + m.headH + gtx.Dp(theme.SpaceSM) + m.descH
	bodyRect := image.Rect(inner.Min.X, bodyTop, inner.Max.X, bodyTop+bodyH)
	if bodyRect.Dy() > 0 && props.Body != nil {
		defer clip.Rect(bodyRect).Push(gtx.Ops).Pop()
		if active {
			d.bodyInput(gtx, bodyRect)
		}
		op.Offset(image.Pt(0, -d.scroll)).Add(gtx.Ops)
		bodyOps.Add(gtx.Ops)
	}

	// The footer: Cancel and the primary, right-aligned.
	footTop := d.rect.Max.Y - m.pad - m.footH
	if props.Cancel != nil && props.Primary != nil {
		gap := gtx.Dp(theme.SpaceMD)
		x := inner.Max.X - props.Primary.Measure(env, gtx, ButtonProps{Text: props.PrimaryLabel, Variant: props.PrimaryVariant, Size: SizeLG}).Size.X
		if clicked := d.footButton(env, gtx, props.Primary, ButtonProps{Text: props.PrimaryLabel, Variant: props.PrimaryVariant, Size: SizeLG, Busy: props.Busy}, image.Pt(x, footTop), active); clicked {
			res.Submitted, res.Closed = true, true
			d.close(gtx)
			return nil
		}
		x -= props.Cancel.Measure(env, gtx, ButtonProps{Text: "Cancel", Variant: Secondary, Size: SizeLG}).Size.X + gap
		if clicked := d.footButton(env, gtx, props.Cancel, ButtonProps{Text: "Cancel", Variant: Secondary, Size: SizeLG}, image.Pt(x, footTop), active); clicked {
			d.cancel(gtx, res)
			return nil
		}
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

// bodyInput registers the body's scroll area and applies the frame's wheel
// scrolling to the offset (§6.17: a scrollable body).
func (d *DialogState) bodyInput(gtx *layout.Context, r image.Rectangle) {
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &d.body)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &d.body, Kinds: pointer.Scroll})
		if !ok {
			return
		}
		if pe, is := ev.(pointer.Event); is && pe.Kind == pointer.Scroll {
			d.scroll = max(min(d.scroll+int(pe.Scroll.Y), 1<<20), 0)
		}
	}
}
