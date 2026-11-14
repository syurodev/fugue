package kitspike

import (
	"gioui.org/font"
	"gioui.org/unit"
)

// The tokens of ui-kit §3, as constants, so every component draws from one
// place. Colors are NOT here: they come from theme (UK-03); the interaction
// washes of §3.5 are derived in washes.go until theme owns them.

// Spacing (§3.1), the 4 dp grid plus 2 and 6.
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

// Corner radii (§3.2).
const (
	RadiusSM = unit.Dp(4)
	RadiusMD = unit.Dp(6)
	RadiusLG = unit.Dp(8)
	RadiusXL = unit.Dp(12)
)

// Control and row sizes (§3.4).
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
	// Hairline is borders and dividers.
	Hairline = unit.Dp(1)
	// FocusStroke is the keyboard focus ring width; it sits FocusInset
	// inside the control's edge.
	FocusStroke = unit.Dp(2)
	FocusInset  = unit.Dp(1)
	// HitMin is the smallest pointer target (WCAG 2.5.8).
	HitMin = unit.Dp(24)
	// HandleWidth is the visible width of a SplitPane handle (hairline);
	// HandleHit is how wide the pointer target around it is (§6.27).
	HandleHit = unit.Dp(8)
	// HandleStep is how far one arrow press moves a focused handle;
	// HandleStepBig with Shift.
	HandleStep    = unit.Dp(16)
	HandleStepBig = unit.Dp(64)
	// IconButtonGapSM/MD is the icon-to-label gap of a control sized sm
	// resp. md/lg (§6.2).
	IconButtonGapSM = SpaceXS
	IconButtonGapMD = SpaceSM
	// ButtonPadSM/MDLG is the horizontal padding of a button sized sm
	// resp. md/lg (§6.2).
	ButtonPadSM   = SpaceMD
	ButtonPadMDLG = SpaceLG
)

// IconStroke returns the icon stroke width for an icon of size s
// (§3.4): 1.25 dp at 12 and 14, 1.5 dp at 16 and 20.
func IconStroke(s unit.Dp) float32 {
	if s <= IconSM {
		return 1.25
	}
	return 1.5
}

// Type is one type token of §3.3: an absolute size and line height, with
// LineHeightScale always 1 (§3.0).
type Type struct {
	Size   unit.Sp
	Line   unit.Sp
	Weight font.Weight
	Mono   bool
}

// The type tokens (§3.3).
var (
	TypeCaption   = Type{Size: 11, Line: 16, Weight: font.Normal}
	TypeSmall     = Type{Size: 12, Line: 16, Weight: font.Normal}
	TypeBody      = Type{Size: 13, Line: 18, Weight: font.Normal}
	TypeLabel     = Type{Size: 13, Line: 18, Weight: font.Medium}
	TypeProse     = Type{Size: 14, Line: 22, Weight: font.Normal}
	TypeTitle     = Type{Size: 14, Line: 20, Weight: font.Medium}
	TypeHeading   = Type{Size: 16, Line: 24, Weight: font.Medium}
	TypeDisplay   = Type{Size: 20, Line: 28, Weight: font.Medium}
	TypeCode      = Type{Size: 13, Line: 20, Weight: font.Normal, Mono: true}
	TypeCodeSmall = Type{Size: 12, Line: 16, Weight: font.Normal, Mono: true}
)
