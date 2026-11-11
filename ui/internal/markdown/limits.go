package markdown

import "time"

// Resource limits, all named. Vendor output is untrusted, so Parse caps
// everything it reads; when a cap bites, content is cut and Doc.Truncated
// is set (or, for the highlight caps, the code block stays plain).

const (
	// maxSourceBytes caps one Parse call; a longer source is cut at a rune
	// boundary and the document is marked truncated.
	maxSourceBytes = 1 << 20
	// maxNestingDepth caps blockquote and list nesting; deeper blocks are
	// dropped and the document is marked truncated.
	maxNestingDepth = 64
	// maxTableColumns caps a table's columns; extra columns are dropped
	// and the document is marked truncated.
	maxTableColumns = 64
	// maxTableCells caps a table's total cells (header included); extra
	// cells are dropped and the document is marked truncated.
	maxTableCells = 4096
	// maxHighlightBytes caps the source fed to the syntax lexer; a larger
	// code block stays plain.
	maxHighlightBytes = 64 << 10
	// maxHighlightLines caps the lines fed to the syntax lexer; a taller
	// code block stays plain.
	maxHighlightLines = 2000
	// tabStop is the column width tabs expand to before any text enters
	// the model, so no tab ever reaches a shaper (transcript TR-07).
	tabStop = 4
	// highlightBudget bounds one syntax tokenization. Chroma's regexes
	// time out per match (250 ms each, chroma regexp.go:375), but a block
	// within the size caps can still stack thousands of matches; the
	// budget, checked between tokens on an injected clock, caps the total.
	highlightBudget = 100 * time.Millisecond
)

// capSource cuts an over-long source at a rune boundary and reports the
// cut. Cutting a multi-byte sequence in half would put invalid UTF-8 into
// every span, so the cut backs off to a rune start.
func capSource(src string) (string, bool) {
	if len(src) <= maxSourceBytes {
		return src, false
	}
	cut := maxSourceBytes
	for cut > 0 && !isRuneStart(src[cut]) {
		cut--
	}
	return src[:cut], true
}

// isRuneStart reports whether b begins a UTF-8 sequence.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
