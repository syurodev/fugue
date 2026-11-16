package kit

import (
	"go/parser"
	"go/token"
	"image"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// Evidence for Kbd (ui-kit Kbd, owner request 2026-11-16; UK-E04, UK-E05).

// TestKbdRendersMacSymbolsAndLinuxWords pins both notations: symbols in the
// platform order ⌃ ⌥ ⇧ ⌘ with no separator on macOS, words in the order Ctrl,
// Alt, Shift, Super joined by "+" elsewhere; Mod is Cmd on macOS and Ctrl
// elsewhere, and every special key has its symbol and its word.
func TestKbdRendersMacSymbolsAndLinuxWords(t *testing.T) {
	t.Parallel()
	all := ModPrimary | ModCtrl | ModAlt | ModShift | ModSuper
	cases := []struct {
		s       Shortcut
		mac, pc string
	}{
		{Shortcut{Key: Char('k')}, "K", "K"},
		{Shortcut{Key: Char('7')}, "7", "7"},
		{Shortcut{Key: Char(',')}, ",", ","},
		{Shortcut{Mods: ModPrimary, Key: Char('k')}, "⌘K", "Ctrl+K"},
		{Shortcut{Mods: ModPrimary | ModShift, Key: Char('p')}, "⇧⌘P", "Ctrl+Shift+P"},
		{Shortcut{Mods: ModCtrl | ModAlt, Key: Char('t')}, "⌃⌥T", "Ctrl+Alt+T"},
		{Shortcut{Mods: ModSuper, Key: Char('e')}, "⌘E", "Super+E"},
		{Shortcut{Mods: all, Key: Char('k')}, "⌃⌥⇧⌘K", "Ctrl+Alt+Shift+Super+K"},
		{Shortcut{Mods: ModPrimary, Key: KeyBackspace}, "⌘⌫", "Ctrl+Backspace"},
		{Shortcut{Key: KeyEnter}, "↩", "Enter"},
		{Shortcut{Key: KeyEscape}, "⎋", "Esc"},
		{Shortcut{Key: KeyTab}, "⇥", "Tab"},
		{Shortcut{Key: KeySpace}, "␣", "Space"},
		{Shortcut{Key: KeyBackspace}, "⌫", "Backspace"},
		{Shortcut{Key: KeyDelete}, "⌦", string(KeyDelete)},
		{Shortcut{Key: KeyLeft}, "←", "Left"},
		{Shortcut{Key: KeyUp}, "↑", "Up"},
		{Shortcut{Key: KeyRight}, "→", "Right"},
		{Shortcut{Key: KeyDown}, "↓", "Down"},
		{Shortcut{Key: KeyHome}, "↖", "Home"},
		{Shortcut{Key: KeyEnd}, "↘", "End"},
		{Shortcut{Key: KeyPageUp}, "⇞", "PgUp"},
		{Shortcut{Key: KeyPageDown}, "⇟", "PgDn"},
		{Shortcut{Key: KeyF(1)}, "F1", "F1"},
		{Shortcut{Key: KeyF(12)}, "F12", "F12"},
	}
	for _, tc := range cases {
		for _, p := range []struct {
			pf   Platform
			want string
		}{{PlatformMac, tc.mac}, {PlatformPC, tc.pc}} {
			got, err := tc.s.text(p.pf)
			if err != nil || got != p.want {
				t.Errorf("%+v on platform %d = %q (%v), want %q", tc.s, p.pf, got, err, p.want)
			}
		}
	}
	for _, bad := range []Key{"Foo", "F13", "F0", "ab", "F01", " "} {
		if _, err := (Shortcut{Key: bad}).text(PlatformPC); err == nil {
			t.Errorf("key %q was accepted", bad)
		}
	}
	h := newHarness(t)
	h.frame(t)
	size, err := KbdSize(h.env, &h.gtx, Shortcut{Mods: ModPrimary, Key: Char('k')})
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if want := labelLine(h.env, &h.gtx, theme.TypeSmall()) + 2*h.gtx.Dp(theme.SpaceXXS); size.Y != want {
		t.Errorf("chip height %d, want the small line plus 2 dp padding twice = %d", size.Y, want)
	}
}

// TestKbdNeverGuessesThePlatform: the notation follows Env.Platform alone —
// the same shortcut changes with it and with nothing else — and the kit
// package never imports the runtime to ask the OS (§7).
func TestKbdNeverGuessesThePlatform(t *testing.T) {
	t.Parallel()
	s := Shortcut{Mods: ModPrimary, Key: Char('o')}
	h := newHarness(t)
	h.frame(t)
	widths := map[Platform]int{}
	for _, p := range []Platform{PlatformMac, PlatformPC} {
		h.env.Platform = p
		size, err := KbdSize(h.env, &h.gtx, s)
		if err != nil {
			t.Fatalf("measure: %v", err)
		}
		widths[p] = size.X
	}
	if widths[PlatformMac] >= widths[PlatformPC] {
		t.Errorf("widths %v: the symbols should be narrower than the words, so Env.Platform must pick the notation", widths)
	}
	assertNoRuntimeImport(t)
}

// assertNoRuntimeImport fails for every non-test file of the package that
// imports runtime.
func assertNoRuntimeImport(t *testing.T) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list the package: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, imp := range src.Imports {
			if imp.Path.Value == `"runtime"` {
				t.Errorf("%s imports runtime: the window sets Env.Platform, the kit never asks the OS", f)
			}
		}
	}
}

