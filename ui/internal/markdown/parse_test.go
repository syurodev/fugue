package markdown_test

import (
	goparser "go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/ui/internal/markdown"
)

// firstBlock returns the document's first block of the given kind.
func firstBlock(t *testing.T, src string, kind markdown.BlockKind) markdown.Block {
	t.Helper()
	d := markdown.Parse(src)
	for i := range d.Blocks {
		if d.Blocks[i].Kind == kind {
			return d.Blocks[i]
		}
	}
	t.Fatalf("no %v block in %q", kind, src)
	return markdown.Block{}
}

// spanText concatenates a span slice's text.
func spanText(spans []markdown.Span) string {
	var sb strings.Builder
	for _, s := range spans {
		sb.WriteString(s.Text)
	}
	return sb.String()
}

// TestParseKeepsSpacesAroundCodeAndBold pins the DA-E03 whitespace rule:
// the spaces around inline code and bold, and between adjacent spans, are
// exactly the source's.
func TestParseKeepsSpacesAroundCodeAndBold(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		src  string
		want []string
	}{
		{src: "a `code` b", want: []string{"a ", "code", " b"}},
		{src: "x **bold** y", want: []string{"x ", "bold", " y"}},
		{src: "no-space `c`", want: []string{"no-space ", "c"}},
		{src: "many    spaces  `c`  here", want: []string{"many    spaces  ", "c", "  here"}},
	} {
		t.Run(tc.src, func(t *testing.T) {
			t.Parallel()
			para := firstBlock(t, tc.src, markdown.BlockParagraph)
			got := make([]string, 0, len(para.Spans))
			for _, s := range para.Spans {
				got = append(got, s.Text)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("spans %q, want %q", got, tc.want)
			}
		})
	}
}

// testHost is the example host of the link cases; a constant keeps the
// string count below the goconst threshold.
const testHost = "example.com"

// findList returns the first list block in a slice, or nil.
func findList(blocks []markdown.Block) *markdown.List {
	for i := range blocks {
		if blocks[i].Kind == markdown.BlockList {
			return &blocks[i].List
		}
	}
	return nil
}

// TestParseNestedListsAndTaskList checks list nesting, ordering and the
// read-only task state.
func TestParseNestedListsAndTaskList(t *testing.T) {
	t.Parallel()
	src := "3. third\n   - inner\n      - deepest\n\n- [x] done\n- [ ] open\n"
	blocks := markdown.Parse(src).Blocks
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(blocks))
	}
	t.Run("nested", func(t *testing.T) {
		t.Parallel()
		checkNesting(t, blocks[0].List)
	})
	t.Run("tasks", func(t *testing.T) {
		t.Parallel()
		checkTasks(t, blocks[1].List)
	})
}

// checkNesting verifies the three nested levels and the start number.
func checkNesting(t *testing.T, outer markdown.List) {
	t.Helper()
	if !outer.Ordered || outer.Start != 3 || len(outer.Items) != 1 {
		t.Fatalf("outer list wrong: %+v", outer)
	}
	inner := findList(outer.Items[0].Blocks)
	if inner == nil || inner.Ordered || len(inner.Items) != 1 {
		t.Fatalf("inner list wrong: %+v", inner)
	}
	deepest := findList(inner.Items[0].Blocks)
	if deepest == nil || deepest.Ordered || len(deepest.Items) != 1 {
		t.Fatalf("deepest list wrong: %+v", deepest)
	}
}

// checkTasks verifies the two task items and their checked state.
func checkTasks(t *testing.T, tasks markdown.List) {
	t.Helper()
	if len(tasks.Items) != 2 {
		t.Fatalf("task list wrong: %+v", tasks)
	}
	if !tasks.Items[0].Task || !tasks.Items[0].Checked || spanText(tasks.Items[0].Blocks[0].Spans) != "done" {
		t.Fatalf("checked task wrong: %+v", tasks.Items[0])
	}
	if !tasks.Items[1].Task || tasks.Items[1].Checked {
		t.Fatalf("unchecked task wrong: %+v", tasks.Items[1])
	}
}

