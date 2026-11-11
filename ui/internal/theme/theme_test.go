package theme_test

import (
	"image/color"
	"math"
	"testing"

	"gioui.org/font"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// mode bundles one theme with its name so failures name the mode.
type mode struct {
	name  string
	theme theme.Theme
}

func bothModes() []mode {
	return []mode{
		{name: "light", theme: theme.Light(nil)},
		{name: "dark", theme: theme.Dark(nil)},
	}
}

// namedColor is one token with the name failures should print.
type namedColor struct {
	name  string
	color color.NRGBA
}

// foregroundTokens lists every token that renders text or a hue on a chrome
// background, with its contrast budget.
func foregroundTokens(m *mode) []namedColor {
	p, c, s := m.theme.Palette(), m.theme.Chrome(), m.theme.Status()
	return []namedColor{
		{name: "red", color: p.Red},
		{name: "orange", color: p.Orange},
		{name: "yellow", color: p.Yellow},
		{name: "green", color: p.Green},
		{name: "teal", color: p.Teal},
		{name: "blue", color: p.Blue},
		{name: "violet", color: p.Violet},
		{name: "pink", color: p.Pink},
		{name: "status info", color: s.Info},
		{name: "status success", color: s.Success},
		{name: "status warning", color: s.Warning},
		{name: "status danger", color: s.Danger},
		{name: "link", color: m.theme.Link()},
		{name: "fg", color: c.Fg},
		{name: "fg muted", color: c.FgMuted},
		{name: "fg subtle", color: c.FgSubtle},
		{name: "icon muted", color: c.IconMuted},
	}
}

// backgroundTokens lists the five chrome backgrounds every text token and
// hue must hold up on.
func backgroundTokens(c theme.Chrome) []namedColor {
	return []namedColor{
		{name: "app bg", color: c.AppBg},
		{name: "canvas bg", color: c.CanvasBg},
		{name: "surface base", color: c.SurfaceBase},
		{name: "surface raised", color: c.SurfaceRaised},
		{name: "surface tinted", color: c.SurfaceTinted},
	}
}

// requireContrast fails unless fg reaches the 4.5:1 budget on bg.
func requireContrast(t *testing.T, label string, fg, bg color.NRGBA) {
	t.Helper()
	if ratio := contrastRatio(fg, bg); ratio < 4.5 {
		t.Errorf("%s contrasts %.2f:1 on its background, want at least 4.5:1", label, ratio)
	}
}

// TestContrastOfTextAndHueTokensOnEveryChromeBackground pins the WCAG 2.x
// contrast budget of F10.2c: every hue, status, link and text token —
// including the subtle text of comments and meta, which code blocks render
// on tinted and raised surfaces — reaches at least 4.5:1 on every chrome
// background of its mode. The action fg must also hold 4.5:1 on the hover
// and pressed fills.
func TestContrastOfTextAndHueTokensOnEveryChromeBackground(t *testing.T) {
	t.Parallel()
	for _, m := range bothModes() {
		backgrounds := backgroundTokens(m.theme.Chrome())
		for _, fgk := range foregroundTokens(&m) {
			for _, bgk := range backgrounds {
				requireContrast(t, m.name+" "+fgk.name, fgk.color, bgk.color)
			}
		}
		c := m.theme.Chrome()
		requireContrast(t, m.name+" action fg on action", c.ActionFg, c.Action)
		requireContrast(t, m.name+" action fg on action hover", c.ActionFg, c.ActionHover)
		requireContrast(t, m.name+" action fg on action pressed", c.ActionFg, c.ActionPressed)
	}
}

// TestFgSubtleStaysBelowFgMuted keeps the text hierarchy: subtle is now held
// to the same 4.5:1 budget as muted, so only its lower contrast on every
// background keeps it visibly quieter.
func TestFgSubtleStaysBelowFgMuted(t *testing.T) {
	t.Parallel()
	for _, m := range bothModes() {
		c := m.theme.Chrome()
		for _, bgk := range backgroundTokens(c) {
			subtle := contrastRatio(c.FgSubtle, bgk.color)
			muted := contrastRatio(c.FgMuted, bgk.color)
			if subtle >= muted {
				t.Errorf("%s: fg subtle contrasts %.2f:1 on %s, not below muted's %.2f:1",
					m.name, subtle, bgk.name, muted)
			}
		}
	}
}

// TestDerivedTokensPointAtTheirHues pins the derivation table: status, link
// and syntax colors are the hues they map to, defined once, in both modes.
func TestDerivedTokensPointAtTheirHues(t *testing.T) {
	t.Parallel()
	for _, m := range bothModes() {
		p, c, s := m.theme.Palette(), m.theme.Chrome(), m.theme.Status()
		derives := []struct {
			name string
			got  color.NRGBA
			want color.NRGBA
		}{
			{"info", s.Info, p.Blue},
			{"success", s.Success, p.Green},
			{"warning", s.Warning, p.Orange},
			{"danger", s.Danger, p.Red},
			{"link", m.theme.Link(), p.Blue},
			{"keyword", m.theme.Syntax(theme.RoleKeyword).Color, p.Violet},
			{"string", m.theme.Syntax(theme.RoleString).Color, p.Green},
			{"number", m.theme.Syntax(theme.RoleNumber).Color, p.Orange},
			{"type", m.theme.Syntax(theme.RoleType).Color, p.Yellow},
			{"function", m.theme.Syntax(theme.RoleFunction).Color, p.Blue},
			{"definition", m.theme.Syntax(theme.RoleDefinition).Color, c.Fg},
			{"variable", m.theme.Syntax(theme.RoleVariable).Color, c.Fg},
			{"operator", m.theme.Syntax(theme.RoleOperator).Color, c.FgMuted},
			{"comment", m.theme.Syntax(theme.RoleComment).Color, c.FgSubtle},
			{"meta", m.theme.Syntax(theme.RoleMeta).Color, c.FgSubtle},
			{"invalid", m.theme.Syntax(theme.RoleInvalid).Color, s.Danger},
		}
		for _, d := range derives {
			if d.got != d.want {
				t.Errorf("%s: %s is %#v, want the derived hue %#v", m.name, d.name, d.got, d.want)
			}
		}
		if !m.theme.Syntax(theme.RoleComment).Italic {
			t.Errorf("%s: comment must render italic", m.name)
		}
		if m.theme.Syntax(theme.RoleMeta).Italic {
			t.Errorf("%s: meta must render upright", m.name)
		}
	}
}

// TestSyntaxStylesDefaultToWeight400 pins the default weight of every role:
// no role may silently render bolder or thinner than regular.
func TestSyntaxStylesDefaultToWeight400(t *testing.T) {
	t.Parallel()
	roles := []theme.SyntaxRole{
		theme.RoleKeyword, theme.RoleString, theme.RoleNumber, theme.RoleType,
		theme.RoleFunction, theme.RoleDefinition, theme.RoleVariable,
		theme.RoleOperator, theme.RoleComment, theme.RoleMeta, theme.RoleInvalid,
	}
	for _, m := range bothModes() {
		for _, role := range roles {
			if got := m.theme.Syntax(role).Weight; got != font.Normal {
				t.Errorf("%s: %s defaults to weight %d, want %d (400)",
					m.name, role, got, font.Normal)
			}
		}
	}
}

// TestSyntaxRolesMeetContrastOnTheCodeSurface holds every syntax role, and
// the plain fallback, to 4.5:1 on the surface code blocks are drawn on, in
// both modes.
func TestSyntaxRolesMeetContrastOnTheCodeSurface(t *testing.T) {
	t.Parallel()
	roles := []theme.SyntaxRole{
		theme.RoleKeyword, theme.RoleString, theme.RoleNumber, theme.RoleType,
		theme.RoleFunction, theme.RoleDefinition, theme.RoleVariable,
		theme.RoleOperator, theme.RoleComment, theme.RoleMeta, theme.RoleInvalid,
		theme.SyntaxRole("plain"),
	}
	for _, m := range bothModes() {
		surface := m.theme.CodeSurface()
		for _, role := range roles {
			requireContrast(t, m.name+" "+string(role)+" on the code surface", m.theme.Syntax(role).Color, surface)
		}
	}
}

// TestSyntaxUnknownRoleFallsBackToMainText pins the safe degradation: a role
// this package does not know renders as plain main text.
func TestSyntaxUnknownRoleFallsBackToMainText(t *testing.T) {
	t.Parallel()
	for _, m := range bothModes() {
		got := m.theme.Syntax(theme.SyntaxRole("from-the-future"))
		if got.Color != m.theme.Chrome().Fg || got.Italic || got.Weight != font.Normal {
			t.Errorf("%s: unknown role degrades to %#v, want plain fg", m.name, got)
		}
	}
}

// TestLightAndDarkAreDistinctModes guards against the two tables collapsing
// into one: backgrounds, text and action colors must all differ.
func TestLightAndDarkAreDistinctModes(t *testing.T) {
	t.Parallel()
	light, dark := theme.Light(nil), theme.Dark(nil)
	if light.Chrome().AppBg == dark.Chrome().AppBg {
		t.Error("app bg is the same in both modes")
	}
	if light.Chrome().Fg == dark.Chrome().Fg {
		t.Error("fg is the same in both modes")
	}
	if light.Palette() == dark.Palette() {
		t.Error("the palettes are the same in both modes")
	}
	// The action pair inverts between modes: a dark fill with a light
	// foreground in light mode, the other way around in dark mode.
	if light.Chrome().Action == light.Chrome().ActionFg {
		t.Error("light mode action fill and fg must differ")
	}
	if dark.Chrome().Action == dark.Chrome().ActionFg {
		t.Error("dark mode action fill and fg must differ")
	}
}

// contrastRatio is the WCAG 2.x contrast ratio of two colors, computed here
// so the test adds no dependency.
func contrastRatio(a, b color.NRGBA) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}

// relativeLuminance is the WCAG 2.x relative luminance of a color.
func relativeLuminance(c color.NRGBA) float64 {
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// lin linearizes one sRGB channel per WCAG 2.x.
func lin(v uint8) float64 {
	s := float64(v) / 255
	if s <= 0.04045 {
		return s / 12.92
	}
	return math.Pow((s+0.055)/1.055, 2.4)
}
