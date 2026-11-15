package kit

import (
	"image/color"
	"math"
	"testing"

	"gioui.org/io/key"
	"gioui.org/text"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

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
	p := ButtonProps{Text: "Save", Variant: Secondary}

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

// TestButtonBusyKeepsItsWidth covers §6.2: busy swaps the content inside
// the box measured from the idle content — with an icon the spinner takes
// the icon's slot, without one it replaces the content centered — and the
// size never moves.
func TestButtonBusyKeepsItsWidth(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		p    ButtonProps
	}{
		{"text only", ButtonProps{Text: "Run checks", Variant: Primary}},
		{"icon and text", ButtonProps{Text: "Run checks", Icon: IconRestart, Variant: Secondary}},
		{"long label", ButtonProps{Text: "Rebuild every index now", Variant: Ghost, Size: SizeLG}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			st := NewButtonState()
			h.frame(t)
			idle, err := st.draw(h.env, &h.gtx, tc.p, controlState{})
			if err != nil {
				t.Fatalf("draw the idle button: %v", err)
			}
			h.end()
			busy := tc.p
			busy.Busy = true
			h.frame(t)
			got, err := st.draw(h.env, &h.gtx, busy, controlState{busy: true})
			if err != nil {
				t.Fatalf("draw the busy button: %v", err)
			}
			h.end()
			if got.Size != idle.Size {
				t.Fatalf("busy moved the button from %v to %v; busy must keep the idle size", idle.Size, got.Size)
			}
		})
	}
}

// TestButtonTextContrastInEveryState covers UK-E04: the label and icon
// color reaches 4.5:1 on the composited background of rest, hover, pressed
// and selected, in both modes, on every chrome surface the gallery puts
// under a control. FgDisabled is the §4.1 disabled look and is exempt;
// busy is in the matrix to pin that it never changes the pairing.
func TestButtonTextContrastInEveryState(t *testing.T) {
	t.Parallel()
	for _, mode := range []struct {
		name string
		th   func(*text.Shaper) theme.Theme
	}{
		{"light", theme.Light},
		{"dark", theme.Dark},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			sh, err := fonts.Shaper()
			if err != nil {
				t.Fatalf("build the shaper: %v", err)
			}
			th := mode.th(sh)
			env := &Env{Th: &th, Sh: sh}
			surfaces := contrastSurfaces(&th)
			checkContrast(t, surfaces, buttonContrastCases(env))
			checkContrast(t, surfaces, iconContrastCases(env))
		})
	}
}

// contrastSurface is one chrome surface a control can rest on.
type contrastSurface struct {
	name string
	c    color.NRGBA
}

// contrastSurfaces are the surfaces the gallery draws controls over.
func contrastSurfaces(th *theme.Theme) []contrastSurface {
	return []contrastSurface{
		{"AppBg", th.Chrome().AppBg},
		{"CanvasBg", th.Chrome().CanvasBg},
		{"SurfaceBase", th.Chrome().SurfaceBase},
		{"SurfaceRaised", th.Chrome().SurfaceRaised},
	}
}

// contrastCase is one control state and the look it settles into.
type contrastCase struct {
	name    string
	look    controlLook
	hovered bool
	pressed bool
}

// buttonContrastCases are the Button variant × state cases: rest, hover,
// pressed (with the pointer still over it) and busy.
func buttonContrastCases(env *Env) []contrastCase {
	states := []struct {
		name string
		st   controlState
	}{
		{"rest", controlState{}},
		{"hover", controlState{hovered: true}},
		{"pressed", controlState{hovered: true, pressed: true}},
		{"busy", controlState{busy: true}},
	}
	cases := make([]contrastCase, 0, len(states)*4)
	for _, v := range []ButtonVariant{Primary, Secondary, Ghost, Danger} {
		for _, bs := range states {
			look, _ := buttonLookOf(env, v, bs.st)
			cases = append(cases, contrastCase{buttonVariantName(v) + " " + bs.name, look, bs.st.hovered, bs.st.pressed})
		}
	}
	return cases
}

// iconContrastCases are the IconButton variant × state cases, selected
// being the toggled state of §6.3.
func iconContrastCases(env *Env) []contrastCase {
	states := []struct {
		name string
		st   controlState
	}{
		{"rest", controlState{}},
		{"hover", controlState{hovered: true}},
		{"pressed", controlState{hovered: true, pressed: true}},
		{"selected", controlState{toggled: true}},
		{"selected and hovered", controlState{toggled: true, hovered: true}},
	}
	cases := make([]contrastCase, 0, len(states)*2)
	for _, v := range []IconButtonVariant{IconButtonGhost, IconButtonSecondary} {
		for _, is := range states {
			look, _ := iconButtonLookOf(env, v, is.st)
			cases = append(cases, contrastCase{"icon button " + is.name, look, is.st.hovered, is.st.pressed})
		}
	}
	return cases
}

// checkContrast asserts every case's label color reaches 4.5:1 on its
// composited background over every surface.
func checkContrast(t *testing.T, surfaces []contrastSurface, cases []contrastCase) {
	t.Helper()
	for _, tc := range cases {
		for _, surface := range surfaces {
			bg := composited(surface.c, tc.look, tc.hovered, tc.pressed)
			if got := contrastRatio(tc.look.fg, bg); got < 4.5 {
				t.Fatalf("%s on %s: contrast %.2f below 4.5", tc.name, surface.name, got)
			}
		}
	}
}

// buttonVariantName names a variant for the failure line.
func buttonVariantName(v ButtonVariant) string {
	switch v {
	case Primary:
		return "primary"
	case Secondary:
		return "secondary"
	case Ghost:
		return "ghost"
	case Danger:
		return "danger"
	}
	return "unknown"
}

// composited layers the fills a settled state actually draws over an
// opaque surface, in draw order: exactly what the GPU composites (the
// washes ride at their token alpha, and pressed replaces hover, §4.1).
func composited(surface color.NRGBA, look controlLook, hovered, pressed bool) color.NRGBA {
	bg := surface
	if look.base.A > 0 {
		bg = over(bg, look.base)
	}
	switch {
	case pressed:
		bg = over(bg, look.press)
	case hovered:
		bg = over(bg, look.hover)
	}
	return bg
}

// over blends src over dst.
func over(dst, src color.NRGBA) color.NRGBA {
	a := float64(src.A) / 255
	mix := func(d, s uint8) uint8 {
		return uint8(math.Round(float64(s)*a + float64(d)*(1-a)))
	}
	return color.NRGBA{R: mix(dst.R, src.R), G: mix(dst.G, src.G), B: mix(dst.B, src.B), A: 0xff}
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
