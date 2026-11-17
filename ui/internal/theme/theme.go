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
	// FgSubtle is tertiary text (comments, meta): at least 4.5:1, still
	// below FgMuted on every background so the hierarchy holds.
	FgSubtle color.NRGBA
	// Placeholder is the placeholder text inside a field: derived per mode
	// between FgMuted and Fg, because the placeholder must keep 4.5:1 over
	// the field's lifted fills (FillSoft, FillSoftHover) on every chrome
	// surface, and neither text token alone does (UK-E04).
	Placeholder color.NRGBA
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
	// The interaction washes of ui-kit §3.5, derived from this mode's Fg so
	// they keep their contrast on every surface Fg is readable on. A
	// component never mixes a wash of its own; drawing Fg-derived washes
	// over a surface is exactly drawing text on it, one derivation to check.
	// HoverWash overlays a resting row, ghost button or tab under the
	// pointer.
	HoverWash color.NRGBA
	// PressedWash overlays a control while it is held down.
	PressedWash color.NRGBA
	// SelectedWash is the background of the selected item, stronger than
	// HoverWash so selection stays visible while another row hovers.
	SelectedWash color.NRGBA
	// FgDisabled is the text and icon color of a disabled control outside a
	// control (a disabled menu item, a row's secondary text); a disabled
	// control itself draws whole at DisabledOpacity.
	FgDisabled color.NRGBA
	// Scrim is the dim behind a dialog: black at 40%, mode-independent.
	Scrim color.NRGBA
	// FillSoft is the tinted fill of the secondary button and the neutral
	// tag: Fg at 6% (v0.3); FillSoftHover is the same at 10%.
	FillSoft      color.NRGBA
	FillSoftHover color.NRGBA
	// BorderSubtle is the faint border of bordered controls, cards, inputs
	// and floating surfaces: Fg at 9%. Border stays for Divider.
	BorderSubtle color.NRGBA
	// DangerSoft is the tinted fill of the danger button: Status.Danger at
	// 14%; DangerFg is the label color on it, the danger hue shifted until
	// it holds 4.5:1 over that fill and its hover and press fills on every
	// chrome surface.
	DangerSoft color.NRGBA
	DangerFg   color.NRGBA
	// DangerSoftHover and DangerSoftPressed are the same tint at 22% and 30%
	// perceived, the danger variant's hover and press fills (HeroUI deepens a
	// flat danger the same way); DangerFg holds 4.5:1 on all three.
	DangerSoftHover   color.NRGBA
	DangerSoftPressed color.NRGBA
	// RippleWash is the press ripple over a control body, and RippleOnAction
	// the one over the solid Action fill: about 11% perceived, faint on top
	// of the whole-body pressed fill (§3.7).
	RippleWash     color.NRGBA
	RippleOnAction color.NRGBA
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

// Tone is a tinted surface fill and its ≥4.5:1 foreground text/icon color
// (§3.5, §6.4).
type Tone struct {
	Fill color.NRGBA
	Fg   color.NRGBA
}

// Hue names one of the eight palette hues (§6.4).
type Hue uint8

const (
	HueRed Hue = iota
	HueOrange
	HueYellow
	HueGreen
	HueTeal
	HueBlue
	HueViolet
	HuePink
	numHues
)

// StatusKind names one of the four status tones (§3.5, §6.4).
type StatusKind uint8

const (
	StatusInfo StatusKind = iota
	StatusSuccess
	StatusWarning
	StatusDanger
)

// Theme is one immutable mode: every token plus the derived status, link and
// syntax colors, the shaper its Material() hands to widgets, and the one
// material theme those tokens seed — built once here, never per frame.
type Theme struct {
	chrome   Chrome
	palette  Palette
	status   Status
	hueTones [numHues]Tone
	link     color.NRGBA
	syntax   map[SyntaxRole]SyntaxStyle
	shaper   *text.Shaper
	mat      *material.Theme
}

// Light builds the light mode over shaper.
func Light(shaper *text.Shaper) Theme {
	c := lightChrome()
	return newTheme(&c, lightPalette(), shaper)
}

// Dark builds the dark mode over shaper.
func Dark(shaper *text.Shaper) Theme {
	c := darkChrome()
	return newTheme(&c, darkPalette(), shaper)
}

