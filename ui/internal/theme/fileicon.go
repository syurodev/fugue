package theme

import "image/color"

// FileColorName names one of the file-icon colors (ui-kit v0.5 §6.1). The
// generated Symbols data keeps only the name; the value comes from this
// package, so no component carries a source hex.
type FileColorName string

// The file-icon color names. They are the palette hues plus gray.
const (
	FileColorGray   FileColorName = "gray"
	FileColorRed    FileColorName = "red"
	FileColorOrange FileColorName = "orange"
	FileColorYellow FileColorName = "yellow"
	FileColorGreen  FileColorName = "green"
	FileColorTeal   FileColorName = "teal"
	FileColorBlue   FileColorName = "blue"
	FileColorViolet FileColorName = "violet"
	FileColorPink   FileColorName = "pink"
)

// FileColorNames returns the file-icon color names in table order.
func FileColorNames() []FileColorName {
	return []FileColorName{
		FileColorGray, FileColorRed, FileColorOrange, FileColorYellow, FileColorGreen,
		FileColorTeal, FileColorBlue, FileColorViolet, FileColorPink,
	}
}

// FileColor returns the icon color of one file-icon name (ui-kit v0.5 §6.1):
// the palette hue, or the muted text color for gray. Every value reaches at
// least 3:1 on SurfaceBase and SurfaceRaised in both modes (UK-E13); an
// unknown name degrades to gray rather than drawing nothing.
func (t *Theme) FileColor(n FileColorName) color.NRGBA {
	switch n {
	case FileColorRed:
		return t.palette.Red
	case FileColorOrange:
		return t.palette.Orange
	case FileColorYellow:
		return t.palette.Yellow
	case FileColorGreen:
		return t.palette.Green
	case FileColorTeal:
		return t.palette.Teal
	case FileColorBlue:
		return t.palette.Blue
	case FileColorViolet:
		return t.palette.Violet
	case FileColorPink:
		return t.palette.Pink
	case FileColorGray:
		return t.chrome.FgMuted
	default:
		return t.chrome.FgMuted
	}
}
