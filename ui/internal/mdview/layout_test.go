package mdview

import (
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/font"
	"gioui.org/io/key"
	"golang.org/x/image/math/fixed"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestLayoutUsesTheBundledFontsAndWeights checks that bold is the font
// weight, code is JetBrains Mono and comments ask for italic.
func TestLayoutUsesTheBundledFontsAndWeights(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "plain", 400)
	v := &view{th: &h.th, st: &h.st, shaper: h.th.Shaper()}
	body := flowStyle{px: fixed.I(15)}
	cases := []struct {
		name string
		span markdown.Span
		want font.Font
	}{
		{"plain", markdown.Span{}, font.Font{Typeface: fonts.Sans}},
		{"bold", markdown.Span{Style: markdown.StyleBold}, font.Font{Typeface: fonts.Sans, Weight: font.Bold}},
		{"italic", markdown.Span{Style: markdown.StyleItalic}, font.Font{Typeface: fonts.Sans, Style: font.Italic}},
		{"inline code", markdown.Span{Style: markdown.StyleCode}, font.Font{Typeface: fonts.Mono}},
		{"comment", markdown.Span{Role: markdown.RoleComment}, font.Font{Typeface: fonts.Sans, Style: font.Italic}},
	}
	for _, c := range cases {
		if got, _ := v.face(&c.span, body); got != c.want {
			t.Errorf("%s: font = %+v, want %+v", c.name, got, c.want)
		}
	}
	mono := font.Font{Typeface: fonts.Mono}
	if i, w := v.shape("iiii", mono, fixed.I(15)).w, v.shape("WWWW", mono, fixed.I(15)).w; i != w {
		t.Errorf("code widths differ (%d vs %d): the text did not shape with the monospaced face", i, w)
	}
	sans := font.Font{Typeface: fonts.Sans}
	if i, w := v.shape("iiii", sans, fixed.I(15)).w, v.shape("WWWW", sans, fixed.I(15)).w; i == w {
		t.Errorf("text widths are equal (%d): the text face is not proportional", i)
	}
	if reg, bold := v.shape("Fugue", sans, fixed.I(15)).w, v.shape("Fugue", font.Font{Typeface: fonts.Sans, Weight: font.Bold}, fixed.I(15)).w; reg == bold {
		t.Errorf("bold and regular share the width %d: the weight did not select the bold face", reg)
	}
}

// TestNoTabReachesTheShaper checks that a tab shapes exactly as the spaces of
// the tab stop, in a word, in a gap and in a whole code span, and that a
// document full of tabs lays out with only spaces in its atoms' widths.
func TestNoTabReachesTheShaper(t *testing.T) {
	t.Parallel()
	h := newHarness(t, "a\tb\n\n```go\n\tx := 1\n```\n", 400)
	v := &view{th: &h.th, st: &h.st, shaper: h.th.Shaper()}
	mono := font.Font{Typeface: fonts.Mono}
	tab, spaces := v.shape("a\tb", mono, fixed.I(14)), v.shape("a    b", mono, fixed.I(14))
	if tab.w != spaces.w || len(tab.glyphs) != len(spaces.glyphs) {
		t.Errorf("a tab shaped to %d px in %d glyphs, four spaces to %d px in %d glyphs", tab.w, len(tab.glyphs), spaces.w, len(spaces.glyphs))
	}
	for _, b := range h.doc.Blocks {
		for _, s := range slices.Concat(b.Spans, slices.Concat(b.Lines...)) {
			if strings.Contains(s.Text, "\t") {
				t.Errorf("the model handed a tab to the layout: %q", s.Text)
			}
		}
	}
	h.frame()
}

// TestSyntaxRolesMapOneToOneOntoTheThemeRoles pins the string contract the
// layout relies on to map the model's roles onto the shared palette.
func TestSyntaxRolesMapOneToOneOntoTheThemeRoles(t *testing.T) {
	t.Parallel()
	pairs := map[markdown.Role]theme.SyntaxRole{
		markdown.RoleKeyword: theme.RoleKeyword, markdown.RoleOperator: theme.RoleOperator,
		markdown.RoleVariable: theme.RoleVariable, markdown.RoleFunction: theme.RoleFunction,
		markdown.RoleType: theme.RoleType, markdown.RoleDefinition: theme.RoleDefinition,
		markdown.RoleString: theme.RoleString, markdown.RoleNumber: theme.RoleNumber,
		markdown.RoleComment: theme.RoleComment, markdown.RoleMeta: theme.RoleMeta,
		markdown.RoleInvalid: theme.RoleInvalid,
	}
	for m, want := range pairs {
		if got := theme.SyntaxRole(m); got != want {
			t.Errorf("model role %q maps to theme role %q, want %q", m, got, want)
		}
	}
}