// newTheme assembles one mode: chrome and palette are the tables, and every
// derived color is computed here so the mapping exists exactly once.
func newTheme(c *Chrome, p Palette, shaper *text.Shaper) Theme {
	// The §3.5 washes derive from Fg here, once per mode, solved to their
	// perceived value (wash.go), so no component ever fades a color of its
	// own.
	surface := c.CanvasBg
	c.HoverWash = perceived(surface, c.Fg, 0.07)
	c.PressedWash = perceived(surface, c.Fg, 0.14)
	c.SelectedWash = perceived(surface, c.Fg, 0.11)
	c.FgDisabled = fgWash(c.Fg, 0.35)
	c.Scrim = color.NRGBA{A: 0x66} // black at 40%
	c.FillSoft = perceived(surface, c.Fg, 0.06)
	c.FillSoftHover = perceived(surface, c.Fg, 0.10)
	c.Placeholder = placeholderOf(c.FgMuted, c.Fg)
	c.BorderSubtle = perceived(surface, c.Fg, 0.09)
	c.DangerSoft = perceived(surface, p.Red, 0.14)
	c.DangerSoftHover = perceived(surface, p.Red, 0.22)
	c.DangerSoftPressed = perceived(surface, p.Red, 0.30)
	c.RippleWash = perceived(surface, c.Fg, 0.11)
	c.RippleOnAction = perceived(c.Action, c.ActionFg, 0.11)
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
		RoleComment:    {Color: c.FgSubtle, Weight: defaultWeight},
		RoleMeta:       {Color: c.FgSubtle, Weight: defaultWeight},
		RoleInvalid:    {Color: status.Danger, Weight: defaultWeight},
	}
	hueTones := [numHues]Tone{
		HueRed:    {Fill: c.DangerSoft, Fg: c.DangerFg},
		HueOrange: softTone(c, p.Orange),
		HueYellow: softTone(c, p.Yellow),
		HueGreen:  softTone(c, p.Green),
		HueTeal:   softTone(c, p.Teal),
		HueBlue:   softTone(c, p.Blue),
		HueViolet: softTone(c, p.Violet),
		HuePink:   softTone(c, p.Pink),
	}
	t := Theme{
		chrome:   *c,
		palette:  p,
		status:   status,
		hueTones: hueTones,
		link:     p.Blue,
		syntax:   syntax,
		shaper:   shaper,
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

// NeutralTone returns the FillSoft + Fg pair of a neutral tag (§3.5, §6.4).
func (t *Theme) NeutralTone() Tone { return Tone{Fill: t.chrome.FillSoft, Fg: t.chrome.Fg} }

// HueTone returns the soft fill and ≥4.5:1 foreground color of hue h (§6.4).
// Out-of-range hues fall back to NeutralTone.
func (t *Theme) HueTone(h Hue) Tone {
	if h >= numHues {
		return t.NeutralTone()
	}
	return t.hueTones[h]
}

// StatusTone returns the soft fill and ≥4.5:1 foreground color of status s
// (§3.5, §6.4).
func (t *Theme) StatusTone(s StatusKind) Tone {
	switch s {
	case StatusInfo:
		return t.hueTones[HueBlue]
	case StatusSuccess:
		return t.hueTones[HueGreen]
	case StatusWarning:
		return t.hueTones[HueOrange]
	case StatusDanger:
		return t.hueTones[HueRed]
	default:
		return t.NeutralTone()
	}
}

// Link returns the hyperlink color, derived from the palette (blue).
func (t *Theme) Link() color.NRGBA { return t.link }

// CodeSurface returns the surface code blocks and inline code sit on. Every
// syntax role reaches 4.5:1 on it in both modes.
func (t *Theme) CodeSurface() color.NRGBA { return t.chrome.SurfaceTinted }

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
		DangerFg:      nrgba(0x752822),
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
		DangerFg:      nrgba(0xF4B8B3),
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

// fgWash returns c with its alpha scaled by fraction, clamped to [0, 1]:
// the Fg-derived wash of §3.5.
func fgWash(c color.NRGBA, fraction float32) color.NRGBA {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	c.A = uint8(float32(c.A) * fraction)
	return c
}
