package kitspike

import (
	"embed"
	"fmt"
	"strings"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"image/color"
)

// Ten Lucide icons (ui-kit §10): the SVG path data lives in testdata with
// the ISC license. Only these ten are packaged; adding one is a line here
// plus a file there.
//
//go:embed testdata/*.txt
var iconData embed.FS

// iconSpec is one parsed icon: subpaths on the 24 dp Lucide grid.
type iconSpec struct {
	name     string
	subpaths []svgSubpath
}

// icons holds the parsed set, built once at first use.
var icons map[string]iconSpec

// strokeCache caches the stroke of one icon at one size class, so
// alternative (b) of Q5 never re-encodes the path per frame; the transform
// scales it to the target size.
type strokeCache struct {
	stroke clip.Stroke
}

var strokeCacheMap map[string]strokeCache

func loadIcons() error {
	if icons != nil {
		return nil
	}
	icons = make(map[string]iconSpec)
	strokeCacheMap = make(map[string]strokeCache)
	for _, name := range iconNames {
		raw, err := iconData.ReadFile("testdata/" + name + ".txt")
		if err != nil {
			return fmt.Errorf("read the icon %s: %w", name, err)
		}
		// One line per SVG <path> element: each starts its own coordinate
		// space, so relative commands must not carry across lines.
		var subpaths []svgSubpath
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			p, err := parseSVGPath(strings.TrimSpace(line))
			if err != nil {
				return fmt.Errorf("parse the icon %s: %w", name, err)
			}
			subpaths = append(subpaths, p...)
		}
		icons[name] = iconSpec{name: name, subpaths: subpaths}
	}
	return nil
}

// iconOps holds the cached icon strokes; it is written once per (icon,
// size) pair and never reset.
var iconOps op.Ops

// iconNames are the ten packaged icons, in gallery order.
var iconNames = []string{
	"check", "chevron-down", "chevron-right", "search", "x",
	"eye", "eye-off", "copy", "rotate-ccw", "circle-help",
}

// iconGrid is the Lucide coordinate grid in dp: paths are authored on it and
// scaled to the target size.
const iconGrid = 24

// drawIconFresh renders name at size with the stroke width of §3.4,
// re-parsing nothing (parse once per process) but re-stroking and
// re-tessellating the path every frame: alternative (a) of Q5.
func drawIconFresh(gtx *layout.Context, name string, size unit.Dp, c color.NRGBA) error {
	if err := loadIcons(); err != nil {
		return err
	}
	spec := icons[name]
	scale := float32(gtx.Dp(size)) / iconGrid
	var p clip.Path
	p.Begin(gtx.Ops)
	for _, sp := range spec.subpaths {
		p.MoveTo(scalePt(sp.Move, scale))
		for _, seg := range sp.Segments {
			if seg.IsCubic {
				p.CubeTo(scalePt(seg.C0, scale), scalePt(seg.C1, scale), scalePt(seg.End, scale))
			} else {
				p.LineTo(scalePt(seg.To, scale))
			}
		}
	}
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: IconStroke(size) * gtx.Metric.PxPerDp}.Op())
	return nil
}

// drawIconCached renders name at size from a PathSpec stroked once on the 24
// grid and transformed per frame: alternative (b) of Q5. The stroke width is
// chosen so it is IconStroke(size) after the transform.
func drawIconCached(gtx *layout.Context, name string, size unit.Dp, c color.NRGBA) error {
	if err := loadIcons(); err != nil {
		return err
	}
	key := fmt.Sprintf("%s@%v", name, size)
	st, ok := strokeCacheMap[key]
	if !ok {
		// Stroke on the 24 grid so that scaling to size dp yields exactly
		// IconStroke(size): w * size/24 = IconStroke(size).
		w := IconStroke(size) * iconGrid / float32(gtx.Dp(size))
		var p clip.Path
		p.Begin(&iconOps)
		spec := icons[name]
		for _, sp := range spec.subpaths {
			p.MoveTo(f32.Pt(sp.Move.X, sp.Move.Y))
			for _, seg := range sp.Segments {
				if seg.IsCubic {
					p.CubeTo(f32.Pt(seg.C0.X, seg.C0.Y), f32.Pt(seg.C1.X, seg.C1.Y), f32.Pt(seg.End.X, seg.End.Y))
				} else {
					p.LineTo(f32.Pt(seg.To.X, seg.To.Y))
				}
			}
		}
		st = strokeCache{stroke: clip.Stroke{Path: p.End(), Width: w}}
		strokeCacheMap[key] = st
	}
	scale := float32(gtx.Dp(size)) / iconGrid
	defer op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale))).Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, c, st.stroke.Op())
	return nil
}

func scalePt(p f32Point, s float32) f32.Point {
	return f32.Pt(p.X*s, p.Y*s)
}
