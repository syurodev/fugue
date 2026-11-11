package markdown_test

import (
	"strings"
	"testing"

	"github.com/syurodev/fugue/ui/internal/markdown"
)

// FuzzParse feeds arbitrary input to Parse: it must never panic, no tab
// and no unsafe character may survive into any span, and every openable
// link URL carries an allowed scheme. The ui module has no fuzz gate of
// its own, so this target runs its seed corpus under the ui test job on
// both OSes and the nightly corpus when fuzzed locally.
func FuzzParse(f *testing.F) {
	f.Add("plain text")
	f.Add("a `code` b **bold** c")
	f.Add("# heading\n\n> quote\n\n- [x] task\n")
	f.Add("| a | b |\n|:-:|--:|\n| 1 | 2 |\n")
	f.Add("```go\nfunc f() { s := \"x\" }\n```")
	f.Add("```typescript\nconst x: number = 42;\n```")
	f.Add("[l](https://example.com/x) [e](javascript:alert(1))")
	f.Add("[bidi](https://example.com/\u202Eevil\u2066)")
	f.Add("[idn](https://bücher.example/p)")
	f.Add("<div>raw</div> and <b>inline</b>")
	f.Add("![alt](https://example.com/x.png)")
	f.Add(strings.Repeat(">", 10000))
	f.Add(strings.Repeat("a", 1<<20))
	f.Add("\t\t\t tabbed \t\t")

	f.Fuzz(func(t *testing.T, src string) {
		d := markdown.Parse(src)
		walkDoc(t, d)
	})
}

// walkDoc checks every span of every block for the fuzz invariants.
func walkDoc(t *testing.T, d *markdown.Doc) {
	t.Helper()
	for i := range d.Blocks {
		walkBlock(t, &d.Blocks[i])
	}
}

// walkBlock checks one block's spans and recurses.
func walkBlock(t *testing.T, b *markdown.Block) {
	t.Helper()
	checkSpans(t, b.Spans)
	for _, line := range b.Lines {
		for _, s := range line {
			if strings.ContainsRune(s.Text, '\t') {
				t.Fatalf("tab survived into code span %q", s.Text)
			}
		}
	}
	for i := range b.Blocks {
		walkBlock(t, &b.Blocks[i])
	}
	for _, item := range b.List.Items {
		for i := range item.Blocks {
			walkBlock(t, &item.Blocks[i])
		}
	}
	for _, cell := range b.Table.Header {
		checkSpans(t, cell.Spans)
	}
	for _, row := range b.Table.Rows {
		for _, cell := range row {
			checkSpans(t, cell.Spans)
		}
	}
}

// checkSpans enforces the invariants on inline spans.
func checkSpans(t *testing.T, spans []markdown.Span) {
	t.Helper()
	for _, s := range spans {
		if strings.ContainsRune(s.Text, '\t') {
			t.Fatalf("tab survived into span %q", s.Text)
		}
		if strings.ContainsAny(s.Text, "\u202A\u202B\u202C\u202D\u202E\u2066\u2067\u2068\u2069\u200B\u200C\u200D\uFEFF") {
			t.Fatalf("unsafe rune survived into span %q", s.Text)
		}
		if l := s.Link; l != nil && l.Openable && !allowedScheme(l.URL) {
			t.Fatalf("openable link with disallowed url %q", l.URL)
		}
	}
}

// allowedScheme reports whether an openable URL starts with an allowed
// scheme, case-normalized by the sanitizer.
func allowedScheme(url string) bool {
	for _, scheme := range []string{"http://", "https://", "mailto:"} {
		if strings.HasPrefix(url, scheme) {
			return true
		}
	}
	return false
}
