package main

import (
	"fmt"
	"io"

	"gioui.org/text"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kitspike"
)

// printTypeMetrics answers Q4: for every §3.3 token, shape two lines of the
// Vietnamese stress string and report, per line, where the ink falls inside
// the line box, whether consecutive lines' ink overlaps, and the smallest
// line-height ratio that keeps them apart. Pure shaping: no window needed.
func printTypeMetrics(w io.Writer, scale float32) error {
	sh, err := fonts.Shaper()
	if err != nil {
		return err
	}
	tokens := []struct {
		name string
		t    kitspike.Type
	}{
		{"caption", kitspike.TypeCaption}, {"small", kitspike.TypeSmall},
		{"body", kitspike.TypeBody}, {"label", kitspike.TypeLabel},
		{"prose", kitspike.TypeProse}, {"title", kitspike.TypeTitle},
		{"heading", kitspike.TypeHeading}, {"display", kitspike.TypeDisplay},
		{"code", kitspike.TypeCode}, {"codeSmall", kitspike.TypeCodeSmall},
	}
	text2 := kitspike.VietnameseInk + "\n" + kitspike.VietnameseInk
	if _, err = fmt.Fprintf(w, "%-10s %4s %4s | %8s %8s %8s | %9s %9s | %9s\n",
		"token", "sp", "line", "inkTop", "inkBot", "inkH", "clipTop", "clipBot", "lineGap"); err != nil {
		return err
	}
	for _, tk := range tokens {
		m := unit.Metric{PxPerDp: scale, PxPerSp: scale}
		linePx := float32(m.Sp(tk.t.Line))
		sh.LayoutString(kitspike.TextParameters(sh, tk.t, m, 1<<20), text2)
		// Split glyphs into lines on the line-break flag; record each
		// line's ink extent and baseline.
		type ln struct {
			baseline float32
			inkTop   float32
			inkBot   float32
			asc      float32
			desc     float32
		}
		var lines []ln
		cur := ln{baseline: -1, inkTop: 1e9, inkBot: -1e9}
		flush := func() {
			if cur.baseline >= 0 {
				lines = append(lines, cur)
			}
			cur = ln{baseline: -1, inkTop: 1e9, inkBot: -1e9}
		}
		for g, ok := sh.NextGlyph(); ok; g, ok = sh.NextGlyph() {
			cur.baseline = float32(g.Y)
			cur.inkTop = min(cur.inkTop, float32(g.Y)+float32(g.Bounds.Min.Y)/64)
			cur.inkBot = max(cur.inkBot, float32(g.Y)+float32(g.Bounds.Max.Y)/64)
			cur.asc = max(cur.asc, float32(g.Ascent)/64)
			cur.desc = max(cur.desc, float32(g.Descent)/64)
			if g.Flags&text.FlagLineBreak != 0 {
				flush()
			}
		}
		flush()
		if len(lines) != 2 {
			return fmt.Errorf("want two shaped lines, got %d", len(lines))
		}
		// How far ink pokes out of its own line box. The box of a line sits
		// around its baseline with the natural ascent/descent plus the
		// half-leading the absolute line height adds.
		lead := (linePx - (lines[0].asc + lines[0].desc)) / 2
		clipTop := lines[0].inkTop - (lines[0].baseline - lines[0].asc - lead)
		clipBot := lines[1].inkBot - (lines[1].baseline + lines[1].desc + lead)
		gap := lines[1].inkTop - lines[0].inkBot // negative: the lines' ink overlaps
		inkH := lines[0].inkBot - lines[0].inkTop
		if _, err = fmt.Fprintf(w, "%-10s %4.0f %4.0f | %8.1f %8.1f %8.1f | %9.1f %9.1f | %9.1f\n",
			tk.name, float32(tk.t.Size), linePx, lines[0].inkTop, lines[0].inkBot, inkH, clipTop, clipBot, gap); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(w, `
All numbers in px at the given scale; the string is `+"`"+kitspike.VietnameseInk+"`"+`.
clipTop/clipBot: how far ink reaches outside its line box (negative = fits).
lineGap: distance between line 1's ink bottom and line 2's ink top
(negative means the stacked marks of line 2 touch line 1's descenders).
safeLineRatio = inkH / linePx is the smallest line-height ratio that keeps
two lines of this token apart; §3.3 requires >= 1.35 with margin.`)
	return err
}
