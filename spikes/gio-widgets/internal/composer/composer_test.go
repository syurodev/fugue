package composer

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/fonts"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/view"
)

type rig struct {
	t         *testing.T
	c         *Composer
	r         input.Router
	ops       op.Ops
	lastFrame time.Duration
}

func newRig(t *testing.T) *rig {
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	g := &rig{t: t, c: New(view.NewTheme(sh, view.Dark()))}
	g.frame()
	g.r.Source().Execute(key.FocusCmd{Tag: &g.c.Ed})
	g.frame()
	if !g.r.Source().Focused(&g.c.Ed) {
		t.Fatal("editor did not take focus")
	}
	return g
}

func (g *rig) frame() time.Duration {
	d := g.frame1()
	g.lastFrame = d
	return d
}

func (g *rig) frame1() time.Duration {
	g.ops.Reset()
	gtx := layout.Context{Ops: &g.ops, Constraints: layout.Constraints{Max: image.Pt(800, 600)}, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Source: g.r.Source()}
	t := time.Now()
	g.c.Layout(gtx, 8)
	d := time.Since(t)
	g.r.Frame(gtx.Ops)
	return d
}

func (g *rig) typ(s string) {
	for _, r := range s {
		g.edit(string(r))
	}
}

// edit is what the platform text input sends for a typed rune: a replacement
// of the current selection.
func (g *rig) edit(s string) {
	a, b := g.c.Ed.Selection()
	g.r.Queue(key.EditEvent{Range: key.Range{Start: a, End: b}, Text: s})
	// app.Window mirrors the editor state and answers every edit with the
	// new selection; a bare router has no window, so do it here.
	n := min(a, b) + len([]rune(s))
	g.r.Queue(key.SelectionEvent{Start: n, End: n})
	g.frame()
}

func (g *rig) press(name key.Name, mod key.Modifiers) {
	g.r.Queue(key.Event{Name: name, Modifiers: mod, State: key.Press})
	g.frame()
}

func TestEnterSubmitsShiftEnterBreaksLine(t *testing.T) {
	g := newRig(t)
	g.typ("a")
	g.press(key.NameReturn, key.ModShift)
	g.typ("b")
	if g.c.Ed.Text() != "a\nb" || len(g.c.Sent) != 0 {
		t.Fatalf("shift+enter: text %q sent %v", g.c.Ed.Text(), g.c.Sent)
	}
	g.press(key.NameReturn, 0)
	if len(g.c.Sent) != 1 || g.c.Sent[0] != "a\nb" || g.c.Ed.Text() != "" {
		t.Fatalf("enter: sent %q, left %q", g.c.Sent, g.c.Ed.Text())
	}
}

func TestUndoRedoGranularity(t *testing.T) {
	g := newRig(t)
	g.typ("hello world")
	g.press("Z", key.ModShortcut)
	after1 := g.c.Ed.Text()
	g.press("Z", key.ModShortcut)
	after2 := g.c.Ed.Text()
	t.Logf("after 11 typed runes: undo once -> %q, twice -> %q", after1, after2)
	if after1 == "hello world" {
		t.Fatal("undo did nothing")
	}
	g.press("Z", key.ModShortcut|key.ModShift)
	if g.c.Ed.Text() != after1 {
		t.Fatalf("redo gave %q, want %q", g.c.Ed.Text(), after1)
	}
}

func TestWordMovement(t *testing.T) {
	g := newRig(t)
	g.typ("foo bar baz")
	var got []int
	for i := 0; i < 3; i++ {
		g.press(key.NameLeftArrow, key.ModShortcut)
		_, c := g.c.Ed.Selection()
		got = append(got, c)
	}
	g.press(key.NameRightArrow, key.ModShortcut)
	_, c := g.c.Ed.Selection()
	got = append(got, c)
	t.Logf("caret after ctrl+left x3, ctrl+right: %v", got)
	if got[0] != 8 || got[1] != 4 || got[2] != 0 || got[3] != 3 && got[3] != 4 {
		t.Fatalf("word movement went to %v", got)
	}
}

func TestSlashMenu(t *testing.T) {
	g := newRig(t)
	g.typ("hi /")
	if len(g.c.Menu) != 0 {
		t.Fatal("the menu opens only for a slash at the start")
	}
	g.c.Ed.SetText("")
	g.typ("/")
	if len(g.c.Menu) < len(Commands) {
		t.Fatalf("typing / should list all commands, got %d", len(g.c.Menu))
	}
	g.typ("fu")
	if len(g.c.Menu) != 4 {
		t.Fatalf("/fu matches the four fugue: commands, got %d", len(g.c.Menu))
	}
	g.press(key.NameDownArrow, 0)
	g.press(key.NameReturn, 0) // accepts, must not submit
	if g.c.Ed.Text() != "/fugue:status " || len(g.c.Sent) != 0 || len(g.c.Menu) != 0 {
		t.Fatalf("text %q sent %v menu %d", g.c.Ed.Text(), g.c.Sent, len(g.c.Menu))
	}
	g.press(key.NameReturn, 0) // menu closed: Enter submits again
	if len(g.c.Sent) != 1 {
		t.Fatal("enter after the menu closed must submit")
	}
}

func TestPaste100KB(t *testing.T) {
	for name, line := range map[string]string{
		"2000 lines of 50 bytes": strings.Repeat("0123456789 0123456789 0123456789 0123456789", 1) + "\n",
		"one 100 KB paragraph":   strings.Repeat("word ", 20000),
	} {
		g := newRig(t)
		big := strings.Repeat(line, 100_000/len(line))
		t0 := time.Now()
		g.c.Ed.Insert(big) // what the paste path calls
		ins := time.Since(t0)
		first := g.frame()
		var worst time.Duration
		for i := 0; i < 20; i++ { // then typing into the big text
			g.edit("x")
			worst = max(worst, g.lastFrame)
		}
		t.Logf("%s: insert %v, first layout %v, worst of 20 keystrokes after %v", name, ins, first, worst)
		if first > 500*time.Millisecond {
			t.Errorf("%s: first layout %v hangs the UI", name, first)
		}
	}
}