// TestCodeBlocksScrollSidewaysAndNeverWrapInsideAToken checks that a code line
// wider than the block stays one line, wider than the block.
func TestCodeBlocksScrollSidewaysAndNeverWrapInsideAToken(t *testing.T) {
	t.Parallel()
	long := "const veryLongIdentifierName = someFunction(argumentNumberOne, argumentNumberTwo, argumentNumberThree)"
	h := newHarness(t, "```\n"+long+"\nshort\n```\n", 200)
	dims := h.frame()
	lines, widest := 0, 0
	for _, f := range h.st.flows {
		lines += len(f.lines)
		widest = max(widest, f.width)
	}
	if lines != 2 {
		t.Errorf("code block laid out %d lines, want its 2 source lines unwrapped", lines)
	}
	if widest <= h.width {
		t.Errorf("widest code line is %d px, want it wider than the %d px block so it scrolls", widest, h.width)
	}
	if dims.Size.X != h.width {
		t.Errorf("block width = %d, want the full %d", dims.Size.X, h.width)
	}
}

// TestLongParagraphsWrapAndLinesNeverExceedTheWidth checks wrapping with
// whitespace kept between styled spans.
func TestLongParagraphsWrapAndLinesNeverExceedTheWidth(t *testing.T) {
	t.Parallel()
	h := newHarness(t, strings.Repeat("alpha **beta** `gamma` delta, ", 30), 300)
	dims := h.frame()
	for _, f := range h.st.flows {
		if len(f.lines) < 3 {
			t.Errorf("paragraph wrapped into %d lines, want several at 300 px", len(f.lines))
		}
		for _, l := range f.lines {
			if l.w > h.width {
				t.Errorf("a line is %d px wide, want at most %d", l.w, h.width)
			}
		}
	}
	if dims.Size.Y == 0 {
		t.Error("layout reported no height")
	}
}

// TestLayoutDrawsEveryBlockKindWithinBounds runs the whole preview document
// through layout at several widths, checking it neither panics nor reports a
// size beyond its width.
func TestLayoutDrawsEveryBlockKindWithinBounds(t *testing.T) {
	t.Parallel()
	preview, err := os.ReadFile(filepath.Join("..", "markdown", "testdata", "preview.md"))
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{"sample": previewSource(), "preview.md": string(preview)} {
		for _, width := range []int{120, 400, 900} {
			h := newHarness(t, src, width)
			dims := h.frame()
			if dims.Size.X > width || dims.Size.Y <= 0 {
				t.Errorf("%s at width %d: size %v", name, width, dims.Size)
			}
		}
	}
}

// TestNonOpenableLinksRenderAsPlainTextWithoutAPopover checks that a blocked
// scheme draws no underline target and registers no popover.
func TestNonOpenableLinksRenderAsPlainTextWithoutAPopover(t *testing.T) {
	t.Parallel()
	h := newHarness(t, linkDoc("javascript:alert(1)"), 400)
	h.frame()
	if len(h.st.links) != 0 {
		t.Errorf("a blocked link created popover state: %d entries", len(h.st.links))
	}
	lk := h.link()
	if lk.Openable {
		t.Fatal("control: the javascript link is openable")
	}
	for _, f := range h.st.flows {
		for _, l := range f.lines {
			for _, a := range l.atoms {
				if a.arrow {
					t.Error("a blocked link got the openable mark")
				}
			}
		}
	}
}

// TestLinkPopoverShowsOnHoverAndHidesWhenThePointerLeaves covers the hover
// lifecycle.
func TestLinkPopoverShowsOnHoverAndHidesWhenThePointerLeaves(t *testing.T) {
	t.Parallel()
	h := newHarness(t, linkDoc("https://"+exampleHost+"/guide"), 400)
	h.frame()
	lk := h.link()
	if h.shown(lk) {
		t.Fatal("the popover shows before any hover")
	}
	box := h.box(lk)
	h.move(center(box))
	if !h.shown(lk) {
		t.Fatal("the popover does not show while the pointer is over the link")
	}
	h.move(image.Pt(5, 2))
	h.frame()
	if h.shown(lk) {
		t.Error("the popover stays after the pointer left the link")
	}
}

