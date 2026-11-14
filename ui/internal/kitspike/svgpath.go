package kitspike

import (
	"fmt"
	"math"
	"strconv"
)

// A parsed SVG path (the subset Lucide uses): each element is one subpath of
// straight lines and cubic Béziers. Arcs are converted to cubics while
// parsing, so a renderer only ever sees lines and cubics; Gio has ArcTo but
// with different semantics than SVG's arc command, and the
// endpoint-to-center conversion is the standard small algorithm every SVG
// renderer runs.
type svgPath []svgSubpath

// svgSubpath is one open-or-closed run of segments starting at Move.
type svgSubpath struct {
	Move     f32Point
	Segments []svgSegment
	Closed   bool
}

// svgSegment is one line or cubic; exactly one of the two shapes is used.
type svgSegment struct {
	To          f32Point // line
	C0, C1, End f32Point // cubic
	IsCubic     bool
}

// f32Point is gioui.org/f32.Point minus the import: the parser is unit-free.
type f32Point struct{ X, Y float32 }

// parseSVGPath parses path data such as Lucide's. It supports M m L l H h V
// v C c S s Q q T t A a and Z z, with implicit repetition after M m L l H h
// V v C c S s Q q T t A a as the SVG grammar allows.
func parseSVGPath(d string) (svgPath, error) {
	toks := tokenizePath(d)
	if len(toks) == 0 {
		return nil, fmt.Errorf("empty path data")
	}
	var p svgPath
	var cur f32Point
	var sub *svgSubpath
	var lastC, lastQ f32Point
	var haveC, haveQ bool
	// pop reads the next n numbers.
	pop := func(n int) ([]float64, error) {
		if len(toks) < n {
			return nil, fmt.Errorf("path data ended early: want %d numbers, have %d", n, len(toks))
		}
		vals := make([]float64, n)
		for i := range vals {
			v, err := strconv.ParseFloat(toks[i], 64)
			if err != nil {
				return nil, fmt.Errorf("bad number %q: %w", toks[i], err)
			}
			vals[i] = v
		}
		toks = toks[n:]
		return vals, nil
	}
	// pt resolves one coordinate pair: relative commands offset the cursor,
	// absolute ones name it directly.
	pt := func(rel bool, vx, vy float64) f32Point {
		if rel {
			return f32Point{cur.X + float32(vx), cur.Y + float32(vy)}
		}
		return f32Point{float32(vx), float32(vy)}
	}
	line := func(rel bool, vx, vy float64) {
		to := pt(rel, vx, vy)
		sub.Segments = append(sub.Segments, svgSegment{To: to})
		cur = to
		haveC, haveQ = false, false
	}
	cubic := func(rel bool, x0, y0, x1, y1, x2, y2 float64) {
		c0 := pt(rel, x0, y0)
		c1 := pt(rel, x1, y1)
		end := pt(rel, x2, y2)
		sub.Segments = append(sub.Segments, svgSegment{C0: c0, C1: c1, End: end, IsCubic: true})
		cur, lastC, haveC = end, c1, true
		haveQ = false
	}
	begin := func(to f32Point) {
		p = append(p, svgSubpath{Move: to})
		sub = &p[len(p)-1]
		cur = to
		haveC, haveQ = false, false
	}
	for len(toks) > 0 {
		cmd := toks[0]
		toks = toks[1:]
		if len(cmd) != 1 || cmd[0] < 'A' || cmd[0] > 'z' {
			return nil, fmt.Errorf("bad path command %q", cmd)
		}
		rel := cmd[0] >= 'a' && cmd[0] <= 'z'
		switch c := cmd[0] | 0x20; c {
		case 'm':
			v, err := pop(2)
			if err != nil {
				return nil, err
			}
			to := pt(rel && sub != nil, v[0], v[1]) // a leading m is absolute
			begin(to)
			for moreNumbers(toks, 2) { // implicit pairs continue as l
				v, err := pop(2)
				if err != nil {
					return nil, err
				}
				line(rel, v[0], v[1])
			}
		case 'l':
			for {
				v, err := pop(2)
				if err != nil {
					return nil, err
				}
				line(rel, v[0], v[1])
				if !moreNumbers(toks, 2) {
					break
				}
			}
		case 'h':
			for {
				v, err := pop(1)
				if err != nil {
					return nil, err
				}
				x := v[0]
				if rel {
					x += float64(cur.X)
				}
				to := f32Point{float32(x), cur.Y}
				sub.Segments = append(sub.Segments, svgSegment{To: to})
				cur = to
				haveC, haveQ = false, false
				if !moreNumbers(toks, 1) {
					break
				}
			}
		case 'v':
			for {
				v, err := pop(1)
				if err != nil {
					return nil, err
				}
				y := v[0]
				if rel {
					y += float64(cur.Y)
				}
				to := f32Point{cur.X, float32(y)}
				sub.Segments = append(sub.Segments, svgSegment{To: to})
				cur = to
				haveC, haveQ = false, false
				if !moreNumbers(toks, 1) {
					break
				}
			}
		case 'c':
			for {
				v, err := pop(6)
				if err != nil {
					return nil, err
				}
				cubic(rel, v[0], v[1], v[2], v[3], v[4], v[5])
				if !moreNumbers(toks, 6) {
					break
				}
			}
		case 's':
			for {
				v, err := pop(4)
				if err != nil {
					return nil, err
				}
				c0 := cur
				if haveC {
					c0 = f32Point{2*cur.X - lastC.X, 2*cur.Y - lastC.Y}
				}
				c1 := pt(rel, v[0], v[1])
				end := pt(rel, v[2], v[3])
				sub.Segments = append(sub.Segments, svgSegment{C0: c0, C1: c1, End: end, IsCubic: true})
				cur, lastC, haveC = end, c1, true
				haveQ = false
				if !moreNumbers(toks, 4) {
					break
				}
			}
		case 'q':
			for {
				v, err := pop(4)
				if err != nil {
					return nil, err
				}
				// Degree elevation: quadratic to cubic.
				q1 := pt(rel, v[0], v[1])
				end := pt(rel, v[2], v[3])
				c0 := f32Point{cur.X + (q1.X-cur.X)*2/3, cur.Y + (q1.Y-cur.Y)*2/3}
				c1 := f32Point{end.X + (q1.X-end.X)*2/3, end.Y + (q1.Y-end.Y)*2/3}
				sub.Segments = append(sub.Segments, svgSegment{C0: c0, C1: c1, End: end, IsCubic: true})
				cur, lastQ, haveQ = end, q1, true
				haveC = false
				if !moreNumbers(toks, 4) {
					break
				}
			}
		case 't':
			for {
				v, err := pop(2)
				if err != nil {
					return nil, err
				}
				if !haveQ {
					return nil, fmt.Errorf("t with no preceding q")
				}
				q1 := f32Point{2*cur.X - lastQ.X, 2*cur.Y - lastQ.Y}
				end := pt(rel, v[0], v[1])
				c0 := f32Point{cur.X + (q1.X-cur.X)*2/3, cur.Y + (q1.Y-cur.Y)*2/3}
				c1 := f32Point{end.X + (q1.X-end.X)*2/3, end.Y + (q1.Y-end.Y)*2/3}
				sub.Segments = append(sub.Segments, svgSegment{C0: c0, C1: c1, End: end, IsCubic: true})
				cur, lastQ = end, q1
				haveC = false
				if !moreNumbers(toks, 2) {
					break
				}
			}
		case 'a':
			for {
				v, err := pop(7)
				if err != nil {
					return nil, err
				}
				end := pt(rel, v[5], v[6])
				cubics, err := arcToCubics(cur, v[0], v[1], v[2], v[3] != 0, v[4] != 0, end)
				if err != nil {
					return nil, err
				}
				sub.Segments = append(sub.Segments, cubics...)
				cur = end
				haveC, haveQ = false, false
				if !moreNumbers(toks, 7) {
					break
				}
			}
		case 'z':
			if sub == nil {
				return nil, fmt.Errorf("z with no preceding m")
			}
			sub.Closed = true
			if cur != sub.Move {
				sub.Segments = append(sub.Segments, svgSegment{To: sub.Move})
			}
			cur = sub.Move
			haveC, haveQ = false, false
		default:
			return nil, fmt.Errorf("unknown path command %q", cmd)
		}
	}
	return p, nil
}

