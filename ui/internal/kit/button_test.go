package kit

import (
	"fmt"
	"image/color"
	"math"
	"testing"

	"gioui.org/io/key"
	"gioui.org/text"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// labelSave is the label the button tests draw.
const labelSave = "Save"

// focusButton lays one frame out and moves focus to the button the way a
// real window's Tab handler would: the headless router does not synthesize
// focus movement, so the test issues the FocusCmd itself (ui-guide §4).
func focusButton(t *testing.T, h *harness, st *ButtonState, p ButtonProps) {
	t.Helper()
	h.frame(t)
	if _, err := st.Layout(h.env, &h.gtx, p); err != nil {
		t.Fatalf("lay the button out: %v", err)
	}
	h.gtx.Execute(key.FocusCmd{Tag: st.Tag()})
	h.end()
}

// clickFrame lays one frame out and reports whether the button clicked in
// it (UK-06: the event is read in the same frame).
func clickFrame(t *testing.T, h *harness, st *ButtonState, p ButtonProps) bool {
	t.Helper()
	h.frame(t)
	clicked, err := st.Layout(h.env, &h.gtx, p)
	if err != nil {
		t.Fatalf("lay the button out: %v", err)
	}
	h.end()
	return clicked
}

// TestButtonActivatesOnEnterAndOnSpaceRelease covers §6.2: a focused button
// activates on Enter and on Space, Space only when the key is released —
// the activation arrives in the frame that saw the key (UK-06).
func TestButtonActivatesOnEnterAndOnSpaceRelease(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewButtonState()
	p := ButtonProps{Text: labelSave, Variant: Secondary}

	// Register the input filters, then take focus, then sit quiet.
	h.frame(t)
	if _, err := st.Layout(h.env, &h.gtx, p); err != nil {
		t.Fatalf("lay the button out: %v", err)
	}
	h.end()
	focusButton(t, h, st, p)
	if clickFrame(t, h, st, p) {
		t.Fatal("the button clicked with no key")
	}

	// Enter activates.
	h.router.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	h.router.Queue(key.Event{Name: key.NameReturn, State: key.Release})
	if !clickFrame(t, h, st, p) {
		t.Fatal("Enter did not activate the focused button")
	}

	// Space waits for the release (§6.2).
	h.router.Queue(key.Event{Name: key.NameSpace, State: key.Press})
	if clickFrame(t, h, st, p) {
		t.Fatal("Space activated while held; it must wait for the release")
	}
	h.router.Queue(key.Event{Name: key.NameSpace, State: key.Release})
	if !clickFrame(t, h, st, p) {
		t.Fatal("Space release did not activate the focused button")
	}

	// Busy blocks repeat activation (§4.1).
	busy := p
	busy.Busy = true
	h.router.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	h.router.Queue(key.Event{Name: key.NameReturn, State: key.Release})
	if clickFrame(t, h, st, busy) {
		t.Fatal("a busy button must not activate again")
	}
}

// TestButtonBusyKeepsItsWidth covers §6.2: busy swaps the content inside the
// box measured from the idle content, so the size never moves — the measured
// box ignores Busy, and the drawn body of a busy frame covers exactly the
// pixels the idle body does.
func TestButtonBusyKeepsItsWidth(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		p    ButtonProps
	}{
		{"text only", ButtonProps{Text: "Run checks", Variant: Primary}},
		{"icon and text", ButtonProps{Text: "Run checks", Icon: IconRestart, Variant: Secondary}},
		{"long label", ButtonProps{Text: "Rebuild every index now", Variant: Primary, Size: SizeLG, Icon: IconRestart}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			st := NewButtonState()
			busy := tc.p
			busy.Busy = true
			h.frame(t)
			if idle, got := st.Measure(h.env, &h.gtx, tc.p).Size, st.Measure(h.env, &h.gtx, busy).Size; idle != got {
				t.Fatalf("busy moved the measured box from %v to %v", idle, got)
			}
			if tc.p.Variant != Primary {
				return
			}
			idle := bodyBounds(t, h, st, tc.p, false)
			if got := bodyBounds(t, h, st, busy, true); got != idle {
				t.Fatalf("the busy body covers %v, the idle one %v", got, idle)
			}
		})
	}
}

// TestBusyShowsNoHoverOrPressWash covers §4.1: a busy control draws no hover
// or press wash — nor does a disabled one — even when the pointer is on it.
func TestBusyShowsNoHoverOrPressWash(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	st := NewButtonState()
	h.env.Preview = Preview{Hover: st.Tag(), Press: st.Tag()}
	h.frame(t)
	for _, o := range []pressOpts{{busy: true}, {disabled: true}, {}} {
		got := resolveState(h.env, &h.gtx, st.Tag(), &st.click, o)
		if live := !o.busy && !o.disabled; got.hovered != live || got.pressed != live {
			t.Errorf("%+v: hovered %v pressed %v, want both %v", o, got.hovered, got.pressed, live)
		}
	}
}

