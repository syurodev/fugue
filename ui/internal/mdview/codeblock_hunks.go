package mdview

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// The diff hunk geometry of desktop-app v0.11 §5.4 (F10.4c): adjacent
// add/del rows merge into one tinted block with a concentric radius, and
// same-direction hunks sitting together share a boundary gap.

// ConcentricRadius returns the corner radius concentric with an outer rounded
// frame of radius outerDp inset by pad pixels, clamped to at least RadiusSM.
// Code blocks and the read-only file viewer share this helper.
func ConcentricRadius(gtx *layout.Context, outerDp unit.Dp, pad int) int {
	return max(gtx.Dp(outerDp)-pad, gtx.Dp(theme.RadiusSM))
}

type diffHunk struct {
	kind  DiffKind
	start int
	end   int
	rect  image.Rectangle
}

func collectDiffHunks(
	diffLines []DiffLine, flows []*flow, visCount, pad, bodyTop, bodyW, scrollY, lineH, gap int,
) []diffHunk {
	n := min(visCount, len(diffLines), len(flows))
	var hunks []diffHunk
	y := bodyTop - scrollY
	for i := range n {
		rowH := max(flows[i].height, lineH)
		kind := diffLines[i].Kind
		if kind == DiffAdd || kind == DiffDel {
			if m := len(hunks); m > 0 && hunks[m-1].kind == kind && hunks[m-1].end == i {
				hunks[m-1].end = i + 1
				hunks[m-1].rect.Max.Y = y + rowH
			} else {
				hunks = append(hunks, diffHunk{
					kind:  kind,
					start: i,
					end:   i + 1,
					rect:  image.Rect(pad, y, pad+bodyW, y+rowH),
				})
			}
		}
		y += rowH
	}
	applyHunkBoundaryGaps(hunks, gap)
	return hunks
}

func applyHunkBoundaryGaps(hunks []diffHunk, gap int) {
	if gap <= 0 {
		return
	}
	topShrink := gap / 2
	botShrink := gap - topShrink
	for i := 0; i+1 < len(hunks); i++ {
		if hunks[i].end == hunks[i+1].start {
			hunks[i].rect.Max.Y -= topShrink
			hunks[i+1].rect.Min.Y += botShrink
		}
	}
}

func (v *view) paintDiffHunks(gtx *layout.Context, hunks []diffHunk, bodyTop, viewH, radius int) {
	bodyBot := bodyTop + viewH
	for i := range hunks {
		r := hunks[i].rect
		if r.Max.Y <= r.Min.Y || r.Max.Y <= bodyTop || r.Min.Y >= bodyBot {
			continue
		}
		paint.FillShape(gtx.Ops, diffHunkColor(v.th, hunks[i].kind), clip.UniformRRect(r, radius).Op(gtx.Ops))
	}
}

func diffHunkColor(th *theme.Theme, kind DiffKind) color.NRGBA {
	if kind == DiffAdd {
		return th.StatusTone(theme.StatusSuccess).Fill
	}
	return th.StatusTone(theme.StatusDanger).Fill
}
