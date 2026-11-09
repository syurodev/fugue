// Package diff builds a syntax-highlighted unified diff as flow items, one
// item per diff line, so the transcript's selection and list engine
// handle scrolling, selection and copy unchanged.
package diff

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/flow"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/gen"
)

// Line is one line of the unified diff.
type Line struct {
	Kind     byte // ' ', '-', '+', '@'
	Text     string
	Old, New int
}

// Stats says what building cost.
type Stats struct{ Lex, Build time.Duration }

// Source returns real Go source to diff: the standard library's net/http
// server.go, transport.go, request.go and client.go, which have plenty of comments, strings and
// declarations for the lexer.
func Source() []string {
	var all []string
	for _, f := range []string{"server.go", "transport.go", "request.go", "client.go"} {
		b, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "src", "net", "http", f))
		if err != nil {
			b = []byte(strings.Repeat("func f(x int) string { return \"v\" } // note\n", 4000))
		}
		all = append(all, strings.Split(string(b), "\n")...)
	}
	return all
}

// Make edits a copy of the source (deleting, changing and inserting lines)
// and returns the first n lines of the unified diff, with 3 lines of context.
func Make(a []string, n int, seed int64) (lines []Line, b []string) {
	r := rand.New(rand.NewSource(seed))
	type op struct {
		kind     byte
		text     string
		old, new int
	}
	var ops []op
	o, nw := 1, 1
	for _, l := range a {
		switch x := r.Intn(100); {
		case x < 3:
			ops = append(ops, op{'-', l, o, 0})
			o++
		case x < 7:
			c := l + " // changed"
			if strings.TrimSpace(l) == "" {
				c = "// new comment line"
			}
			ops = append(ops, op{'-', l, o, 0}, op{'+', c, 0, nw})
			o++
			nw++
			b = append(b, c)
		case x < 9:
			ins := fmt.Sprintf("var added%d = \"inserted line %d\" // 0x%x", nw, nw, nw)
			ops = append(ops, op{' ', l, o, nw}, op{'+', ins, 0, nw + 1})
			o++
			nw += 2
			b = append(b, l, ins)
		default:
			ops = append(ops, op{' ', l, o, nw})
			o++
			nw++
			b = append(b, l)
		}
	}
	changed := make([]bool, len(ops))
	for i, p := range ops {
		changed[i] = p.kind != ' '
	}
	near := func(i int) bool {
		for d := -3; d <= 3; d++ {
			if j := i + d; j >= 0 && j < len(ops) && changed[j] {
				return true
			}
		}
		return false
	}
	inHunk := false
	for i, p := range ops {
		if len(lines) >= n {
			break
		}
		if !near(i) {
			inHunk = false
			continue
		}
		if !inHunk {
			inHunk = true
			lines = append(lines, Line{Kind: '@', Text: fmt.Sprintf("@@ -%d +%d @@", max(p.old, 1), max(p.new, 1))})
		}
		lines = append(lines, Line{Kind: p.kind, Text: p.text, Old: p.old, New: p.new})
	}
	return lines, b
}

func tokClass(t chroma.TokenType) uint8 {
	switch {
	case t.InCategory(chroma.Keyword):
		return 1
	case t.InCategory(chroma.LiteralString):
		return 2
	case t.InCategory(chroma.Comment):
		return 3
	case t.InCategory(chroma.LiteralNumber):
		return 4
	case t == chroma.NameFunction:
		return 5
	case t == chroma.NameBuiltin || t == chroma.KeywordType:
		return 6
	case t == chroma.NameClass:
		return 7
	}
	return 0
}

// Highlight tokenises the whole source and returns the runs of every line.
func Highlight(src []string) ([][]flow.Run, error) {
	lx := chroma.Coalesce(lexers.Get("go"))
	it, err := lx.Tokenise(nil, strings.Join(src, "\n"))
	if err != nil {
		return nil, err
	}
	out := make([][]flow.Run, 1, len(src))
	for t := it(); t != chroma.EOF; t = it() {
		parts := strings.Split(t.Value, "\n")
		for i, p := range parts {
			if i > 0 {
				out = append(out, nil)
			}
			if p != "" {
				out[len(out)-1] = append(out[len(out)-1], flow.Run{Text: p, Style: flow.Mono, Tok: tokClass(t.Type)})
			}
		}
	}
	return out, nil
}

// Entries turns the diff into one flow item per line.
func Entries(lines []Line, a, b []string) ([]gen.Entry, Stats, error) {
	t0 := time.Now()
	ra, err := Highlight(a)
	if err != nil {
		return nil, Stats{}, err
	}
	rb, err := Highlight(b)
	if err != nil {
		return nil, Stats{}, err
	}
	st := Stats{Lex: time.Since(t0)}
	t0 = time.Now()
	out := make([]gen.Entry, 0, len(lines))
	for _, l := range lines {
		bl := flow.Block{Mono: true, Prefix: string(l.Kind)}
		switch l.Kind {
		case '@':
			bl.Prefix = ""
			bl.Runs = []flow.Run{{Text: l.Text, Style: flow.Dim}}
		case '-':
			bl.Bg, bl.Gutter = flow.BgDel, fmt.Sprintf("%5d      ", l.Old)
			bl.Runs = pick(ra, l.Old-1, l.Text)
		case '+':
			bl.Bg, bl.Gutter = flow.BgAdd, fmt.Sprintf("     %5d ", l.New)
			bl.Runs = pick(rb, l.New-1, l.Text)
		default:
			bl.Gutter = fmt.Sprintf("%5d %5d ", l.Old, l.New)
			bl.Runs = pick(ra, l.Old-1, l.Text)
		}
		out = append(out, gen.Entry{Kind: "raw", Body: []flow.Block{bl}})
	}
	st.Build = time.Since(t0)
	return out, st, nil
}

func pick(runs [][]flow.Run, i int, text string) []flow.Run {
	if i >= 0 && i < len(runs) && len(runs[i]) > 0 {
		return runs[i]
	}
	return []flow.Run{{Text: text, Style: flow.Mono}}
}
