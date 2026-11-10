// Package theme defines the color tokens shared by every screen of the app
// (F10.2c): a neutral chrome tier that all widgets use, and an eight-hue
// palette tier for syntax, status and accents. There are exactly two modes,
// light and dark (DA-08); the tables are constants tuned so every text token
// and hue reaches a WCAG contrast of at least 4.5:1 on every chrome
// background of its mode; the subtle text token holds the same 4.5:1
// budget because code blocks sit on tinted and raised surfaces.
//
// Owner: F10.2c. Allowed callers: every ui/ package that draws; nothing
// outside ui/.
package theme

import (
	"image/color"

	"gioui.org/font"
	"gioui.org/text"
	"gioui.org/widget/material"
)

// SyntaxRole names the highlighting roles shared with the markdown renderer
// (F10.3a/F10.3b). The names are the roles' stable identifiers; screens map
// their spans to them and never pick colors of their own.
type SyntaxRole string

// The syntax roles. Definition and variable fall back to the main text; the
// renderer still emits them so spans keep their role even when unstyled.
const (
	RoleKeyword    SyntaxRole = "keyword"
	RoleString     SyntaxRole = "string"
	RoleNumber     SyntaxRole = "number"
	RoleType       SyntaxRole = "type"
	RoleFunction   SyntaxRole = "function"
	RoleDefinition SyntaxRole = "definition"
	RoleVariable   SyntaxRole = "variable"
	RoleOperator   SyntaxRole = "operator"
	RoleComment    SyntaxRole = "comment"
	RoleMeta       SyntaxRole = "meta"
	RoleInvalid    SyntaxRole = "invalid"
)

// defaultWeight is the weight every syntax style starts from: regular
// (400 in CSS units, which Gio's font package encodes as zero).
const defaultWeight = font.Normal

// SyntaxStyle is how one syntax role renders: its color, weight and posture.
type SyntaxStyle struct {
	Color  color.NRGBA
	Weight font.Weight
	Italic bool
}

// Chrome is the neutral tier: backgrounds, text levels, borders and the
// action color every widget uses. Chrome never references a hue; accents
// come from Palette or the derived sets.
type Chrome struct {
	// AppBg is the window's base background, behind every surface.
	AppBg color.NRGBA
	// CanvasBg is the reading surface of the main content area.
	CanvasBg color.NRGBA
	// SurfaceBase is a resting card or panel surface.
	SurfaceBase color.NRGBA
	// SurfaceRaised is a surface that floats over Base (menus, popovers).
	SurfaceRaised color.NRGBA
	// SurfaceTinted is a Base surface with a wash (quote blocks, code chips).
	SurfaceTinted color.NRGBA
	// Fg is the main text color.
	Fg color.NRGBA
	// FgMuted is secondary text (labels, meta lines): at least 4.5:1.
	FgMuted color.NRGBA
	// FgSubtle is tertiary text (comments, meta, placeholders): at least
	// 4.5:1, still below FgMuted on every background so the hierarchy holds.
	FgSubtle color.NRGBA
	// IconMuted is the quiet icon color; same contrast budget as FgMuted.
	IconMuted color.NRGBA
	// Border is the hairline border color.
	Border color.NRGBA
	// FocusBorder is the keyboard-focus indicator color.
	FocusBorder color.NRGBA
	// Action is the primary action fill. In dark mode it is light with a
	// dark foreground; in light mode the other way around.
	Action color.NRGBA
	// ActionFg is the text color on Action.
	ActionFg color.NRGBA
	// ActionHover is Action's fill on hover.
	ActionHover color.NRGBA
	// ActionPressed is Action's fill while pressed.
	ActionPressed color.NRGBA
}

// Palette is the eight-hue tier for syntax, status and accents. Hues are
// tuned per mode so each one reaches at least 4.5:1 on every chrome
// background of that mode; screens never adjust them.
type Palette struct {
	Red    color.NRGBA
	Orange color.NRGBA
	Yellow color.NRGBA
	Green  color.NRGBA
	Teal   color.NRGBA
	Blue   color.NRGBA
	Violet color.NRGBA
	Pink   color.NRGBA
}

// Status maps the connection and outcome states onto hues, derived once so
// no screen picks its own status color.
type Status struct {
	Info    color.NRGBA
	Success color.NRGBA
	Warning color.NRGBA
	Danger  color.NRGBA
}

// Theme is one immutable mode: every token plus the derived status, link and
// syntax colors, the shaper its Material() hands to widgets, and the one
// material theme those tokens seed — built once here, never per frame.
type Theme struct {
	chrome  Chrome
	palette Palette
	status  Status
	link    color.NRGBA
	syntax  map[SyntaxRole]SyntaxStyle
	shaper  *text.Shaper
	mat     *material.Theme
}

// Light builds the light mode over shaper.
func Light(shaper *text.Shaper) Theme {
	return newTheme(lightChrome(), lightPalette(), shaper)
}

// Dark builds the dark mode over shaper.
func Dark(shaper *text.Shaper) Theme {
	return newTheme(darkChrome(), darkPalette(), shaper)
}