// TestButtonDisabledDrawsTheWholeControlAtDisabledOpacity covers §4.1: every
// color of a disabled look — fill, border and label — is the enabled one at
// DisabledOpacity.
func TestButtonDisabledDrawsTheWholeControlAtDisabledOpacity(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	for _, v := range []ButtonVariant{Secondary, Primary, Bordered, Ghost, Danger} {
		on, off := buttonLookOf(h.env, v, false), buttonLookOf(h.env, v, true)
		for name, pair := range map[string][2]color.NRGBA{
			"base": {on.base, off.base}, "border": {on.border, off.border}, "fg": {on.fg, off.fg}, "ripple": {on.ripple, off.ripple},
		} {
			if want := fade(pair[0], theme.DisabledOpacity); pair[1] != want {
				t.Errorf("variant %d %s disabled = %v, want %v", v, name, pair[1], want)
			}
		}
	}
}

// TestButtonTextContrastInEveryState covers UK-E04: the label and icon
// color reaches 4.5:1 on the composited background of rest, hover, pressed
// and selected, in both modes, on every chrome surface the gallery puts
// under a control, for every variant including danger on DangerSoft in
// DangerFg. A disabled control draws at DisabledOpacity and is exempt
// (§4.1).
func TestButtonTextContrastInEveryState(t *testing.T) {
	t.Parallel()
	for _, mode := range themeModes() {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			sh, err := fonts.Shaper()
			if err != nil {
				t.Fatalf("build the shaper: %v", err)
			}
			th := mode.th(sh)
			checkContrastInEveryState(t, &Env{Th: &th, Sh: sh})
		})
	}
}

// checkContrastInEveryState asserts the 4.5:1 budget of every case on every
// chrome surface of env's mode.
func checkContrastInEveryState(t *testing.T, env *Env) {
	t.Helper()
	for _, surface := range contrastSurfaces(env.Th) {
		for _, tc := range contrastCases(env) {
			bg := composited(surface.c, tc.look, tc.hovered, tc.pressed, tc.toggled)
			fg := tc.look.fg
			if tc.hovered || tc.pressed {
				fg = tc.look.fgHover
			}
			if got := contrastRatio(fg, bg); got < 4.5 {
				t.Errorf("%s on %s: contrast %.2f below 4.5", tc.name, surface.name, got)
			}
		}
	}
}

// contrastSurface is one chrome surface a control can rest on.
type contrastSurface struct {
	name string
	c    color.NRGBA
}

// contrastSurfaces are the surfaces the gallery draws controls over.
func contrastSurfaces(th *theme.Theme) []contrastSurface {
	c := th.Chrome()
	return []contrastSurface{
		{"AppBg", c.AppBg},
		{"CanvasBg", c.CanvasBg},
		{"SurfaceBase", c.SurfaceBase},
		{"SurfaceRaised", c.SurfaceRaised},
		{"SurfaceTinted", c.SurfaceTinted},
	}
}

// contrastCase is one control state and the look it settles into.
type contrastCase struct {
	name                      string
	look                      controlLook
	hovered, pressed, toggled bool
}

// contrastCases are every Button and IconButton variant at rest, hovered,
// pressed (pointer still over it) and, for icons, toggled with and without
// hover. Busy shares the rest look: it adds no wash (§4.1).
func contrastCases(env *Env) []contrastCase {
	cases := make([]contrastCase, 0, 5*3+3*6)
	for _, v := range []ButtonVariant{Secondary, Primary, Bordered, Ghost, Danger} {
		look := buttonLookOf(env, v, false)
		cases = append(cases,
			contrastCase{name: fmt.Sprintf("button %d rest", v), look: look},
			contrastCase{name: fmt.Sprintf("button %d hover", v), look: look, hovered: true},
			contrastCase{name: fmt.Sprintf("button %d pressed", v), look: look, hovered: true, pressed: true})
	}
	for _, v := range []IconButtonVariant{IconButtonGhost, IconButtonSecondary, IconButtonBordered} {
		rest, tog := iconButtonLookOf(env, v, false, false), iconButtonLookOf(env, v, true, false)
		cases = append(cases,
			contrastCase{name: fmt.Sprintf("icon %d rest", v), look: rest},
			contrastCase{name: fmt.Sprintf("icon %d hover", v), look: rest, hovered: true},
			contrastCase{name: fmt.Sprintf("icon %d pressed", v), look: rest, hovered: true, pressed: true},
			contrastCase{name: fmt.Sprintf("icon %d selected", v), look: tog, toggled: true},
			contrastCase{name: fmt.Sprintf("icon %d selected hovered", v), look: tog, toggled: true, hovered: true},
			contrastCase{name: fmt.Sprintf("icon %d selected pressed", v), look: tog, toggled: true, hovered: true, pressed: true})
	}
	return cases
}

