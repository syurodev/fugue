package markdown

import (
	"strings"
	"unicode/utf8"
)

// Text and URL hygiene: vendor output must not smuggle control characters,
// bidi overrides or zero-width characters into anything the app shows, and
// no tab may reach the layout layer (transcript TR-07: tabs become spaces
// before drawing).

// expandTabs replaces every tab with the spaces that advance col to the
// next multiple of tabStop and returns the new column. Columns count runes
// from the line start; a newline resets the caller's column.
func expandTabs(s string, col int) (string, int) {
	if !strings.ContainsRune(s, '\t') {
		return s, col + utf8.RuneCountInString(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\t':
			n := tabStop - col%tabStop
			b.WriteString(strings.Repeat(" ", n))
			col += n
		case '\n':
			b.WriteRune(r)
			col = 0
		default:
			b.WriteRune(r)
			col++
		}
	}
	return b.String(), col
}

// sanitizeText removes the characters a hostile source can use to forge or
// corrupt displayed text: C0 controls except newline and tab (tabs are
// expanded before this runs), DEL, the bidi overrides U+202A–U+202E and
// U+2066–U+2069, and the zero-width characters U+200B–U+200D and U+FEFF.
func sanitizeText(s string) string {
	return strings.Map(func(r rune) rune {
		if unsafeRune(r) {
			return -1
		}
		return r
	}, s)
}

// unsafeRune reports whether r is removed from text and URLs.
func unsafeRune(r rune) bool {
	switch {
	case r == '\n', r == '\t':
		return false
	case r < 0x20 || r == 0x7F:
		return true
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	case r >= 0x200B && r <= 0x200D, r == 0xFEFF:
		return true
	default:
		return false
	}
}

// sanitizeURL removes the unsafe runes from a destination before it is
// parsed; newline and tab go too, since neither belongs in a URL and a
// tab inside one would reach the layout layer unexpanded.
func sanitizeURL(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || unsafeRune(r) {
			return -1
		}
		return r
	}, s)
}
