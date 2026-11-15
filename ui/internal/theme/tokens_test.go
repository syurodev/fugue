package theme_test

import (
	"image/color"
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
		{"radius.sm", theme.RadiusSM, 4},
		{"radius.md", theme.RadiusMD, 6},
		{"radius.lg", theme.RadiusLG, 8},
		{"radius.xl", theme.RadiusXL, 12},
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
		{"focus.inset", theme.FocusInset, 1},
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

// TestInteractionWashesDeriveFromFg pins the §3.5 washes: every one derives
// from the mode's Fg at its spec fraction, and the scrim is plain black at
// 40%. One derivation keeps the UK-E02 scan and the UK-E04 contrast checks
// pointed at a single place.
func TestInteractionWashesDeriveFromFg(t *testing.T) {
	t.Parallel()
	for _, m := range bothModes() {
		c := m.theme.Chrome()
		for _, tc := range []struct {
			name     string
			got      color.NRGBA
			fraction float32
		}{
			{"HoverWash", c.HoverWash, 0.07},
			{"PressedWash", c.PressedWash, 0.14},
			{"SelectedWash", c.SelectedWash, 0.11},
			{"FgDisabled", c.FgDisabled, 0.35},
		} {
			if want := fgWashOf(c.Fg, tc.fraction); tc.got != want {
				t.Errorf("%s %s = %v, want Fg at %v = %v", m.name, tc.name, tc.got, tc.fraction, want)
			}
		}
		if want := (color.NRGBA{A: 0x66}); c.Scrim != want {
			t.Errorf("%s Scrim = %v, want black at 40%% = %v", m.name, c.Scrim, want)
		}
	}
}

// fgWashOf mirrors the derivation: Fg with its alpha scaled by fraction.
func fgWashOf(c color.NRGBA, fraction float32) color.NRGBA {
	c.A = uint8(float32(c.A) * fraction)
	return c
}
