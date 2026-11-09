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

//go:embed data/Inter-Variable.ttf data/JetBrainsMono-Variable.ttf
var data embed.FS

// faces are the embedded font files with the typeface each provides.
var faces = []struct {
	path     string
	typeface giofont.Typeface
}{
	{path: "data/Inter-Variable.ttf", typeface: "Inter"},
	{path: "data/JetBrainsMono-Variable.ttf", typeface: "JetBrains Mono"},
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