// composited layers the fills a settled state actually draws over an
// opaque surface, in the order paint draws them, with the weights paint
// itself computes (layerWeights) for the settled transitions.
func composited(surface color.NRGBA, look controlLook, hovered, pressed, toggled bool) color.NRGBA {
	var mv motionVals
	if hovered {
		mv.hover = 1
	}
	if pressed {
		mv.press = 1
	}
	if toggled {
		mv.sel = 1
	}
	base, sel, hover, press := layerWeights(mv)
	bg := surface
	for _, l := range []struct {
		c color.NRGBA
		w float32
	}{{look.base, base}, {look.sel, sel}, {look.hover, hover}, {look.press, press}} {
		bg = over(bg, fade(l.c, l.w))
	}
	return bg
}

// over blends src over the opaque dst the way the GPU does: Gio draws into
// an sRGB framebuffer, which blends in linear light, so a translucent Fg
// wash over a dark surface lifts it far more than the same alpha would in
// sRGB arithmetic (measured: Fg at 6% over AppBg renders as 0x44, not 0x21).
func over(dst, src color.NRGBA) color.NRGBA {
	a := float64(src.A) / 255
	mix := func(d, s uint8) uint8 {
		return fromLinear(toLinear(s)*a + toLinear(d)*(1-a))
	}
	return color.NRGBA{R: mix(dst.R, src.R), G: mix(dst.G, src.G), B: mix(dst.B, src.B), A: 0xff}
}

// toLinear and fromLinear convert one sRGB channel to and from linear light.
func toLinear(v uint8) float64 {
	s := float64(v) / 255
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}

func fromLinear(l float64) uint8 {
	s := l * 12.92
	if l > 0.0031308 {
		s = 1.055*math.Pow(l, 1/2.4) - 0.055
	}
	return uint8(math.Round(min(max(s, 0), 1) * 255))
}

// contrastRatio is the WCAG 2.x contrast of two opaque colors.
func contrastRatio(a, b color.NRGBA) float64 {
	la, lb := luminance(a), luminance(b)
	hi, lo := max(la, lb), min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

// luminance is the WCAG relative luminance of an opaque color.
func luminance(c color.NRGBA) float64 {
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// TestFocusRingReaches3to1InEveryVariant covers UK-E04 for the ring of §3.8:
// the solid ring of every Button and IconButton variant — Action for primary,
// Status.Danger for danger, FocusBorder for the rest — reaches 3:1 against
// every chrome surface around it, in both modes, with the opaque colors
// compared directly (a solid ring has no blend to lift it).
func TestFocusRingReaches3to1InEveryVariant(t *testing.T) {
	t.Parallel()
	for _, mode := range []struct {
		name string
		th   func(*text.Shaper) theme.Theme
	}{{"light", theme.Light}, {"dark", theme.Dark}} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			sh, err := fonts.Shaper()
			if err != nil {
				t.Fatalf("build the shaper: %v", err)
			}
			th := mode.th(sh)
			for name, ring := range variantRings(&Env{Th: &th, Sh: sh}) {
				checkRing(t, &th, name, ring)
			}
		})
	}
}

// variantRings is the ring color of every Button and IconButton variant.
func variantRings(env *Env) map[string]color.NRGBA {
	rings := map[string]color.NRGBA{}
	for _, v := range []ButtonVariant{Secondary, Primary, Bordered, Ghost, Danger} {
		rings[fmt.Sprintf("button %d", v)] = buttonLookOf(env, v, false).ring
	}
	for _, v := range []IconButtonVariant{IconButtonGhost, IconButtonSecondary, IconButtonBordered} {
		rings[fmt.Sprintf("icon %d", v)] = iconButtonLookOf(env, v, false, false).ring
	}
	return rings
}

// checkRing asserts one ring is solid and reaches 3:1 on every chrome surface.
func checkRing(t *testing.T, th *theme.Theme, name string, ring color.NRGBA) {
	t.Helper()
	if ring.A != 0xff {
		t.Errorf("%s ring %v is not solid", name, ring)
	}
	for _, surface := range contrastSurfaces(th) {
		if got := contrastRatio(ring, surface.c); got < 3 {
			t.Errorf("%s ring on %s: %.2f:1, want at least 3:1", name, surface.name, got)
		}
	}
}
