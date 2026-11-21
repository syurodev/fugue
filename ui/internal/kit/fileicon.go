package kit

import (
	"image"
	"image/color"
	"path/filepath"
	"strings"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit/icondata"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// symbolGrid is the Symbols coordinate grid in dp: paths are authored on it.
const symbolGrid = 24

// FileIcon returns the Symbols file glyph for one name, with lookup order:
// full filename → extension → generic file icon. The name is matched
// lower-case; unknown names degrade to the generic file icon ("document").
func FileIcon(name string) icondata.FileIcon {
	_, icon := fileIconData(name, false)
	return icon
}

// FolderIcon returns the Symbols folder glyph for one name. Known folder
// names (doc, docs, documents, documentation) map to folder-documents;
// every other name degrades to the generic folder icon ("folder").
func FolderIcon(name string) icondata.FileIcon {
	_, icon := fileIconData(name, true)
	return icon
}

// FileIconData returns the upstream Symbols name and glyph for one file
// name, with lookup order: full filename → extension → generic. The name
// is matched lower-case.
func FileIconData(name string) (string, icondata.FileIcon) {
	return fileIconData(name, false)
}

// FolderIconData returns the upstream Symbols name and glyph for one folder
// name.
func FolderIconData(name string) (string, icondata.FileIcon) {
	return fileIconData(name, true)
}

// fileIconData resolves one name to a Symbols name and glyph. For files the
// lookup order is full filename → extension → generic; for folders it is
// folder name → generic.
func fileIconData(name string, isFolder bool) (string, icondata.FileIcon) {
	name = strings.ToLower(name)
	if isFolder {
		if iconName, ok := icondata.FolderNames[name]; ok {
			if icon, ok := icondata.FileIcons[iconName]; ok {
				return iconName, icon
			}
		}
		iconName := icondata.FolderGeneric
		return iconName, icondata.FileIcons[iconName]
	}
	if iconName, ok := icondata.FileNames[name]; ok {
		if icon, ok := icondata.FileIcons[iconName]; ok {
			return iconName, icon
		}
	}
	ext := strings.TrimPrefix(filepath.Ext(name), ".")
	if iconName, ok := icondata.FileExtensions[ext]; ok {
		if icon, ok := icondata.FileIcons[iconName]; ok {
			return iconName, icon
		}
	}
	iconName := icondata.FileGeneric
	return iconName, icondata.FileIcons[iconName]
}

// symbolPaths caches the parsed fill subpaths of one Symbols glyph, parsed
// once on the 24 grid and scaled at draw time.
func (e *Env) symbolPaths(name, data string) ([]iconSubpath, error) {
	if e.symbolPathCache == nil {
		e.symbolPathCache = make(map[string][]iconSubpath)
	}
	if p, ok := e.symbolPathCache[name]; ok {
		return p, nil
	}
	p, err := parseIconPath(data)
	if err != nil {
		return nil, err
	}
	e.symbolPathCache[name] = p
	return p, nil
}

// symbolEvenOdd caches the parsed even-odd fill subpaths of one Symbols
// glyph, with subpath direction reversed so the non-zero winding rule
// reproduces the even-odd fill. Parsed once on the 24 grid.
func (e *Env) symbolEvenOdd(name, data string) ([]iconSubpath, error) {
	if e.symbolEvenOddCache == nil {
		e.symbolEvenOddCache = make(map[string][]iconSubpath)
	}
	if p, ok := e.symbolEvenOddCache[name]; ok {
		return p, nil
	}
	raw, err := parseIconPath(data)
	if err != nil {
		return nil, err
	}
	p := normalizeEvenOdd(raw)
	e.symbolEvenOddCache[name] = p
	return p, nil
}

// symbolStroke returns the cached stroke of one Symbols glyph at one dp
// size, building it once on the 24 grid.
func (e *Env) symbolStroke(name string, size unit.Dp, data string) (clip.Op, error) {
	if e.symbolStrokeCache == nil {
		e.symbolStrokeCache = make(map[symbolKey]clip.Op)
	}
	key := symbolKey{name: name, size: size}
	if st, ok := e.symbolStrokeCache[key]; ok {
		return st, nil
	}
	paths, err := parseIconPath(data)
	if err != nil {
		return clip.Op{}, err
	}
	w := theme.IconStroke(size) * symbolGrid / float32(size)
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
	e.symbolStrokeCache[key] = st
	return st, nil
}

// DrawSymbol draws one Symbols glyph at size inside a size×size box, filled
// with c. Fill paths use the non-zero winding rule; even-odd paths have
// subpath direction reversed so non-zero winding reproduces even-odd fill.
// Stroked paths use the kit's stroke width at size. Unknown or
// unparseable glyphs draw nothing and return an error.
func (e *Env) DrawSymbol(gtx *layout.Context, name string, icon *icondata.FileIcon, size unit.Dp, c color.NRGBA) (layout.Dimensions, error) {
	scale := float32(gtx.Dp(size)) / symbolGrid
	defer op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale))).Push(gtx.Ops).Pop()

	if icon.Fill != "" {
		paths, err := e.symbolPaths(name, icon.Fill)
		if err != nil {
			return layout.Dimensions{}, err
		}
		drawSymbolPaths(gtx.Ops, c, paths)
	}
	if icon.FillEvenOdd != "" {
		paths, err := e.symbolEvenOdd(name, icon.FillEvenOdd)
		if err != nil {
			return layout.Dimensions{}, err
		}
		drawSymbolPaths(gtx.Ops, c, paths)
	}
	if icon.Stroke != "" {
		stroke, err := e.symbolStroke(name, size, icon.Stroke)
		if err != nil {
			return layout.Dimensions{}, err
		}
		paint.FillShape(gtx.Ops, c, stroke)
	}
	return layout.Dimensions{Size: image.Pt(gtx.Dp(size), gtx.Dp(size))}, nil
}

