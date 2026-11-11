// Package fonts embeds the bundled faces of the desktop app and builds the
// text collection they lead: Inter for text, JetBrains Mono for code, both
// shipped with their SIL Open Font License 1.1 files (desktop-app DA-04). The
// shaper keeps system fonts behind the bundled ones, so they only serve
// glyphs the bundled faces lack.
//
// Owner: desktop app (ui module). Allowed callers: ui/cmd/fugue-app and ui
// internal packages; the core module never imports this module.
package fonts

import (
	"embed"
	"fmt"

	giofont "gioui.org/font"
	"gioui.org/font/opentype"
	"gioui.org/text"
)

const interFamily = "Inter"

// The typefaces of the bundled collection, so a layer picks a face by role and
// not by a string of its own.
const (
	// Sans is the text face.
	Sans giofont.Typeface = interFamily
	// Mono is the code face.
	Mono giofont.Typeface = "JetBrains Mono"
)

//go:embed data/*.ttf
var data embed.FS

var faces = []struct {
	path     string
	typeface giofont.Typeface
	weight   giofont.Weight
	style    giofont.Style
}{
	{"data/Inter-Regular.ttf", interFamily, giofont.Normal, giofont.Regular},
	{"data/Inter-Medium.ttf", interFamily, giofont.Medium, giofont.Regular},
	{"data/Inter-SemiBold.ttf", interFamily, giofont.SemiBold, giofont.Regular},
	{"data/Inter-Bold.ttf", interFamily, giofont.Bold, giofont.Regular},
	{"data/Inter-Italic.ttf", interFamily, giofont.Normal, giofont.Italic},
	{"data/JetBrainsMono-Regular.ttf", Mono, giofont.Normal, giofont.Regular},
	{"data/JetBrainsMono-Bold.ttf", Mono, giofont.Bold, giofont.Regular},
}

// Collection parses the bundled faces in DA-04 order: Inter first, JetBrains
// Mono second. Parsing is deterministic and the files are read-only, so a
// failure is a broken build: the error names the file for the caller to fail
// on.
func Collection() ([]text.FontFace, error) {
	var out []text.FontFace
	for _, f := range faces {
		src, err := data.ReadFile(f.path)
		if err != nil {
			return nil, fmt.Errorf("read the bundled font %s: %w", f.path, err)
		}
		parsed, err := opentype.ParseCollection(src)
		if err != nil {
			return nil, fmt.Errorf("parse the bundled font %s: %w", f.path, err)
		}
		for _, face := range parsed {
			face.Font.Typeface = f.typeface
			face.Font.Weight = f.weight
			face.Font.Style = f.style
			out = append(out, face)
		}
	}
	return out, nil
}

// Shaper builds the app's text shaper over the bundled collection, with the
// system fonts left enabled as the fallback (DA-04).
func Shaper() (*text.Shaper, error) {
	collection, err := Collection()
	if err != nil {
		return nil, err
	}
	return text.NewShaper(text.WithCollection(collection)), nil
}