// TestParseTablesWithColumnAlignment checks per-column alignment.
func TestParseTablesWithColumnAlignment(t *testing.T) {
	t.Parallel()
	table := firstBlock(t, "| L | C | R | N |\n|:-|:-:|-:|-|\n| a | b | c | d |\n",
		markdown.BlockTable).Table
	want := []markdown.Align{markdown.AlignLeft, markdown.AlignCenter, markdown.AlignRight, markdown.AlignNone}
	if len(table.Aligns) != len(want) {
		t.Fatalf("aligns %v, want %v", table.Aligns, want)
	}
	for i := range want {
		if table.Aligns[i] != want[i] {
			t.Fatalf("align %d = %v, want %v", i, table.Aligns[i], want[i])
		}
	}
	if len(table.Header) != 4 || len(table.Rows) != 1 {
		t.Fatalf("table shape wrong: %+v", table)
	}
	if spanText(table.Rows[0][3].Spans) != "d" {
		t.Fatalf("cell text wrong: %q", spanText(table.Rows[0][3].Spans))
	}
}

// TestParseExpandsTabsAtAFourColumnTabStop checks that no tab survives
// into text or code, and that stops are absolute per line.
func TestParseExpandsTabsAtAFourColumnTabStop(t *testing.T) {
	t.Parallel()
	code := firstBlock(t, "```\nab\tc\n\tabcd\tx\n```", markdown.BlockCode)
	want := []string{"ab  c", "    abcd    x"}
	if len(code.Lines) != len(want) {
		t.Fatalf("lines %d, want %d", len(code.Lines), len(want))
	}
	for i := range want {
		if got := spanText(code.Lines[i]); got != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got, want[i])
		}
	}
	para := firstBlock(t, "text\tspan", markdown.BlockParagraph)
	if got := spanText(para.Spans); got != "text    span" {
		t.Fatalf("text = %q", got)
	}
}

// containsRole reports whether any code span carries the role.
func containsRole(lines [][]markdown.Span, role markdown.Role) bool {
	for _, line := range lines {
		for _, s := range line {
			if s.Role == role {
				return true
			}
		}
	}
	return false
}

// TestParseHighlightsGoAndTypeScript checks that known languages are
// tokenized into the stable roles.
func TestParseHighlightsGoAndTypeScript(t *testing.T) {
	t.Parallel()
	goBlock := firstBlock(t, "```go\nfunc main() { s := \"x\" /* c */ }\n```",
		markdown.BlockCode)
	if !goBlock.Highlighted {
		t.Fatal("go block not highlighted")
	}
	for _, role := range []markdown.Role{
		markdown.RoleKeyword, markdown.RoleFunction,
		markdown.RoleString, markdown.RoleOperator,
	} {
		if !containsRole(goBlock.Lines, role) {
			t.Fatalf("go block missing role %v", role)
		}
	}
	ts := firstBlock(t, "```typescript\nconst x: number = 42; // answer\n```",
		markdown.BlockCode)
	if !ts.Highlighted {
		t.Fatal("typescript block not highlighted")
	}
	for _, role := range []markdown.Role{
		markdown.RoleKeyword, markdown.RoleType,
		markdown.RoleNumber, markdown.RoleComment,
	} {
		if !containsRole(ts.Lines, role) {
			t.Fatalf("typescript block missing role %v", role)
		}
	}
}

// TestParseUnknownOrOversizedCodeStaysPlain checks that an unknown
// language is never guessed and the size caps leave a block plain.
func TestParseUnknownOrOversizedCodeStaysPlain(t *testing.T) {
	t.Parallel()
	unknown := firstBlock(t, "```neverheardof\nsome code\n```", markdown.BlockCode)
	if unknown.Highlighted || unknown.Language != "neverheardof" {
		t.Fatalf("unknown language highlighted: %+v", unknown)
	}
	bare := firstBlock(t, "```\nplain\n```", markdown.BlockCode)
	if bare.Highlighted {
		t.Fatal("language-less block highlighted")
	}
	tall := firstBlock(t, "```go\n"+strings.Repeat("x\n", 2001)+"```", markdown.BlockCode)
	if tall.Highlighted {
		t.Fatal("over-2000-line block highlighted")
	}
	fat := firstBlock(t, "```go\n"+strings.Repeat("y", 65537)+"\n```", markdown.BlockCode)
	if fat.Highlighted {
		t.Fatal("over-64KiB block highlighted")
	}
}