// TestMenuShowsShortcutsAsKbdInAnAlignedColumn: items with chips of different
// widths right-align them in one column of the widest chip's width, so the
// shortcut text of every item ends at the same x.
func TestMenuShowsShortcutsAsKbdInAnAlignedColumn(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.env.Platform = PlatformMac
	items := []MenuItem{
		{ID: "a", Label: "Find", Shortcut: Shortcut{Mods: ModPrimary, Key: Char('o')}},
		{ID: "b", Label: "Delete forever", Shortcut: Shortcut{Mods: ModPrimary | ModShift, Key: KeyBackspace}},
		{ID: "c", Label: "Pin", Sub: []MenuItem{{ID: "d", Label: "Mail"}}},
		{ID: "e", Label: "Cut", Disabled: true, Shortcut: Shortcut{Key: KeyEnter}},
	}
	var runs []image.Rectangle
	h.env.RunRecorder = func(r image.Rectangle) { runs = append(runs, r) }
	m := NewMenuState()
	m.Open()
	p := MenuProps{Items: items, Inline: true}
	h.menuFrame(t, m, p)
	runs = runs[:0]
	h.menuFrame(t, m, p)

	lv := &m.lv[0]
	widest := 0
	for _, s := range []Shortcut{items[0].Shortcut, items[1].Shortcut, items[3].Shortcut} {
		size, err := KbdSize(h.env, &h.gtx, s)
		if err != nil {
			t.Fatalf("measure: %v", err)
		}
		widest = max(widest, size.X)
	}
	if lv.cols.kbd != widest {
		t.Fatalf("the shortcut column is %d px, want the widest chip, %d", lv.cols.kbd, widest)
	}
	inner := lv.rect.Inset(lv.pad)
	// The chips of the three items without an arrow end at the row's right
	// padding; a chip's text ends one padding inside its chip.
	rowRight := inner.Max.X - h.gtx.Dp(theme.SpaceMD) - h.gtx.Dp(theme.SpaceSM)
	ends := 0
	for _, r := range runs {
		if r.Max.X == rowRight {
			ends++
		}
	}
	if ends != 3 {
		t.Errorf("%d shortcut texts end at the common column edge %d, want 3 (runs %v)", ends, rowRight, runs)
	}
	starts := map[int]bool{}
	for _, r := range runs {
		if r.Max.X == rowRight {
			starts[r.Min.X] = true
		}
	}
	if len(starts) < 2 {
		t.Errorf("every chip text starts at the same x: the chips are not right-aligned to their own widths: %v", starts)
	}
}

// TestKbdTextContrastOnEveryChromeSurface covers UK-E04 for Kbd: the chip's
// text keeps 4.5:1 on FillSoft over every chrome surface and over the menu
// highlight pill, in both modes, with the linear blending Gio does.
func TestKbdTextContrastOnEveryChromeSurface(t *testing.T) {
	t.Parallel()
	for _, mode := range themeModes() {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			env := newModeEnv(t, mode.th)
			c := env.Th.Chrome()
			grounds := contrastSurfaces(env.Th)
			grounds = append(grounds, contrastSurface{"menu highlight pill", over(c.SurfaceRaised, c.HoverWash)})
			for _, g := range grounds {
				if got := contrastRatio(c.FgMuted, over(g.c, c.FillSoft)); got < 4.5 {
					t.Errorf("Kbd text on %s: contrast %.2f below 4.5", g.name, got)
				}
			}
		})
	}
}
