package theme_test

import (
	"image/color"
	"math"
	"testing"

	"gioui.org/font"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestSizeTokensMatchTheKitSpec pins every spacing, radius, size and stroke
// token to the value of ui-kit §3.1–§3.4, and every type token to §3.3: the
// table is the spec, and a silent drift would change every component at
// once.
func TestSizeTokensMatchTheKitSpec(t *testing.T) {
	t.Parallel()
	t.Run("spacing", testSpacingTokens)
	t.Run("radius", testRadiusTokens)
	t.Run("controls rows and tabs", testControlTokens)
	t.Run("icons strokes and minimums", testIconTokens)
	t.Run("type", testTypeTokens)
}

// named is one dp token with the name failures should print.
type named struct {
	name string
	got  unit.Dp
	want unit.Dp
}

// expectDp fails when a token drifted from its spec value.
func expectDp(t *testing.T, tokens []named) {
	t.Helper()
	for _, tc := range tokens {
		if tc.got != tc.want {
			t.Errorf("%s = %v dp, want %v dp", tc.name, tc.got, tc.want)
		}
	}
}

func testSpacingTokens(t *testing.T) {
	t.Parallel()
	expectDp(t, []named{
		{"space.xxs", theme.SpaceXXS, 2},
		{"space.xs", theme.SpaceXS, 4},
		{"space.sm", theme.SpaceSM, 6},
		{"space.md", theme.SpaceMD, 8},
		{"space.lg", theme.SpaceLG, 12},
		{"space.xl", theme.SpaceXL, 16},
		{"space.xxl", theme.SpaceXXL, 24},
		{"space.xxxl", theme.SpaceXXXL, 32},
	})
}

func testRadiusTokens(t *testing.T) {
	t.Parallel()
	expectDp(t, []named{
		{"radius.xs", theme.RadiusXS, 4},
		{"radius.sm", theme.RadiusSM, 6},
		{"radius.md", theme.RadiusMD, 10},
		{"radius.lg", theme.RadiusLG, 14},
		{"radius.xl", theme.RadiusXL, 16},
	})
}

func testControlTokens(t *testing.T) {
	t.Parallel()
	expectDp(t, []named{
		{"size.control.sm", theme.ControlSM, 24},
		{"size.control.md", theme.ControlMD, 28},
		{"size.control.lg", theme.ControlLG, 32},
		{"size.row", theme.Row, 28},
		{"size.row.dense", theme.RowDense, 24},
		{"size.tab", theme.Tab, 32},
	})
}

func testIconTokens(t *testing.T) {
	t.Parallel()
	expectDp(t, []named{
		{"size.icon.xs", theme.IconXS, 12},
		{"size.icon.sm", theme.IconSM, 14},
		{"size.icon.md", theme.IconMD, 16},
		{"size.icon.lg", theme.IconLG, 20},
		{"stroke.hairline", theme.Hairline, 1},
		{"stroke.focus", theme.FocusStroke, 2},
		{"focus.offset", theme.FocusOffset, 2},
		{"size.hit.min", theme.HitMin, 24},
	})
	for _, tc := range []struct {
		size unit.Dp
		want float32
	}{
		{theme.IconXS, 1.25},
		{theme.IconSM, 1.25},
		{theme.IconMD, 1.5},
		{theme.IconLG, 1.5},
	} {
		if got := theme.IconStroke(tc.size); got != tc.want {
			t.Errorf("stroke.icon at %v dp = %v, want %v", tc.size, got, tc.want)
		}
	}
}

// typeToken is one type token row of the §3.3 table.
type typeToken struct {
	name    string
	got     theme.Type
	size    unit.Sp
	line    unit.Sp
	weight  font.Weight
	mono    bool
	content bool
}

func testTypeTokens(t *testing.T) {
	t.Parallel()
	for _, tc := range []typeToken{
		{"type.caption", theme.TypeCaption(), 11, 16, font.Normal, false, false},
		{"type.small", theme.TypeSmall(), 12, 16, font.Normal, false, false},
		{"type.body", theme.TypeBody(), 13, 18, font.Normal, false, false},
		{"type.label", theme.TypeLabel(), 13, 18, font.Medium, false, false},
		{"type.prose", theme.TypeProse(), 14, 22, font.Normal, false, true},
		{"type.title", theme.TypeTitle(), 14, 20, font.Medium, false, false},
		{"type.heading", theme.TypeHeading(), 16, 24, font.Medium, false, false},
		{"type.display", theme.TypeDisplay(), 20, 28, font.Medium, false, false},
		{"type.code", theme.TypeCode(), 13, 20, font.Normal, true, true},
		{"type.codeInline", theme.TypeCodeInline(), 13, 0, font.Normal, true, false},
		{"type.codeSmall", theme.TypeCodeSmall(), 12, 16, font.Normal, true, false},
	} {
		if tc.got.Size != tc.size || tc.got.Line != tc.line || tc.got.Weight != tc.weight ||
			tc.got.Mono != tc.mono || tc.got.Content != tc.content {
			t.Errorf("%s = %+v, want size %v line %v weight %v mono %v content %v",
				tc.name, tc.got, tc.size, tc.line, tc.weight, tc.mono, tc.content)
		}
	}
}

// washTarget is one translucent token with the fraction of its tint it
// must perceive as: the sRGB composite of tint at fraction over the surface.
type washTarget struct {
	name     string
	got      color.NRGBA
	tint     color.NRGBA
	fraction float32
}

// washTargets lists every solved fill and wash of §3.5 and §3.7 for a mode,
// over its main surface (CanvasBg); the ripple over the solid Action fill is
// listed separately, over Action.
func washTargets(m *mode) (onSurface []washTarget, onAction washTarget) {
	c := m.theme.Chrome()
	red := m.theme.Palette().Red
	return []washTarget{
			{"HoverWash", c.HoverWash, c.Fg, 0.07},
			{"PressedWash", c.PressedWash, c.Fg, 0.14},
			{"SelectedWash", c.SelectedWash, c.Fg, 0.11},
			{"FillSoft", c.FillSoft, c.Fg, 0.06},
			{"FillSoftHover", c.FillSoftHover, c.Fg, 0.10},
			{"BorderSubtle", c.BorderSubtle, c.Fg, 0.09},
			{"DangerSoft", c.DangerSoft, red, 0.14},
			{"DangerSoftHover", c.DangerSoftHover, red, 0.22},
			{"DangerSoftPressed", c.DangerSoftPressed, red, 0.30},
			{"RippleWash", c.RippleWash, c.Fg, 0.11},
		},
		washTarget{"RippleOnAction", c.RippleOnAction, c.ActionFg, 0.11}
}

// TestInteractionWashesDeriveFromFg pins the §3.5 washes: every one derives
// from the mode's Fg (the danger tint from Status.Danger) at its spec
// fraction, as a perceived value — blended in linear light over the main
// surface it lands within 2 levels per channel of the sRGB composite — and
// the scrim is plain black at 40%. One derivation keeps the UK-E02 scan and
// the UK-E04 contrast checks pointed at a single place.
func TestInteractionWashesDeriveFromFg(t *testing.T) {
	t.Parallel()
	for _, m := range bothModes() {
		c := m.theme.Chrome()
		onSurface, onAction := washTargets(&m)
		for _, tc := range onSurface {
			checkPerceived(t, m.name, tc, c.CanvasBg, linearBlend(c.CanvasBg, tc.got))
		}
		checkPerceived(t, m.name, onAction, c.Action, linearBlend(c.Action, onAction.got))
		if want := fgWashOf(c.Fg, 0.35); c.FgDisabled != want {
			t.Errorf("%s FgDisabled = %v, want Fg at 35%% = %v", m.name, c.FgDisabled, want)
		}
		if want := (color.NRGBA{A: 0x66}); c.Scrim != want {
			t.Errorf("%s Scrim = %v, want black at 40%% = %v", m.name, c.Scrim, want)
		}
	}
}

// checkPerceived fails when got — what the token rendered or blended to over
// surface — is more than 2 levels per channel from the sRGB composite.
func checkPerceived(t *testing.T, mode string, tc washTarget, surface, got color.NRGBA) {
	t.Helper()
	want := srgbComposite(surface, tc.tint, tc.fraction)
	for _, d := range []struct {
		ch        string
		got, want uint8
	}{{"R", got.R, want.R}, {"G", got.G, want.G}, {"B", got.B, want.B}} {
		if diff := int(d.got) - int(d.want); diff < -2 || diff > 2 {
			t.Errorf("%s %s renders %s=%d over its surface, want the sRGB composite %d ±2", mode, tc.name, d.ch, d.got, d.want)
		}
	}
}

// srgbComposite is tint at fraction over surface in plain sRGB arithmetic:
// the perceived target of a token.
func srgbComposite(surface, tint color.NRGBA, fraction float32) color.NRGBA {
	mix := func(s, t uint8) uint8 {
		return uint8(math.Round(float64(s) + float64(fraction)*(float64(t)-float64(s))))
	}
	return color.NRGBA{R: mix(surface.R, tint.R), G: mix(surface.G, tint.G), B: mix(surface.B, tint.B), A: 0xff}
}

// linearBlend is what Gio's sRGB framebuffer shows for the translucent wash
// over an opaque surface: the blend in linear light.
func linearBlend(surface, wash color.NRGBA) color.NRGBA {
	a := float64(wash.A) / 255
	lin := func(v uint8) float64 {
		s := float64(v) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	enc := func(l float64) uint8 {
		s := l * 12.92
		if l > 0.0031308 {
			s = 1.055*math.Pow(l, 1/2.4) - 0.055
		}
		return uint8(math.Round(math.Min(math.Max(s, 0), 1) * 255))
	}
	mix := func(s, w uint8) uint8 { return enc(lin(s)*(1-a) + lin(w)*a) }
	return color.NRGBA{R: mix(surface.R, wash.R), G: mix(surface.G, wash.G), B: mix(surface.B, wash.B), A: 0xff}
}

// fgWashOf mirrors the derivation: Fg with its alpha scaled by fraction.
func fgWashOf(c color.NRGBA, fraction float32) color.NRGBA {
	c.A = uint8(float32(c.A) * fraction)
	return c
}
