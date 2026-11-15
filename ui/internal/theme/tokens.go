package theme

import (
	"gioui.org/font"
	"gioui.org/unit"
)

// The size tokens of ui-kit §3, as constants, so no component or screen ever
// writes a dp number of its own (UK-03). Colors are not here; the
// interaction washes of §3.5 are derived in newTheme from the mode's Fg.

// Spacing (§3.1): the 4 dp grid plus 2 and 6 for the insides of dense
// controls.
const (
	SpaceXXS  = unit.Dp(2)
	SpaceXS   = unit.Dp(4)
	SpaceSM   = unit.Dp(6)
	SpaceMD   = unit.Dp(8)
	SpaceLG   = unit.Dp(12)
	SpaceXL   = unit.Dp(16)
	SpaceXXL  = unit.Dp(24)
	SpaceXXXL = unit.Dp(32)
)

// Corner radii (§3.2). RadiusFull is half the element's height, computed at
// the point of use and never a constant here.
const (
	RadiusSM = unit.Dp(4)
	RadiusMD = unit.Dp(6)
	RadiusLG = unit.Dp(8)
	RadiusXL = unit.Dp(12)
)

// Control, row and tab heights (§3.4).
const (
	ControlSM = unit.Dp(24)
	ControlMD = unit.Dp(28)
	ControlLG = unit.Dp(32)
	Row       = unit.Dp(28)
	RowDense  = unit.Dp(24)
	Tab       = unit.Dp(32)
)

// Icon sizes (§3.4).
const (
	IconXS = unit.Dp(12)
	IconSM = unit.Dp(14)
	IconMD = unit.Dp(16)
	IconLG = unit.Dp(20)
)

// Strokes and minimums (§3.4).
const (
	// Hairline is the width of borders and dividers.
	Hairline = unit.Dp(1)
	// FocusStroke is the keyboard focus ring width; the ring sits FocusInset
	// inside the control's edge.
	FocusStroke = unit.Dp(2)
	FocusInset  = unit.Dp(1)
	// HitMin is the smallest pointer target (WCAG 2.5.8), even for an icon
	// of IconXS or IconSM.
	HitMin = unit.Dp(24)
)

// IconStroke returns the icon stroke width of §3.4 for an icon of size s:
// 1.25 dp at IconXS and IconSM, 1.5 dp at IconMD and IconLG. The Lucide
// icons are authored on a 24 grid with a 2 stroke, which reads too heavy
// drawn small.
func IconStroke(s unit.Dp) float32 {
	if s <= IconSM {
		return 1.25
	}
	return 1.5
}

// Type is one type token of §3.3: an absolute size and line height, with
// LineHeightScale always 1 at the point of use (§3.0). Gio's own default of
// 1.2× the font size is never allowed to win, which is why Line is absolute.
type Type struct {
	// Size is the font size in sp.
	Size unit.Sp
	// Line is the absolute line height in sp. Zero means the line height
	// belongs to the containing text (codeInline): only the markdown
	// renderer lays that token, always inside a prose run.
	Line unit.Sp
	// Weight is 400 (Normal) or 500 (Medium); UI text never goes heavier.
	Weight font.Weight
	// Mono selects JetBrains Mono instead of Inter.
	Mono bool
	// Content marks the two tokens the "content text size" setting scales
	// (§3.0): prose and code, the long reading surfaces. UI text never
	// scales with it, so the setting never touches the common metric.
	Content bool
}

// The type tokens (§3.3). Functions instead of variables so the package
// holds no state; the values are the spec's, unchanged.

// TypeCaption is the §3.3 caption token: 11/16, normal. Timestamps and
// low-importance metadata; the line is 16, not macOS's 14, for the marks.
func TypeCaption() Type { return Type{Size: 11, Line: 16, Weight: font.Normal} }

// TypeSmall is the §3.3 small token: 12/16, normal. Descriptions, helper
// text, tags, shortcuts.
func TypeSmall() Type { return Type{Size: 12, Line: 16, Weight: font.Normal} }

// TypeBody is the §3.3 body token: 13/18, normal. The default UI text.
func TypeBody() Type { return Type{Size: 13, Line: 18, Weight: font.Normal} }

// TypeLabel is the §3.3 label token: 13/18, medium. Button labels, selected
// object names, column titles; hierarchy by weight, not size.
func TypeLabel() Type { return Type{Size: 13, Line: 18, Weight: font.Medium} }

// TypeProse is the §3.3 prose token: 14/22, normal, a content token.
// Long reading surfaces: transcript, markdown.
func TypeProse() Type { return Type{Size: 14, Line: 22, Weight: font.Normal, Content: true} }

// TypeTitle is the §3.3 title token: 14/20, medium. Frame, panel and dialog
// titles.
func TypeTitle() Type { return Type{Size: 14, Line: 20, Weight: font.Medium} }

// TypeHeading is the §3.3 heading token: 16/24, medium. Screen titles.
func TypeHeading() Type { return Type{Size: 16, Line: 24, Weight: font.Medium} }

// TypeDisplay is the §3.3 display token: 20/28, medium. Large empty states;
// the biggest size the UI uses.
func TypeDisplay() Type { return Type{Size: 20, Line: 28, Weight: font.Medium} }

// TypeCode is the §3.3 code token: 13/20 mono, normal, a content token.
// Code blocks, console, output, editor.
func TypeCode() Type { return Type{Size: 13, Line: 20, Weight: font.Normal, Mono: true, Content: true} }

// TypeCodeInline is the §3.3 inline-code token: 13, mono, normal; its line
// height belongs to the prose run that contains it, so it has none of its
// own.
func TypeCodeInline() Type { return Type{Size: 13, Line: 0, Weight: font.Normal, Mono: true} }

// TypeCodeSmall is the §3.3 small code token: 12/16 mono, normal. IDs,
// hashes, paths, hosts, numbers in rows and columns.
func TypeCodeSmall() Type { return Type{Size: 12, Line: 16, Weight: font.Normal, Mono: true} }