// TestLinkPopoverStaysWhilePointerTravelsToIt moves from the link through the
// gap into the card, then away from both.
func TestLinkPopoverStaysWhilePointerTravelsToIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t, linkDoc("https://"+exampleHost+"/guide"), 400)
	h.frame()
	lk := h.link()
	box := h.box(lk)
	h.move(center(box))
	h.frame()
	h.move(image.Pt(box.Min.X+2, box.Min.Y-3))
	if !h.shown(lk) {
		t.Fatal("the popover hid in the gap between the link and the card")
	}
	h.move(image.Pt(box.Min.X+2, box.Min.Y-12))
	if !h.shown(lk) {
		t.Fatal("the popover hid while the pointer was over the card")
	}
	h.move(image.Pt(h.width-3, 2))
	h.frame()
	if h.shown(lk) {
		t.Error("the popover stays after the pointer left both link and card")
	}
}

// TestLinkPopoverShowsOnKeyboardFocus focuses the link without a pointer.
func TestLinkPopoverShowsOnKeyboardFocus(t *testing.T) {
	t.Parallel()
	h := newHarness(t, linkDoc("https://"+exampleHost+"/guide"), 400)
	h.frame()
	lk := h.link()
	h.router.Source().Execute(key.FocusCmd{Tag: h.st.link(lk)})
	h.frame()
	if !h.shown(lk) {
		t.Error("the popover does not show while the link has keyboard focus")
	}
}

// TestClickPinsThePopoverInsteadOfOpening clicks a link: the popover pins, no
// open request is queued, a second click unpins.
func TestClickPinsThePopoverInsteadOfOpening(t *testing.T) {
	t.Parallel()
	h := newHarness(t, linkDoc("https://"+exampleHost+"/guide"), 400)
	h.frame()
	lk := h.link()
	box := h.box(lk)
	h.move(center(box))
	h.click(center(box))
	if h.st.pinned != lk {
		t.Fatal("clicking the link did not pin its popover")
	}
	if got, ok := h.st.PollOpen(); ok {
		t.Fatalf("clicking the link requested opening %q", got.URL)
	}
	h.move(image.Pt(h.width-3, 2))
	h.frame()
	if !h.shown(lk) {
		t.Error("the pinned popover hid when the pointer left")
	}
	h.move(center(box))
	h.click(center(box))
	if h.st.pinned != nil {
		t.Error("clicking the pinned link again did not unpin it")
	}
}

// TestOpenButtonQueuesOpenLinkAndNeverOpensItself clicks the popover's open
// button: the only effect is one OpenLink for the caller.
func TestOpenButtonQueuesOpenLinkAndNeverOpensItself(t *testing.T) {
	t.Parallel()
	h := newHarness(t, linkDoc("https://"+exampleHost+"/guide"), 400)
	h.frame()
	lk := h.link()
	h.st.pinned = lk
	h.frame()
	ls := h.st.link(lk)
	ls.open.Click()
	h.frame()
	got, ok := h.st.PollOpen()
	if !ok || got.URL != lk.URL {
		t.Fatalf("open request = %+v, %v, want one for %q", got, ok, lk.URL)
	}
	if _, more := h.st.PollOpen(); more {
		t.Error("one click queued more than one open request")
	}
}

// TestCopyButtonWritesTheURLToTheClipboard clicks the popover's copy button.
func TestCopyButtonWritesTheURLToTheClipboard(t *testing.T) {
	t.Parallel()
	h := newHarness(t, linkDoc("https://"+exampleHost+"/guide?a=1"), 400)
	h.frame()
	lk := h.link()
	h.st.pinned = lk
	h.frame()
	h.st.link(lk).copy.Click()
	h.frame()
	h.frame()
	mime, data, ok := h.router.WriteClipboard()
	if !ok || mime != clipboardMIME || string(data) != lk.URL {
		t.Errorf("clipboard = %q %q %v, want %q", mime, data, ok, lk.URL)
	}
	if _, queued := h.st.PollOpen(); queued {
		t.Error("copying queued an open request")
	}
}