// arcToCubics converts one SVG elliptical arc to one or two (or more, for
// wide arcs) cubics, using the endpoint-to-center parameterization of the
// SVG specification appendix.
func arcToCubics(from f32Point, rx, ry, xrotDeg float64, large, sweep bool, to f32Point) ([]svgSegment, error) {
	if rx == 0 || ry == 0 {
		return []svgSegment{{To: to}}, nil
	}
	phi := xrotDeg * math.Pi / 180
	cosPhi, sinPhi := math.Cos(phi), math.Sin(phi)
	// Step 1: into the rotated frame centered between the endpoints.
	dx, dy := float64(from.X-to.X)/2, float64(from.Y-to.Y)/2
	x1 := cosPhi*dx + sinPhi*dy
	y1 := -sinPhi*dx + cosPhi*dy
	// Radii too small for the endpoints are scaled up per the spec.
	lambda := x1*x1/(rx*rx) + y1*y1/(ry*ry)
	if lambda > 1 {
		s := math.Sqrt(lambda)
		rx, ry = rx*s, ry*s
	}
	// Step 2: the center, again in the rotated frame.
	num := rx*rx*ry*ry - rx*rx*y1*y1 - ry*ry*x1*x1
	if num < 0 {
		num = 0
	}
	den := rx*rx*y1*y1 + ry*ry*x1*x1
	co := math.Sqrt(num / den)
	if large == sweep {
		co = -co
	}
	cx1 := co * rx * y1 / ry
	cy1 := -co * ry * x1 / rx
	cx := cosPhi*cx1 - sinPhi*cy1 + float64(from.X+to.X)/2
	cy := sinPhi*cx1 + cosPhi*cy1 + float64(from.Y+to.Y)/2
	// Step 3: start and sweep angles.
	angle := func(ux, uy, vx, vy float64) float64 {
		dot, l := ux*vx+uy*vy, math.Hypot(ux, uy)*math.Hypot(vx, vy)
		a := math.Acos(clampF(dot/l, -1, 1))
		if ux*vy-uy*vx < 0 {
			return -a
		}
		return a
	}
	ux, uy := (x1-cx1)/rx, (y1-cy1)/ry
	vx, vy := (-x1-cx1)/rx, (-y1-cy1)/ry
	theta1 := angle(1, 0, ux, uy)
	dTheta := angle(ux, uy, vx, vy)
	if !sweep && dTheta > 0 {
		dTheta -= 2 * math.Pi
	} else if sweep && dTheta < 0 {
		dTheta += 2 * math.Pi
	}
	// Split arcs wider than 90 degrees so each cubic stays accurate.
	n := int(math.Ceil(math.Abs(dTheta) / (math.Pi / 2)))
	segs := make([]svgSegment, 0, n)
	dt := dTheta / float64(n)
	k := (4.0 / 3) * math.Tan(dt/4) // the standard cubic-arc control factor
	t := theta1
	for i := 0; i < n; i++ {
		t2 := t + dt
		p0 := arcPoint(cx, cy, rx, ry, phi, t)
		p1 := arcPoint(cx, cy, rx, ry, phi, t2)
		d0x, d0y := arcDeriv(rx, ry, phi, t)
		d1x, d1y := arcDeriv(rx, ry, phi, t2)
		segs = append(segs, svgSegment{
			C0:      f32Point{p0.X + float32(k*d0x), p0.Y + float32(k*d0y)},
			C1:      f32Point{p1.X - float32(k*d1x), p1.Y - float32(k*d1y)},
			End:     p1,
			IsCubic: true,
		})
		t = t2
	}
	return segs, nil
}

