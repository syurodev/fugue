package kit

import (
	"errors"
	"fmt"
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit/icondata"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// IconName names one packaged Lucide icon (ui-kit §6.1): one glyph stroked
// and filled in a caller-supplied theme color; a status color only on icons
// that carry a state meaning, and never as the sole carrier of information.
//
// Strokes are cached per (icon, dp size) in the Env's never-reset op.Ops —
// a clip.PathSpec stays bound to the ops that recorded it — and the cached
// path lives on the 24 grid: the affine transform at draw time scales it to
// the current metric, so the cache holds no device pixels and survives a
// monitor or UI-scale change (§3.0). The spike measured the cache at 1.06 ms
// per 100 icons against 4.53 ms re-stroked per frame.
type IconName string

// The icons packaged for kit v1's components. Adding one is a line here,
// its Lucide path data in icondata's testdata, and a regeneration run.
const (
	IconCheck        IconName = "check"
	IconChevronDown  IconName = "chevron-down"
	IconChevronRight IconName = "chevron-right"
	IconSearch       IconName = "search"
	IconClose        IconName = "x"
	IconEye          IconName = "eye"
	IconEyeOff       IconName = "eye-off"
	IconCopy         IconName = "copy"
	IconRestart      IconName = "rotate-ccw"
	IconHelp         IconName = "circle-help"
)

// iconGrid is the Lucide coordinate grid in dp: paths are authored on it.
const iconGrid = 24

// errIconUnknown reports a name outside the packaged set.
var errIconUnknown = errors.New("unknown icon")

// pathOf returns the generated path data of one icon.
func pathOf(n IconName) (string, error) {
	switch n {
	case IconCheck:
		return icondata.Check, nil
	case IconChevronDown:
		return icondata.ChevronDown, nil
	case IconChevronRight:
		return icondata.ChevronRight, nil
	case IconSearch:
		return icondata.Search, nil
	case IconClose:
		return icondata.X, nil
	case IconEye:
		return icondata.Eye, nil
	case IconEyeOff:
		return icondata.EyeOff, nil
	case IconCopy:
		return icondata.Copy, nil
	case IconRestart:
		return icondata.RotateCCW, nil
	case IconHelp:
		return icondata.CircleHelp, nil
	}
	return "", fmt.Errorf("%w: %q", errIconUnknown, string(n))
}

// iconOf returns the parsed subpaths of one icon, parsing on first use.
func (e *Env) iconOf(n IconName) ([]iconSubpath, error) {
	if e.iconPaths == nil {
		e.iconPaths = make(map[IconName][]iconSubpath)
	}
	if p, ok := e.iconPaths[n]; ok {
		return p, nil
	}
	d, err := pathOf(n)
	if err != nil {
		return nil, err
	}
	p, err := parseIconPath(d)
	if err != nil {
		return nil, fmt.Errorf("parse the icon %s: %w", n, err)
	}
	e.iconPaths[n] = p
	return p, nil
}

// Icon draws name at size (§3.4: IconXS–IconLG) in c inside a size×size box.
// An unknown icon or unparseable data is a programming error and comes back
// as an error instead of drawing nothing.
func (e *Env) Icon(gtx *layout.Context, name IconName, size unit.Dp, c color.NRGBA) (layout.Dimensions, error) {
	stroke, err := e.iconStroke(gtx, name, size)
	if err != nil {
		return layout.Dimensions{}, err
	}
	scale := float32(gtx.Dp(size)) / iconGrid
	defer op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale))).Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, c, stroke)
	return layout.Dimensions{Size: image.Pt(gtx.Dp(size), gtx.Dp(size))}, nil
}

// iconStroke returns the cached stroke of one icon at one dp size, building
// it once on the 24 grid: the width is chosen so the draw-time transform
// lands on theme.IconStroke(size) exactly.
func (e *Env) iconStroke(gtx *layout.Context, name IconName, size unit.Dp) (clip.Op, error) {
	if e.iconStrokes == nil {
		e.iconStrokes = make(map[iconKey]clip.Op)
	}
	key := iconKey{name: name, size: size}
	if st, ok := e.iconStrokes[key]; ok {
		return st, nil
	}
	paths, err := e.iconOf(name)
	if err != nil {
		return clip.Op{}, err
	}
	// Stroke on the 24 grid: w * (size/24) = IconStroke(size) dp. Closed
	// subpaths carry a trailing line back to their move point from the
	// parser, so no extra close op is needed.
	w := theme.IconStroke(size) * iconGrid / float32(gtx.Dp(size))
	var p clip.Path
	p.Begin(&e.iconOps)
	for _, sp := range paths {
		p.MoveTo(sp.Move)
		for _, seg := range sp.Segs {
			if seg.Cubic {
				p.CubeTo(seg.C0, seg.C1, seg.End)
			} else {
				p.LineTo(seg.To)
			}
		}
	}
	st := clip.Stroke{Path: p.End(), Width: w}.Op()
	e.iconStrokes[key] = st
	return st, nil
}
