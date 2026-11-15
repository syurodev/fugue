package kit

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Plumbing shared by the kit's pressable controls (Button, IconButton): the
// §4.1 state snapshot, the color pairing of a state, the body painting and
// the §3.7 background transitions. Every color here is a theme token
// (UK-03): the washes are overlays drawn on top of the variant fill, so the
// resting frame shows exact tokens and only a transition shows an alpha of
// one.

// controlState is the resolved §4.1 snapshot of one control for the frame:
// the raw flags, in priority order disabled > busy > pressed > focus >
// hover; the look functions apply the priority.
type controlState struct {
	disabled bool
	busy     bool
	pressed  bool
	focused  bool
	hovered  bool
	toggled  bool
}

// resolveState derives the snapshot from the props, the clickable's real
// interaction and the gallery's Preview override (Env.Preview): one pointer
// cannot hover twelve cells of a state matrix at once, so the gallery and
// the evidence tests force the interaction states and production windows
// leave Preview zero. A disabled control never shows interaction (§4.1).
func resolveState(env *Env, gtx *layout.Context, tag event.Tag, click *widget.Clickable, disabled, busy, toggled bool) controlState {
	return controlState{
		disabled: disabled,
		busy:     busy,
		pressed:  !disabled && (click.Pressed() || env.previewPress(tag)),
		focused:  !disabled && (env.FocusVisible(gtx, tag) || env.previewFocus(tag)),
		hovered:  !disabled && (click.Hovered() || env.previewHover(tag)),
		toggled:  toggled,
	}
}

// controlLook is the color pairing of one state (UK-03, UK-E04): the fills
// painted under the content — the variant's base fill, then the hover and
// press overlays of §3.5 — and the label and icon color over them. fill
// says the variant has a base fill at all; ghost and secondary rest on the
// bare surface.
type controlLook struct {
	fill  bool
	base  color.NRGBA
	hover color.NRGBA
	press color.NRGBA
	fg    color.NRGBA
}

// drawControlBody paints the body: the base fill, then the hover or press
// overlay at its transition alpha — pressed replaces hover (§4.1: the
// stronger wash wins, it never stacks on the weaker one) — and the
// hairline border of the secondary variants. The ring is drawn by the
// caller, after the content.
func drawControlBody(env *Env, gtx *layout.Context, r image.Rectangle, radius int, look controlLook, border bool, hover, press float32) {
	c := env.Th.Chrome()
	if look.fill {
		fillRRect(gtx.Ops, r, radius, look.base)
	}
	switch {
	case press > 0:
		fillRRect(gtx.Ops, r, radius, fade(look.press, press))
	case hover > 0:
		fillRRect(gtx.Ops, r, radius, fade(look.hover, hover))
	}
	if border {
		strokeRRect(gtx.Ops, r, radius, float32(gtx.Dp(theme.Hairline)), c.Border)
	}
}

// followState moves one §3.7 transition toward target on Snappy and asks
// for the next frame while it still moves (the kit's frame discipline,
// §6.5): a page of settled buttons schedules nothing. Reduced motion snaps
// the value, the §3.7 instant change (UK-04).
func followState(env *Env, f *motion.Follower, target bool) float32 {
	v := float32(0)
	if target {
		v = 1
	}
	if env.ReducedMotion {
		f.Snap(v)
		return v
	}
	v = f.Follow(v, env.DT())
	if f.Moving() {
		env.Keep()
	}
	return v
}

// drawIconAt draws one icon in a size×size slot at p, validating the name
// at the draw site so an unknown icon surfaces as the control's error.
func drawIconAt(env *Env, gtx *layout.Context, p image.Point, name IconName, size unit.Dp, c color.NRGBA) error {
	var err error
	at(gtx, p, func(gtx *layout.Context) {
		_, err = env.Icon(gtx, name, size, c)
	})
	return err
}