// arcPoint evaluates the ellipse (center cx,cy, radii rx,ry, rotated phi) at
// parameter t.
func arcPoint(cx, cy, rx, ry, phi, t float64) f32Point {
	cosT, sinT := math.Cos(t), math.Sin(t)
	px := cx + rx*cosT*math.Cos(phi) - ry*sinT*math.Sin(phi)
	py := cy + rx*cosT*math.Sin(phi) + ry*sinT*math.Cos(phi)
	return f32Point{float32(px), float32(py)}
}

// arcDeriv is the derivative of the same ellipse at t.
func arcDeriv(rx, ry, phi, t float64) (float64, float64) {
	cosT, sinT := math.Cos(t), math.Sin(t)
	return -rx*sinT*math.Cos(phi) - ry*cosT*math.Sin(phi),
		-rx*sinT*math.Sin(phi) + ry*cosT*math.Cos(phi)
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// tokenizePath splits path data into commands and numbers, permissive about
// whitespace, and splits at minus signs and second decimal points the way
// SVG data relies on ("1.5.5" is 1.5 0.5).
func tokenizePath(d string) []string {
	var toks []string
	i := 0
	for i < len(d) {
		c := d[i]
		switch {
		case c == ' ' || c == ',' || c == '\n' || c == '\r' || c == '\t':
			i++
		case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			toks = append(toks, string(c))
			i++
		default:
			j := i
			startedDot := false
			for j < len(d) {
				ch := d[j]
				switch {
				case ch >= '0' && ch <= '9':
					j++
				case ch == '.':
					if startedDot {
						goto done
					}
					startedDot = true
					j++
				case ch == '-' || ch == '+':
					if j == i {
						j++ // leading sign belongs to this number
						continue
					}
					if d[j-1] == 'e' || d[j-1] == 'E' {
						j++ // exponent sign
						continue
					}
					goto done
				case ch == 'e' || ch == 'E':
					j++
				default:
					goto done
				}
			}
		done:
			toks = append(toks, d[i:j])
			i = j
		}
	}
	return toks
}

// moreNumbers reports whether implicit command repetition continues.
func moreNumbers(toks []string, n int) bool {
	if len(toks) < n {
		return false
	}
	c := toks[0][0]
	return c >= '0' && c <= '9' || c == '-' || c == '+' || c == '.'
}
