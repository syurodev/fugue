package markdown_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/ui/internal/markdown"
)

// TestMain registers the golden rewrite flag here, not as a package
// variable, so the package owns no mutable globals; the golden test reads
// it back through flag.Lookup. It also checks the package for leaked
// goroutines: chroma's regex engine keeps a self-stopping timeout clock
// goroutine (regexp2 runClock) that the lexer starts on first use and
// stops on its own once its deadline window passes; it belongs to the
// dependency, not this package, so it is ignored.
func TestMain(m *testing.M) {
	flag.Bool("update", false, "rewrite the golden dumps")
	goleak.VerifyTestMain(m, goleak.IgnoreAnyFunction("github.com/dlclark/regexp2/v2.runClock"))
}

// goldensRequested reports whether -update was passed.
func goldensRequested() bool {
	f := flag.Lookup("update")
	return f != nil && f.Value.String() == "true"
}

// spanFlags names a span's style for dumps.
func spanFlags(s markdown.Span) string {
	var parts []string
	if s.Style&markdown.StyleBold != 0 {
		parts = append(parts, "bold")
	}
	if s.Style&markdown.StyleItalic != 0 {
		parts = append(parts, "italic")
	}
	if s.Style&markdown.StyleStrike != 0 {
		parts = append(parts, "strike")
	}
	if s.Style&markdown.StyleCode != 0 {
		parts = append(parts, "code")
	}
	if len(parts) == 0 {
		parts = append(parts, "plain")
	}
	return strings.Join(parts, "+")
}

// dumpSpan renders one span deterministically.
func dumpSpan(sb *strings.Builder, indent, label string, s markdown.Span) {
	line := fmt.Sprintf("%s%s %s %q", indent, label, spanFlags(s), s.Text)
	if s.Role != "" && s.Role != markdown.RolePlain {
		line += fmt.Sprintf(" role=%s", s.Role)
	}
	if s.Link != nil {
		line += fmt.Sprintf(" link url=%q host=%q puny=%q openable=%v",
			s.Link.URL, s.Link.Host, s.Link.HostPunycode, s.Link.Openable)
	}
	if s.Image != nil {
		line += fmt.Sprintf(" image url=%q host=%q", s.Image.URL, s.Image.Host)
	}
	if s.HardBreak {
		line += " hard-break"
	}
	sb.WriteString(line + "\n")
}

// dumpSpans renders a span slice.
func dumpSpans(sb *strings.Builder, indent string, spans []markdown.Span) {
	for _, s := range spans {
		dumpSpan(sb, indent, "span", s)
	}
}

// alignNone is the dump name of the default alignment, shared with
// convertAlign tests to satisfy goconst.
const alignNone = "none"

// dumpAlign names one column alignment.
func dumpAlign(a markdown.Align) string {
	switch a {
	case markdown.AlignLeft:
		return "left"
	case markdown.AlignCenter:
		return "center"
	case markdown.AlignRight:
		return "right"
	case markdown.AlignNone:
		return alignNone
	default:
		return alignNone
	}
}

// dumpBlocks renders blocks recursively.
func dumpBlocks(sb *strings.Builder, indent string, blocks []markdown.Block) {
	for i := range blocks {
		b := &blocks[i]
		switch b.Kind {
		case markdown.BlockParagraph:
			sb.WriteString(indent + "para\n")
			dumpSpans(sb, indent+"  ", b.Spans)
		case markdown.BlockHeading:
			fmt.Fprintf(sb, "%sheading %d\n", indent, b.Level)
			dumpSpans(sb, indent+"  ", b.Spans)
		case markdown.BlockThematicBreak:
			sb.WriteString(indent + "thematic-break\n")
		case markdown.BlockCode:
			dumpCode(sb, indent, b)
		case markdown.BlockHTML:
			sb.WriteString(indent + "html\n")
			dumpSpans(sb, indent+"  ", b.Spans)
		case markdown.BlockQuote:
			sb.WriteString(indent + "quote\n")
			dumpBlocks(sb, indent+"  ", b.Blocks)
		case markdown.BlockList:
			dumpList(sb, indent, b.List)
		case markdown.BlockTable:
			dumpTable(sb, indent, b.Table)
		}
	}
}

// dumpCode renders a code block's lines and tokens.
func dumpCode(sb *strings.Builder, indent string, b *markdown.Block) {
	fmt.Fprintf(sb, "%scode lang=%q highlighted=%v\n", indent, b.Language, b.Highlighted)
	for _, line := range b.Lines {
		sb.WriteString(indent + "  line\n")
		for _, s := range line {
			role := s.Role
			if role == "" {
				role = markdown.RolePlain
			}
			fmt.Fprintf(sb, "%s    token %s %q\n", indent+"  ", role, s.Text)
		}
	}
}

// dumpList renders a list and its items.
func dumpList(sb *strings.Builder, indent string, l markdown.List) {
	fmt.Fprintf(sb, "%slist ordered=%v start=%d\n", indent, l.Ordered, l.Start)
	for _, item := range l.Items {
		state := "none"
		if item.Task {
			state = "unchecked"
			if item.Checked {
				state = "checked"
			}
		}
		fmt.Fprintf(sb, "%s  item task=%s\n", indent, state)
		dumpBlocks(sb, indent+"    ", item.Blocks)
	}
}

// dumpTable renders a table's alignments, header and rows.
func dumpTable(sb *strings.Builder, indent string, t markdown.Table) {
	aligns := make([]string, len(t.Aligns))
	for i, a := range t.Aligns {
		aligns[i] = dumpAlign(a)
	}
	fmt.Fprintf(sb, "%stable aligns=%s\n", indent, strings.Join(aligns, " "))
	sb.WriteString(indent + "  header\n")
	dumpRow(sb, indent+"    ", t.Header)
	for _, row := range t.Rows {
		sb.WriteString(indent + "  row\n")
		dumpRow(sb, indent+"    ", row)
	}
}

// dumpRow renders one table row.
func dumpRow(sb *strings.Builder, indent string, row []markdown.Cell) {
	for _, cell := range row {
		sb.WriteString(indent + "cell\n")
		dumpSpans(sb, indent+"  ", cell.Spans)
	}
}

// dumpDoc renders a whole document; this text is the golden format.
func dumpDoc(d *markdown.Doc) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "doc truncated=%v\n", d.Truncated)
	dumpBlocks(&sb, "", d.Blocks)
	return sb.String()
}

// goldenDir is the package's testdata directory.
func goldenDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// checkGolden parses one .md case and compares (or rewrites) its dump.
func checkGolden(t *testing.T, name string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Clean(filepath.Join(goldenDir(t), name+".md")))
	if err != nil {
		t.Fatal(err)
	}
	got := dumpDoc(markdown.Parse(string(src)))
	golden := filepath.Join(goldenDir(t), name+".golden")
	if goldensRequested() {
		if writeErr := os.WriteFile(filepath.Clean(golden), []byte(got), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		return
	}
	want, err := os.ReadFile(filepath.Clean(golden))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s.md\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}

// TestModelDumpsMatchGoldenFiles pins the DA-E03 model behaviour as text
// dumps, one file per case in testdata, regenerated with -update.
func TestModelDumpsMatchGoldenFiles(t *testing.T) {
	t.Parallel()
	cases := []string{
		"spaces-around-code-and-bold",
		"nested-lists",
		"task-list",
		"table-alignment",
		"tabs-in-code",
		"highlight-go",
		"highlight-typescript",
		"unknown-language",
		"links",
		"raw-html",
		"images",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			checkGolden(t, name)
		})
	}
}
