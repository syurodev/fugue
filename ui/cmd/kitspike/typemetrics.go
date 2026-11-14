package main

import (
	"fmt"
	"io"

	"gioui.org/text"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kitspike"
)

// printTypeMetrics answers Q4: for every §3.3 token, shape the Vietnamese
// stress string twice and report the ink bounds against the line box, plus
// the smallest safe line-height ratio. It is pure shaping: no window needed.
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
	if _, err = fmt.Fprintf(w, "%-10s %5s %5s | %8s %8s | %9s %9s | %8s\n",
		"token", "sp", "line", "inkTop", "inkBot", "gapAbove", "gapBelow", "safeRatio"); err != nil {
		return err
	}
	for _, tk := range tokens {
		m := unit.Metric{PxPerDp: scale, PxPerSp: scale}
		// Shape with the token's absolute line height, LineHeightScale = 1.
		sh.LayoutString(kitspike.TextParameters(sh, tk.t, m, 1<<20), text2)
		var inkTop, inkBot float32 = 1e9, -1e9
		var lineTops []float32
		lineStart := float32(0)
		for g, ok := sh.NextGlyph(); ok; g, ok = sh.NextGlyph() {
			top := float32(g.Y) + float32(g.Bounds.Min.Y)/64
			bot := float32(g.Y) + float32(g.Bounds.Max.Y)/64
			inkTop = min(inkTop, top)
			inkBot = max(inkBot, bot)
			if g.Flags&text.FlagLineBreak != 0 {
				lineStart = float32(g.Y)
				lineTops = append(lineTops, lineStart)
			}
		}
		_ = lineTops
		line := float32(m.Sp(tk.t.Line))
		// The line box of the first line: the shaper starts at y=0 with
		// half-leading; inkTop - 0 is the head margin, and the bottom
		// margin is line - (inkBot - 0) for a single line of box height
		// `line`. With two lines the overlap test is (inkBot - inkTop of
		// line 1) versus the second baseline.
		gapAbove := inkTop
		gapBelow := 2*line - inkBot // bottom of the second line's box
		inkExtent := inkBot - inkTop
		safe := inkExtent / line
		// A two-line block with explicit line height L places line 2's ink
		// at inkTop + L + (leading shifts); the honest overlap check is
		// whether inkExtent <= L: then no two lines of this token can
		// touch, whatever the leading.
		if _, err = fmt.Fprintf(w, "%-10s %5.0f %5.0f | %8.1f %8.1f | %9.1f %9.1f | %8.2f\n",
			tk.name, float32(tk.t.Size), line, inkTop, inkBot, gapAbove, gapBelow, safe); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(w, `
safeRatio = ink height / line height: 1.00 means the two lines just touch.
gapAbove/Below are in px at the given scale, for a two-line block.
Anything with safeRatio > 1.0 overlaps; anything with gapBelow < 0 clips a
descender at the bottom of a single-line box of exactly `+"`line`"+` px.`)
	return err
}


