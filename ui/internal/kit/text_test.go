package kit

import (
	"errors"
	"fmt"
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"golang.org/x/image/math/fixed"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// vietnameseInk is the UK-E08 stress string: stacked double diacritics are
// the tallest ink Inter produces, and lowercase "ỷ" the deepest descender.
const vietnameseInk = "Ễ Ặ Ỡ Ậ ỷ ộ"

// TestEveryTypeTokenKeepsStackedMarksInsideTheLine is UK-E08: at every type
// token with an absolute line height (codeInline's line belongs to its
// containing prose run, so it has nothing of its own to keep), two lines of
// the stress string must show all their ink — nothing clipped at the
// bottom, at most 1 dp of ink above the line box's top, and a visible gap
// between the lines' ink. Measured through the shaper at pixel scale 1 and
// 1.25.
func TestEveryTypeTokenKeepsStackedMarksInsideTheLine(t *testing.T) {
	t.Parallel()
	tokens := []struct {
		name string
		tok  theme.Type
	}{
		{"caption", theme.TypeCaption()},
		{"small", theme.TypeSmall()},
		{"body", theme.TypeBody()},
		{"label", theme.TypeLabel()},
		{"prose", theme.TypeProse()},
		{"title", theme.TypeTitle()},
		{"heading", theme.TypeHeading()},
		{"display", theme.TypeDisplay()},
		{"code", theme.TypeCode()},
		{"codeSmall", theme.TypeCodeSmall()},
	}
	for _, scale := range []float32{1, 1.25} {
		for _, tk := range tokens {
			t.Run(fmt.Sprintf("%s@%v", tk.name, scale), func(t *testing.T) {
				t.Parallel()
				// The shaper is not safe for concurrent shaping, and the
				// subtests run in parallel: each shapes with its own.
				sh, err := fonts.Shaper()
				if err != nil {
					t.Fatalf("build the shaper: %v", err)
				}
				assertInkInsideTheLine(t, sh, tk.tok, scale)
			})
		}
	}
}

// assertInkInsideTheLine shapes two lines of the stress string at one token
// and pixel scale and checks the UK-E08 bounds: no clipping at the bottom,
// at most 1 dp of ink above the line box, and a visible gap between lines.
func assertInkInsideTheLine(t *testing.T, sh *text.Shaper, tok theme.Type, scale float32) {
	t.Helper()
	m := unit.Metric{PxPerDp: scale, PxPerSp: scale}
	linePx := float32(m.Sp(tok.Line))
	sh.LayoutString(parameters(tok, m), vietnameseInk+"\n"+vietnameseInk)
	lines, err := collectLines(sh)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("want two shaped lines, got %d", len(lines))
	}
	// The line box sits around its baseline with the natural ascent/descent
	// plus the half-leading the absolute line height adds.
	lead := (linePx - (lines[0].asc + lines[0].desc)) / 2
	topPoke := lines[0].baseline - lines[0].asc - lead - lines[0].inkTop
	botPoke := lines[1].inkBot - (lines[1].baseline + lines[1].desc + lead)
	gap := lines[1].inkTop - lines[0].inkBot
	if topPoke > 1*scale {
		t.Errorf("ink pokes %.2f px above the line top, limit %.0f px", topPoke, 1*scale)
	}
	if botPoke > 0 {
		t.Errorf("ink clipped at the bottom by %.2f px", botPoke)
	}
	if gap <= 0 {
		t.Errorf("the two lines touch: gap %.2f px", gap)
	}
}

// lineBox is one shaped line's ink extent and baseline, in px.
type lineBox struct {
	baseline float32
	inkTop   float32
	inkBot   float32
	asc      float32
	desc     float32
}

// errNoGlyphs reports a shaper run that produced nothing.
var errNoGlyphs = errors.New("shaper produced no glyphs")

// collectLines splits the shaper's glyphs into lines on the line-break
// flag and records each line's ink extent and baseline.
func collectLines(sh *text.Shaper) ([]lineBox, error) {
	var lines []lineBox
	cur := lineBox{baseline: -1, inkTop: 1e9, inkBot: -1e9}
	flush := func() {
		if cur.baseline >= 0 {
			lines = append(lines, cur)
		}
		cur = lineBox{baseline: -1, inkTop: 1e9, inkBot: -1e9}
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
	if len(lines) == 0 {
		return nil, errNoGlyphs
	}
	return lines, nil
}

// parameters builds the shaping parameters of a type token: absolute line
// height, LineHeightScale 1 (§3.0), unwrapped.
func parameters(tok theme.Type, m unit.Metric) text.Parameters {
	face := font.Font{Typeface: fonts.Sans, Weight: tok.Weight}
	if tok.Mono {
		face.Typeface = fonts.Mono
	}
	return text.Parameters{
		Font:            face,
		PxPerEm:         fixed.I(m.Sp(tok.Size)),
		MaxWidth:        1 << 20,
		LineHeight:      fixed.I(m.Sp(tok.Line)),
		LineHeightScale: 1,
		WrapPolicy:      text.WrapWords,
	}
}

// TestMiddleEllipsisKeepsFileName verifies that fitMiddleEllipsis inserts "…"
// in the middle while keeping the trailing filename intact when space allows.
func TestMiddleEllipsisKeepsFileName(t *testing.T) {
	t.Parallel()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Light(sh)
	var ops op.Ops
	env := &Env{Th: &th, Sh: sh}
	env.Begin(time.Unix(1_700_000_000, 0))
	gtx := &layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1.25, PxPerSp: 1.25},
		Constraints: layout.Constraints{Max: image.Pt(220, 40)},
	}
	path := "ui/internal/very/deeply/nested/subpackage/component/codeblock_draw.go"
	got := fitMiddleEllipsis(env, gtx, theme.TypeCodeSmall(), path, 220)
	if !strings.Contains(got, "…") || !strings.HasSuffix(got, "/codeblock_draw.go") {
		t.Fatalf("fitMiddleEllipsis = %q, want middle ellipsis keeping /codeblock_draw.go", got)
	}
	if w := measureLabel(env, gtx, theme.TypeCodeSmall(), got).X; w > 220 {
		t.Fatalf("fitMiddleEllipsis width = %d, want <= 220", w)
	}
}