// steppingClock advances a fixed step per Now call, so the highlight
// budget is spent deterministically without real time.
type steppingClock struct {
	step time.Duration
	n    int
}

// Now implements markdown.Clock.
func (c *steppingClock) Now() time.Time {
	t := time.Unix(0, 0).Add(c.step * time.Duration(c.n))
	c.n++
	return t
}

// TestHighlightOverBudgetStaysPlain checks that a highlight call whose
// clock walks past the budget between tokens leaves the code block plain.
func TestHighlightOverBudgetStaysPlain(t *testing.T) {
	t.Parallel()
	src := "```go\nfunc f() { x := 1; y := \"s\"; z := 3 }\n```"
	fast := markdown.Parse(src, markdown.WithClock(&steppingClock{step: time.Millisecond}))
	if !fast.Blocks[0].Highlighted {
		t.Fatal("control: block not highlighted with a slow-stepping clock")
	}
	slow := markdown.Parse(src, markdown.WithClock(&steppingClock{step: 60 * time.Millisecond}))
	if slow.Blocks[0].Highlighted {
		t.Fatal("over-budget block still highlighted")
	}
	if len(slow.Blocks[0].Lines) == 0 {
		t.Fatal("over-budget block lost its lines")
	}
	for _, line := range slow.Blocks[0].Lines {
		for _, span := range line {
			if span.Role != markdown.RolePlain && span.Role != "" {
				t.Fatalf("over-budget block kept role %v", span.Role)
			}
		}
	}
}

// linkOf returns the first link span's link in a paragraph.
func linkOf(spans []markdown.Span) *markdown.Link {
	for _, s := range spans {
		if s.Link != nil {
			return s.Link
		}
	}
	return nil
}

// TestParseSanitizesLinks checks openability, unsafe characters, the two
// IDN host forms and the host-spoofing destinations: userinfo,
// backslash- userinfo tricks and percent-encoded bidi or zero-width runes
// in the host never yield an openable link.
func TestParseSanitizesLinks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		src         string
		openable    bool
		host        string
		puny        string
		urlContains string
	}{
		{src: "[a](https://example.com/x)", openable: true, host: testHost, puny: testHost, urlContains: "https://example.com/x"},
		{src: "[a](http://example.com)", openable: true, host: testHost, puny: testHost},
		{src: "[a](mailto:someone@example.org)", openable: true},
		{src: "[a](javascript:alert(1))"},
		{src: "[a](file:///etc/passwd)"},
		{src: "[a](data:text/html,x)"},
		{src: "[a](vbscript:x)"},
		{src: "[a](./relative)"},
		{src: "[a](FTP://example.com/f)"},
		{src: "[a](https://bücher.example/x)", openable: true, host: "bücher.example", puny: "xn--bcher-kva.example", urlContains: "https://xn--bcher-kva.example/x"},
		{src: "[a](https://xn--bcher-kva.example/x)", openable: true, host: "bücher.example", puny: "xn--bcher-kva.example"},
		{src: "[a](https://example.com/\u202Eevil)", openable: true, host: testHost, puny: testHost, urlContains: "https://example.com/evil"},
		// Host-spoofing destinations: the display host must be the host
		// the browser would open, or the link is not openable.
		{src: `[a](https://evil.example\@good.example/login)`},
		{src: "[a](https://alice@good.example/)"},
		{src: "[a](https://%E2%80%AEelpmaxe.live/)", host: "elpmaxe.live", puny: "elpmaxe.live"},
		{src: "[a](https://exa%E2%80%8Bmple.com/)", host: testHost, puny: testHost},
		{src: "[a](https://good.example%2F@evil.example)"},
	} {
		t.Run(tc.src, func(t *testing.T) {
			t.Parallel()
			l := linkOf(firstBlock(t, tc.src, markdown.BlockParagraph).Spans)
			if l == nil {
				t.Fatal("no link parsed")
			}
			if l.Openable != tc.openable {
				t.Fatalf("openable = %v, want %v", l.Openable, tc.openable)
			}
			if l.Host != tc.host || l.HostPunycode != tc.puny {
				t.Fatalf("host = %q puny = %q, want %q / %q", l.Host, l.HostPunycode, tc.host, tc.puny)
			}
			if tc.urlContains != "" && l.URL != tc.urlContains {
				t.Fatalf("url = %q, want %q", l.URL, tc.urlContains)
			}
		})
	}
}