// newTheme assembles one mode: chrome and palette are the tables, and every
// derived color is computed here so the mapping exists exactly once.
func newTheme(c Chrome, p Palette, shaper *text.Shaper) Theme {
	status := Status{
		Info:    p.Blue,
		Success: p.Green,
		Warning: p.Orange,
		Danger:  p.Red,
	}
	syntax := map[SyntaxRole]SyntaxStyle{
		RoleKeyword:    {Color: p.Violet, Weight: defaultWeight},
		RoleString:     {Color: p.Green, Weight: defaultWeight},
		RoleNumber:     {Color: p.Orange, Weight: defaultWeight},
		RoleType:       {Color: p.Yellow, Weight: defaultWeight},
		RoleFunction:   {Color: p.Blue, Weight: defaultWeight},
		RoleDefinition: {Color: c.Fg, Weight: defaultWeight},
		RoleVariable:   {Color: c.Fg, Weight: defaultWeight},
		RoleOperator:   {Color: c.FgMuted, Weight: defaultWeight},
		RoleComment:    {Color: c.FgSubtle, Weight: defaultWeight, Italic: true},
		RoleMeta:       {Color: c.FgSubtle, Weight: defaultWeight},
		RoleInvalid:    {Color: status.Danger, Weight: defaultWeight},
	}
	t := Theme{
		chrome:  c,
		palette: p,
		status:  status,
		link:    p.Blue,
		syntax:  syntax,
		shaper:  shaper,
	}
	t.mat = t.newMaterialTheme()
	return t
}

// Chrome returns the neutral token set of this mode.
func (t *Theme) Chrome() Chrome { return t.chrome }

// Palette returns the eight-hue token set of this mode.
func (t *Theme) Palette() Palette { return t.palette }

// Status returns the derived status colors of this mode.
func (t *Theme) Status() Status { return t.status }

// Link returns the hyperlink color, derived from the palette (blue).
func (t *Theme) Link() color.NRGBA { return t.link }

// Syntax returns the style of one syntax role. Unknown roles render as
// unstyled main text, so a renderer that meets a new role degrades safely.
func (t *Theme) Syntax(role SyntaxRole) SyntaxStyle {
	if style, ok := t.syntax[role]; ok {
		return style
	}
	return SyntaxStyle{Color: t.chrome.Fg, Weight: defaultWeight}
}

// Shaper returns the shaper shared by the mode's Material themes.
func (t *Theme) Shaper() *text.Shaper { return t.shaper }

// lightChrome is the light mode's neutral table; see the plan for how each
// value was tuned.
func lightChrome() Chrome {
	return Chrome{
		AppBg:         nrgba(0xF2F2F0),
		CanvasBg:      nrgba(0xFFFFFF),
		SurfaceBase:   nrgba(0xFFFFFF),
		SurfaceRaised: nrgba(0xE9E9EB),
		SurfaceTinted: nrgba(0xE4E4E8),
		Fg:            nrgba(0x1C1D20),
		FgMuted:       nrgba(0x52535A),
		FgSubtle:      nrgba(0x656570),
		IconMuted:     nrgba(0x52535A),
		Border:        nrgba(0xC9C9CE),
		FocusBorder:   nrgba(0x1C1D20),
		Action:        nrgba(0x1C1D20),
		ActionFg:      nrgba(0xFFFFFF),
		ActionHover:   nrgba(0x2E3036),
		ActionPressed: nrgba(0x0A0B0C),
	}
}

// darkChrome is the dark mode's neutral table; the action pair inverts to a
// light fill with a dark foreground.
func darkChrome() Chrome {
	return Chrome{
		AppBg:         nrgba(0x14161A),
		CanvasBg:      nrgba(0x1A1D22),
		SurfaceBase:   nrgba(0x1F2228),
		SurfaceRaised: nrgba(0x272B33),
		SurfaceTinted: nrgba(0x23272E),
		Fg:            nrgba(0xF0F0F2),
		FgMuted:       nrgba(0xA8AAB2),
		FgSubtle:      nrgba(0x919199),
		IconMuted:     nrgba(0xA8AAB2),
		Border:        nrgba(0x3B3F48),
		FocusBorder:   nrgba(0xF0F0F2),
		Action:        nrgba(0xF0F0F2),
		ActionFg:      nrgba(0x14161A),
		ActionHover:   nrgba(0xD6D6DA),
		ActionPressed: nrgba(0xFFFFFF),
	}
}

// lightPalette is the light mode's hue table; lightness is capped by 4.5:1
// on the white surfaces, so yellow and orange read olive and brown.
func lightPalette() Palette {
	return Palette{
		Red:    nrgba(0xB43D34),
		Orange: nrgba(0x8D5B29),
		Yellow: nrgba(0x7A6423),
		Green:  nrgba(0x227537),
		Teal:   nrgba(0x217267),
		Blue:   nrgba(0x2A5AE1),
		Violet: nrgba(0x7B3FE4),
		Pink:   nrgba(0xB33474),
	}
}

// darkPalette is the dark mode's hue table; lightness is lifted by 4.5:1 on
// the app background.
func darkPalette() Palette {
	return Palette{
		Red:    nrgba(0xDC736B),
		Orange: nrgba(0xCE7F30),
		Yellow: nrgba(0xAF8E29),
		Green:  nrgba(0x3FA558),
		Teal:   nrgba(0x3DA194),
		Blue:   nrgba(0x728FDE),
		Violet: nrgba(0xA084D0),
		Pink:   nrgba(0xCB77A1),
	}
}

// nrgba builds an opaque color from 0xRRGGBB.
func nrgba(v uint32) color.NRGBA {
	return color.NRGBA{
		R: uint8(v >> 16 & 0xff),
		G: uint8(v >> 8 & 0xff),
		B: uint8(v & 0xff),
		A: 0xff,
	}
}
