package kit

import (
	"errors"
	"fmt"
	"image"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// Kbd, ui-kit Kbd (owner request 2026-11-16): one small chip that shows a
// keyboard shortcut — in the Menu's shortcut column now, and later in
// Tooltip and the command palette. The shortcut is structured data, never a
// string: the platform's notation is decided here, from Env.Platform that
// the window sets once; the component never asks the OS (§7).
//
// Look: type.small (Inter has every symbol the macOS notation needs, so the
// mono face is not required), padding 2 dp × 6 dp, radius.xs, FillSoft with
// no border, FgMuted text. It has no motion and no state of its own: the
// caller says whether the item it sits in is disabled.

// Platform picks the notation of a shortcut. The zero value is the Linux and
// Windows notation.
type Platform uint8

const (
	// PlatformPC writes words joined by "+": Ctrl+Shift+K.
	PlatformPC Platform = iota
	// PlatformMac writes symbols in the platform order, with no separator: ⌃⇧K.
	PlatformMac
)

// Mods is the set of modifiers of a shortcut.
type Mods uint8

// The modifiers. ModPrimary is the platform's command key — Cmd on macOS,
// Ctrl elsewhere; the others are the keys of their names (Super is Cmd on
// macOS, so with ModPrimary it shows once).
const (
	ModPrimary Mods = 1 << iota
	ModCtrl
	ModAlt
	ModShift
	ModSuper
)

// Key is the one key of a shortcut: a named key below, or Char for a letter,
// a digit or a punctuation key.
type Key string

// The named keys of a shortcut.
const (
	KeyEnter     Key = "Enter"
	KeyEscape    Key = "Escape"
	KeyTab       Key = "Tab"
	KeySpace     Key = "Space"
	KeyBackspace Key = "Backspace"
	KeyDelete    Key = "Delete"
	KeyLeft      Key = "Left"
	KeyUp        Key = "Up"
	KeyRight     Key = "Right"
	KeyDown      Key = "Down"
	KeyHome      Key = "Home"
	KeyEnd       Key = "End"
	KeyPageUp    Key = "PageUp"
	KeyPageDown  Key = "PageDown"
)

// Char is the key of a letter, a digit or a punctuation mark.
func Char(r rune) Key { return Key(string(r)) }

// KeyF is function key n, 1 to 12.
func KeyF(n int) Key { return Key(fmt.Sprintf("F%d", n)) }

// Shortcut is a key with its modifiers. The zero value is no shortcut.
type Shortcut struct {
	Mods Mods
	Key  Key
}

var errKbdKey = errors.New("unknown shortcut key")

// namedKeys is how each named key is written: its macOS symbol and its word
// elsewhere. A function returning an array keeps the table out of package
// state and off the heap.
func namedKeys() [14]struct {
	key     Key
	mac, pc string
} {
	return [14]struct {
		key     Key
		mac, pc string
	}{
		{KeyEnter, "↩", "Enter"},
		{KeyEscape, "⎋", "Esc"},
		{KeyTab, "⇥", "Tab"},
		{KeySpace, "␣", "Space"},
		{KeyBackspace, "⌫", "Backspace"},
		{KeyDelete, "⌦", "Delete"},
		{KeyLeft, "←", "Left"},
		{KeyUp, "↑", "Up"},
		{KeyRight, "→", "Right"},
		{KeyDown, "↓", "Down"},
		{KeyHome, "↖", "Home"},
		{KeyEnd, "↘", "End"},
		{KeyPageUp, "⇞", "PgUp"},
		{KeyPageDown, "⇟", "PgDn"},
	}
}

// text is the shortcut as the platform writes it.
func (s Shortcut) text(p Platform) (string, error) {
	k, err := s.keyText(p)
	if err != nil {
		return "", err
	}
	if p == PlatformMac {
		var b strings.Builder
		for _, m := range []struct {
			on  bool
			sym string
		}{
			{s.Mods&ModCtrl != 0, "⌃"},
			{s.Mods&ModAlt != 0, "⌥"},
			{s.Mods&ModShift != 0, "⇧"},
			{s.Mods&(ModPrimary|ModSuper) != 0, "⌘"},
		} {
			if m.on {
				b.WriteString(m.sym)
			}
		}
		return b.String() + k, nil
	}
	var parts []string
	for _, m := range []struct {
		on   bool
		word string
	}{
		{s.Mods&(ModPrimary|ModCtrl) != 0, "Ctrl"},
		{s.Mods&ModAlt != 0, "Alt"},
		{s.Mods&ModShift != 0, "Shift"},
		{s.Mods&ModSuper != 0, "Super"},
	} {
		if m.on {
			parts = append(parts, m.word)
		}
	}
	return strings.Join(append(parts, k), "+"), nil
}

// keyText writes the key alone: a named key by the platform's table, F1–F12
// and a single character, which a letter shows in uppercase.
func (s Shortcut) keyText(p Platform) (string, error) {
	for _, k := range namedKeys() {
		if k.key == s.Key {
			if p == PlatformMac {
				return k.mac, nil
			}
			return k.pc, nil
		}
	}
	if n, isF := fKey(s.Key); isF && n >= 1 && n <= 12 {
		return string(s.Key), nil
	}
	if r, size := utf8.DecodeRuneInString(string(s.Key)); size == len(s.Key) && size > 0 && r != unicode.ReplacementChar && !unicode.IsSpace(r) {
		return strings.ToUpper(string(r)), nil
	}
	return "", fmt.Errorf("%w: %q", errKbdKey, string(s.Key))
}

// fKey parses "F1".."F12".
func fKey(k Key) (int, bool) {
	rest, ok := strings.CutPrefix(string(k), "F")
	n, err := strconv.Atoi(rest)
	return n, ok && err == nil && strconv.Itoa(n) == rest
}

// kbdSize is the chip's box for a label: the text plus the paddings.
func kbdSize(env *Env, gtx *layout.Context, label string) image.Point {
	t := theme.TypeSmall()
	return image.Pt(
		measureLabel(env, gtx, t, label).X+2*gtx.Dp(theme.SpaceSM),
		labelLine(env, gtx, t)+2*gtx.Dp(theme.SpaceXXS),
	)
}

// KbdSize is the box Kbd draws s in. An unknown key is a programming error
// and comes back as an error.
func KbdSize(env *Env, gtx *layout.Context, s Shortcut) (image.Point, error) {
	label, err := s.text(env.Platform)
	if err != nil {
		return image.Point{}, fmt.Errorf("measure the shortcut: %w", err)
	}
	return kbdSize(env, gtx, label), nil
}

// Kbd draws the chip for s at the cursor. A disabled one draws its text in
// FgDisabled, like the item it sits in.
func Kbd(env *Env, gtx *layout.Context, s Shortcut, disabled bool) (layout.Dimensions, error) {
	size, err := drawKbd(env, gtx, s, disabled, image.Point{})
	return layout.Dimensions{Size: size}, err
}

// drawKbd draws the chip at the cursor; origin is where the cursor sits in the
// frame of the control being drawn, which the text-run recorder reports in.
func drawKbd(env *Env, gtx *layout.Context, s Shortcut, disabled bool, origin image.Point) (image.Point, error) {
	label, err := s.text(env.Platform)
	if err != nil {
		return image.Point{}, fmt.Errorf("draw the shortcut: %w", err)
	}
	c := env.Th.Chrome()
	size := kbdSize(env, gtx, label)
	fillRRect(gtx.Ops, image.Rectangle{Max: size}, gtx.Dp(theme.RadiusXS), c.FillSoft)
	fg := c.FgMuted
	if disabled {
		fg = c.FgDisabled
	}
	t, pad := theme.TypeSmall(), image.Pt(gtx.Dp(theme.SpaceSM), 0)
	pad.Y = centerLabelY(env, gtx, t, size.Y)
	pt := pad
	at(gtx, pt, func(gtx *layout.Context) {
		dims := drawText(env, gtx, t, fg, label, false)
		recordRun(env, image.Rectangle{Max: dims.Size}.Add(origin).Add(pt))
	})
	return size, nil
}
