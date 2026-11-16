package main

import (
	"fmt"
	"image"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Kbd page (UK-09, ui-kit Kbd, owner request 2026-11-16): the chip in
// both notations side by side — macOS symbols, then Linux and Windows words —
// for single keys, modifier combinations, every special key and the function
// keys. The page draws each chip from its measured size and records its box
// as a text run, so the bounds check holds it like any other run; a chip is
// not a control, so none is held to the hit minimum. The window's own
// platform is restored after each block: a page never leaves a notation
// behind.

// kbdWrap is how wide a group of chips grows before it starts a new line.
const kbdWrap = unit.Dp(300)

// kbdPlatforms are the notations the page shows.
func kbdPlatforms() []struct {
	name string
	p    kit.Platform
} {
	return []struct {
		name string
		p    kit.Platform
	}{{"macOS", kit.PlatformMac}, {"Linux and Windows", kit.PlatformPC}}
}

// kbdGroup is a titled set of shortcuts.
type kbdGroup struct {
	name string
	keys []kit.Shortcut
}

// kbdGroups are the shortcut sets, every key of the component's contract.
func kbdGroups() []kbdGroup {
	pm, sh := kit.ModPrimary, kit.ModShift
	funcs := make([]kit.Shortcut, 0, 12)
	for n := 1; n <= 12; n++ {
		funcs = append(funcs, kit.Shortcut{Key: kit.KeyF(n)})
	}
	return []kbdGroup{
		{"single keys", []kit.Shortcut{{Key: kit.Char('k')}, {Key: kit.Char('7')}, {Key: kit.Char(',')}, {Key: kit.Char('/')}}},
		{"modifiers", []kit.Shortcut{
			{Mods: pm, Key: kit.Char('k')},
			{Mods: pm | sh, Key: kit.Char('p')},
			{Mods: kit.ModCtrl | kit.ModAlt, Key: kit.Char('t')},
			{Mods: pm | kit.ModAlt | sh, Key: kit.Char('k')},
			{Mods: kit.ModSuper, Key: kit.Char('e')},
			{Mods: pm, Key: kit.Char(',')},
		}},
		{"special keys", []kit.Shortcut{
			{Key: kit.KeyEnter},
			{Key: kit.KeyEscape},
			{Key: kit.KeyTab},
			{Key: kit.KeySpace},
			{Key: kit.KeyBackspace},
			{Key: kit.KeyDelete},
			{Key: kit.KeyLeft},
			{Key: kit.KeyUp},
			{Key: kit.KeyRight},
			{Key: kit.KeyDown},
			{Key: kit.KeyHome},
			{Key: kit.KeyEnd},
			{Key: kit.KeyPageUp},
			{Key: kit.KeyPageDown},
		}},
		{"function keys", funcs},
	}
}

// kbdCellKey is the inventory key of one chip: the platform, the group and
// the shortcut's own text.
func kbdCellKey(platform, group int, s kit.Shortcut) cell {
	return cell{compKbd, platform, group, fmt.Sprintf("%d/%s", s.Mods, s.Key)}
}

// layoutKbd draws the Kbd page: a block per notation and group.
func (p *pages) layoutKbd(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Kbd: one chip per shortcut, written by the window's platform — symbols in the order ⌃ ⌥ ⇧ ⌘ on macOS, words joined by + on Linux and Windows. It sits in the Menu's shortcut column and has no motion.") + gtx.Dp(theme.SpaceXL)
	blocks := make([]block, 0, 2*len(kbdGroups()))
	for pi, pf := range kbdPlatforms() {
		for gi, g := range kbdGroups() {
			blocks = append(blocks, func(gtx *layout.Context, dry bool) image.Point {
				return p.kbdBlock(gtx, pi, gi, pf.name+" · "+g.name, pf.p, g.keys, dry)
			})
		}
	}
	p.flow(gtx, y, blocks)
}

// kbdBlock lays one group's chips under its title, wrapped at kbdWrap, in
// the notation pf; with dry set it only measures.
func (p *pages) kbdBlock(gtx *layout.Context, pi, gi int, title string, pf kit.Platform, keys []kit.Shortcut, dry bool) image.Point {
	saved := p.env.Platform
	p.env.Platform = pf
	defer func() { p.env.Platform = saved }()

	c := p.env.Th.Chrome()
	gap, wrap := gtx.Dp(theme.SpaceSM), gtx.Dp(kbdWrap)
	if !dry {
		p.caption(gtx, image.Point{}, title, c.FgMuted)
	}
	titleW := p.captionWidth(gtx, title)
	y := gtx.Sp(theme.TypeSmall().Line) + gtx.Dp(theme.SpaceSM)
	x, rowH, w := 0, 0, titleW
	for _, s := range keys {
		size, err := kit.KbdSize(p.env, gtx, s)
		if err != nil && p.err == nil {
			p.err = err
		}
		if x > 0 && x+size.X > wrap {
			x, y, rowH = 0, y+rowH+gap, 0
		}
		if !dry {
			pt := image.Pt(x, y)
			at(gtx, pt, func(gtx *layout.Context) {
				if _, err := kit.Kbd(p.env, gtx, s, false); err != nil && p.err == nil {
					p.err = err
				}
			})
			p.runs = append(p.runs, image.Rectangle{Min: pt.Add(p.base), Max: pt.Add(p.base).Add(size)})
			p.cells[kbdCellKey(pi, gi, s)] = true
		}
		x += size.X + gap
		rowH = max(rowH, size.Y)
		w = max(w, x-gap)
	}
	return image.Pt(w, y+rowH)
}