// TestMiddleTruncateKeepsTheHostWhole pins the truncation rules.
func TestMiddleTruncateKeepsTheHostWhole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, url, host string
		cols            int
		want            string
	}{
		{"short stays", "https://" + exampleHost + "/a", exampleHost, 40, "https://" + exampleHost + "/a"},
		{"tail cut in the middle", "https://" + exampleHost + "/aaaaaaaaaabbbbbbbbbbcccccccccc", exampleHost, 28, "https://" + exampleHost + "/aaa…cccc"},
		{"host longer than the budget stays whole", "https://averyveryverylonghostname.example/path/to/x", "averyveryverylonghostname.example", 10, "https://averyveryverylonghostname.example…"},
		{"punycode host stays whole", "https://xn--bcher-kva.example/" + strings.Repeat("p", 60), "xn--bcher-kva.example", 40, "https://xn--bcher-kva.example/pppp…ppppp"},
		{"mailto cut as a whole", "mailto:" + strings.Repeat("a", 30) + "@example.org", "", 16, "mailto:a…ple.org"},
	}
	for _, c := range cases {
		got := middleTruncate(c.url, c.host, c.cols)
		if !strings.Contains(got, c.host) {
			t.Errorf("%s: %q lost the host %q", c.name, got, c.host)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestPopoverShowsThePunycodeFormOnlyWhenItDiffers checks the host lines.
func TestPopoverShowsThePunycodeFormOnlyWhenItDiffers(t *testing.T) {
	t.Parallel()
	idn := markdown.Parse("[x](https://bücher.example/p)").Blocks[0].Spans[0].Link
	ascii := markdown.Parse("[x](https://example.com/p)").Blocks[0].Spans[0].Link
	mail := markdown.Parse("[x](mailto:a@example.org)").Blocks[0].Spans[0].Link
	if host, puny := hostLines(idn); host != "bücher.example" || puny != "xn--bcher-kva.example" {
		t.Errorf("idn host lines = %q, %q", host, puny)
	}
	if host, puny := hostLines(ascii); host != exampleHost || puny != "" {
		t.Errorf("ascii host lines = %q, %q", host, puny)
	}
	if host, puny := hostLines(mail); host != "Email address" || puny != "" {
		t.Errorf("mailto host lines = %q, %q", host, puny)
	}
}

// sampleDoc builds a document of about size bytes: prose with bold, inline
// code and links, Go and TypeScript blocks, lists and a table.
func sampleDoc(size int) string {
	const section = "## Section\n\nSome **bold** text with `inline code`, a [link](https://example.com/docs/a?b=c) and more words to wrap across several lines of the view so the paragraph needs real wrapping work.\n\n```go\nfunc add(a, b int) int {\n\t// sum the operands\n\treturn a + b\n}\n```\n\n```ts\nconst total: number = items.reduce((acc, item) => acc + item.price, 0);\n```\n\n- first item\n- second item with `code`\n  - nested item\n\n| Name | Value |\n| --- | ---: |\n| a | 1 |\n| b | 2 |\n\n"
	return strings.Repeat(section, size/len(section)+1)
}

// previewSource is the document every block kind appears in.
func previewSource() string {
	return sampleDoc(2000) + "> quoted text\n\n---\n\n1. one\n2. two\n\n- [x] done\n- [ ] todo\n\n<div>raw</div>\n\n![alt text](https://example.com/i.png)\n\n~~gone~~\n"
}

// TestLayoutP95StaysWithinTheFrameBudget measures a steady-state frame of a
// 20 KB document with highlighted code against the 8 ms budget of DA-03. The
// measurement runs before t.Parallel so no other test competes for the
// machine while it does (as in the redaction cost test of PR 93); it reads
// the benchmark timer, not a clock.
func TestLayoutP95StaysWithinTheFrameBudget(t *testing.T) {
	src := sampleDoc(20 * 1024)
	h := newHarness(t, src, 800)
	if !slices.ContainsFunc(h.doc.Blocks, func(b markdown.Block) bool { return b.Highlighted }) {
		t.Fatal("the sample has no highlighted code")
	}
	first := h.frame()
	var samples []time.Duration
	testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for b.Loop() {
			h.ops.Reset()
			start := b.Elapsed()
			Layout(h.context(), &h.th, h.doc, &h.st)
			samples = append(samples, b.Elapsed()-start)
		}
	})
	slices.Sort(samples)
	p95 := samples[len(samples)*95/100]
	t.Parallel()
	t.Logf("20 KB document (%d bytes, %d px tall): p95 %v over %d frames", len(src), first.Size.Y, p95, len(samples))
	if p95 > 8*time.Millisecond {
		t.Errorf("layout p95 = %v, want at most 8 ms", p95)
	}
}