// TestParseRawHTMLIsLiteralText checks that block and inline raw HTML
// survive as literal spans.
func TestParseRawHTMLIsLiteralText(t *testing.T) {
	t.Parallel()
	html := firstBlock(t, "<div class=\"x\">hi</div>", markdown.BlockHTML)
	if got := spanText(html.Spans); !strings.Contains(got, "<div") || !strings.Contains(got, "</div>") {
		t.Fatalf("html block = %q", got)
	}
	para := firstBlock(t, "a <b>bold</b> c", markdown.BlockParagraph)
	if got := spanText(para.Spans); got != "a <b>bold</b> c" {
		t.Fatalf("inline html = %q", got)
	}
}

// TestParseImagesKeepAltAndHostWithoutLoading checks that an image keeps
// its alt text and destination host and has no openable state.
func TestParseImagesKeepAltAndHostWithoutLoading(t *testing.T) {
	t.Parallel()
	para := firstBlock(t, "![alt text](https://example.com/x.png)", markdown.BlockParagraph)
	var img *markdown.Image
	for _, s := range para.Spans {
		if s.Image != nil {
			img = s.Image
		}
	}
	if img == nil {
		t.Fatal("no image parsed")
	}
	if img.Alt != "alt text" || img.Host != testHost || img.URL != "https://example.com/x.png" {
		t.Fatalf("image = %+v", img)
	}
	if spanText(para.Spans) != "alt text" {
		t.Fatalf("alt not shown as text: %q", spanText(para.Spans))
	}
}

// TestParseResourceLimits checks that hostile inputs parse to a bounded
// document without panicking: 10 000 nesting levels, a 1 000-column table
// and a 1 MiB single line.
func TestParseResourceLimits(t *testing.T) {
	t.Parallel()
	t.Run("deep nesting", func(t *testing.T) {
		t.Parallel()
		src := strings.Repeat(">", 10000) + " bottom"
		d := markdown.Parse(src)
		if !d.Truncated {
			t.Fatal("deep nesting not marked truncated")
		}
	})
	t.Run("wide table", func(t *testing.T) {
		t.Parallel()
		row := "|" + strings.Repeat(" a |", 1000)
		src := row + "\n" + strings.Repeat("|-", 1000) + "|\n" + row
		table := firstBlock(t, src, markdown.BlockTable)
		if len(table.Table.Aligns) > 64 || len(table.Table.Header) > 64 {
			t.Fatalf("table columns uncapped: %d", len(table.Table.Aligns))
		}
	})
	t.Run("long line", func(t *testing.T) {
		t.Parallel()
		d := markdown.Parse(strings.Repeat("a", 1<<20))
		if len(d.Blocks) == 0 {
			t.Fatal("1 MiB line parsed to nothing")
		}
	})
	t.Run("oversized source", func(t *testing.T) {
		t.Parallel()
		d := markdown.Parse(strings.Repeat("a\n", 600000))
		if !d.Truncated {
			t.Fatal("over-cap source not marked truncated")
		}
	})
}

// TestParseNeverImportsGio pins the package contract: the model layer
// never imports a GUI toolkit, so it stays drawable by any backend.
func TestParseNeverImportsGio(t *testing.T) {
	t.Parallel()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := goparser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, goparser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imp := range f.Imports {
			if strings.HasPrefix(imp.Path.Value, `"gioui.org`) {
				t.Fatalf("%s imports %s", e.Name(), imp.Path.Value)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no package files found")
	}
}
