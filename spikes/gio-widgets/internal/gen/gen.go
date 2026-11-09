// Package gen builds deterministic transcript-like content for the spikes.
package gen

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/flow"
)

var words = strings.Fields(`the host streams deltas to every client and the transcript keeps one revision per item
phiên làm việc giữ trạng thái; người dùng duyệt quyền trước khi công cụ chạy; đội agent chia việc theo vai trò
worktree branch merge review approval vendor session quota account reset schedule layout cache virtualized
gõ tiếng Việt bằng Telex trên Wayland phải đúng dấu và không mất chữ khi composer đang soạn thảo`)

var codeLines = []string{
	"func (m *Manager) Run(ctx context.Context) error {",
	"    for {",
	"        select {",
	"        case u := <-m.updates:",
	"            m.apply(u) // update the read model",
	"        case <-ctx.Done():",
	"            return ctx.Err()",
	"        }",
	"    }",
	"}",
	"type Item struct { ID string; Rev int; Kind string }",
	"var ErrStale = errors.New(\"revision is stale\")",
}

// Inline parses **bold** and `code` spans.
func Inline(s string) []flow.Run {
	var out []flow.Run
	st := flow.Normal
	for len(s) > 0 {
		i := strings.IndexAny(s, "*`")
		if i < 0 {
			out = append(out, flow.Run{Text: s, Style: st})
			break
		}
		if i > 0 {
			out = append(out, flow.Run{Text: s[:i], Style: st})
		}
		switch {
		case strings.HasPrefix(s[i:], "**"):
			st ^= flow.Bold
			s = s[i+2:]
		case s[i] == '`':
			st ^= flow.Code
			s = s[i+1:]
		default:
			out = append(out, flow.Run{Text: s[i : i+1], Style: st})
			s = s[i+1:]
		}
		if st != flow.Normal && st != flow.Bold && st != flow.Code {
			st = flow.Normal
		}
	}
	return out
}

func sentence(r *rand.Rand, n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(' ')
		}
		w := words[r.Intn(len(words))]
		switch r.Intn(14) {
		case 0:
			w = "**" + w + "**"
		case 1:
			w = "`" + w + "`"
		}
		sb.WriteString(w)
	}
	return sb.String() + "."
}

// Message returns the blocks of an assistant message: paragraphs, a list and
// a code block, in a mix that depends on the seed and size.
func Message(r *rand.Rand, paras int) []flow.Block {
	var bl []flow.Block
	for p := 0; p < paras; p++ {
		switch r.Intn(4) {
		case 0:
			for i := 0; i < 2+r.Intn(3); i++ {
				bl = append(bl, flow.Block{Prefix: "- ", Indent: 1, Runs: Inline(sentence(r, 6+r.Intn(14)))})
			}
		case 1:
			var runs []flow.Run
			for i := 0; i < 4+r.Intn(10); i++ {
				runs = append(runs, flow.Run{Text: codeLines[r.Intn(len(codeLines))] + "\n", Style: flow.Mono})
			}
			runs[len(runs)-1].Text = strings.TrimSuffix(runs[len(runs)-1].Text, "\n")
			bl = append(bl, flow.Block{Runs: runs, Mono: true, Bg: flow.BgCode, Gap: 4})
		default:
			bl = append(bl, flow.Block{Runs: Inline(sentence(r, 14+r.Intn(50)))})
		}
	}
	return bl
}

// Entry is one transcript item for the list spikes.
type Entry struct {
	Kind string // user, assistant, tool_call, reasoning
	Head string
	Body []flow.Block
}

// Entries makes n entries; every 5th is a tool_call, every 7th reasoning.
func Entries(n int, seed int64) []Entry {
	r := rand.New(rand.NewSource(seed))
	out := make([]Entry, n)
	for i := range out {
		switch {
		case i%2 == 0 && i%10 != 0:
			out[i] = Entry{Kind: "user", Head: fmt.Sprintf("**You** #%d", i), Body: []flow.Block{{Runs: Inline(sentence(r, 4+r.Intn(30)))}}}
		case i%5 == 1:
			out[i] = Entry{Kind: "tool_call", Head: fmt.Sprintf("tool `Bash` go test ./... #%d", i), Body: []flow.Block{{Mono: true, Bg: flow.BgCode, Runs: []flow.Run{{Style: flow.Mono, Text: strings.Join(codeLines[:3+r.Intn(8)], "\n")}}}}}
		case i%7 == 3:
			out[i] = Entry{Kind: "reasoning", Head: fmt.Sprintf("reasoning #%d", i), Body: []flow.Block{{Runs: []flow.Run{{Style: flow.Dim, Text: sentence(r, 20+r.Intn(60))}}}}}
		default:
			out[i] = Entry{Kind: "assistant", Head: fmt.Sprintf("**Fugue** #%d", i), Body: Message(r, 1+r.Intn(4))}
		}
	}
	return out
}