// drawSymbolPaths fills one set of subpaths with the non-zero winding rule.
func drawSymbolPaths(ops *op.Ops, c color.NRGBA, paths []iconSubpath) {
	var p clip.Path
	p.Begin(ops)
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
	paint.FillShape(ops, c, clip.Outline{Path: p.End()}.Op())
}

// symbolKey identifies one cached Symbols stroke: an icon at a dp size.
type symbolKey struct {
	name string
	size unit.Dp
}

// normalizeEvenOdd rewrites raw subpaths so the non-zero winding rule
// reproduces the even-odd fill rule: subpaths at odd containment depth have
// their direction reversed, so their winding cancels the containing
// subpath's winding.
func normalizeEvenOdd(raw []iconSubpath) []iconSubpath {
	out := make([]iconSubpath, len(raw))
	for i, sp := range raw {
		depth := 0
		for j, other := range raw {
			if i == j {
				continue
			}
			if pointInSubpath(sp.Move, other) {
				depth++
			}
		}
		if depth%2 == 1 {
			out[i] = reverseSubpath(sp)
		} else {
			out[i] = sp
		}
	}
	return out
}

// reverseSubpath returns sp with its direction reversed: lines run backward
// and cubics swap their control points and traverse from end to start.
func reverseSubpath(sp iconSubpath) iconSubpath {
	n := len(sp.Segs)
	segs := make([]iconSeg, n)
	// The reversed subpath starts at the last point of the original
	// subpath (which is sp.Move if closed, or the last segment's end).
	start := sp.Move
	if n > 0 {
		last := sp.Segs[n-1]
		if last.Cubic {
			start = last.End
		} else {
			start = last.To
		}
	}
	cur := start
	for i := n - 1; i >= 0; i-- {
		orig := sp.Segs[i]
		if orig.Cubic {
			// Original: cur_orig --(C0,C1)--> orig.End
			// Reversed: orig.End --(C1,C0)--> cur_orig
			segs[n-1-i] = iconSeg{
				Cubic: true,
				C0:    orig.C1,
				C1:    orig.C0,
				End:   cur,
			}
			// After this reversed segment, the cursor is at orig's start
			// point, which is the previous segment's end (or sp.Move).
			cur = subpathStartPoint(sp, i)
		} else {
			// Original: cur_orig --> orig.To
			// Reversed: orig.To --> cur_orig
			segs[n-1-i] = iconSeg{
				Cubic: false,
				To:    cur,
			}
			cur = subpathStartPoint(sp, i)
		}
	}
	return iconSubpath{Move: start, Segs: segs, Closed: sp.Closed}
}

// subpathStartPoint returns the starting point of segment i: sp.Move for
// the first segment, or the end of the previous segment otherwise.
func subpathStartPoint(sp iconSubpath, i int) f32.Point {
	if i == 0 {
		return sp.Move
	}
	prev := sp.Segs[i-1]
	if prev.Cubic {
		return prev.End
	}
	return prev.To
}

// pointInSubpath reports whether p is inside sp, using ray casting with the
// curves approximated by line segments. Points exactly on the boundary are
// treated as outside.
func pointInSubpath(p f32.Point, sp iconSubpath) bool {
	inside := false
	pts := sampleSubpath(sp, 12)
	for i := range pts {
		j := (i + 1) % len(pts)
		a, b := pts[i], pts[j]
		if (a.Y > p.Y) != (b.Y > p.Y) {
			x := a.X + (p.Y-a.Y)*(b.X-a.X)/(b.Y-a.Y)
			if p.X < x {
				inside = !inside
			}
		}
	}
	return inside
}

// sampleSubpath returns the points of one subpath, sampling each cubic at
// n intervals and treating lines as single segments.
func sampleSubpath(sp iconSubpath, n int) []f32.Point {
	var pts []f32.Point
	pts = append(pts, sp.Move)
	cur := sp.Move
	for _, seg := range sp.Segs {
		if seg.Cubic {
			for k := 1; k <= n; k++ {
				t := float32(k) / float32(n)
				pts = append(pts, cubicPoint(cur, seg.C0, seg.C1, seg.End, t))
			}
			cur = seg.End
		} else {
			pts = append(pts, seg.To)
			cur = seg.To
		}
	}
	if sp.Closed && len(pts) > 0 && pts[len(pts)-1] != pts[0] {
		pts = append(pts, pts[0])
	}
	return pts
}

// cubicPoint evaluates a cubic Bézier at t: P0=start, C0,C1=controls, P3=end.
func cubicPoint(p0, c0, c1, p3 f32.Point, t float32) f32.Point {
	omt := 1 - t
	omt2 := omt * omt
	t2 := t * t
	x := omt2*omt*p0.X + 3*omt2*t*c0.X + 3*omt*t2*c1.X + t2*t*p3.X
	y := omt2*omt*p0.Y + 3*omt2*t*c0.Y + 3*omt*t2*c1.Y + t2*t*p3.Y
	return f32.Pt(x, y)
}
