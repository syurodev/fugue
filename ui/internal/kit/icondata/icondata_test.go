package icondata

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
)

// rawIcons embeds the original Lucide path data the constants are generated
// from, with the ISC license alongside.
//
//go:embed testdata/*.txt
var rawIcons embed.FS

// TestDataIsCurrent is the generator committed with its output: it converts
// the original Lucide path data in testdata (arcs and quadratics to cubics)
// and checks icondata.go against the result. After adding or changing an
// icon, rewrite icondata.go with the regenerate-tagged companion of this
// test:
//
//	go test -tags regenerate ./internal/kit/icondata -run TestRegenerateIconData
func TestDataIsCurrent(t *testing.T) {
	t.Parallel()
	const path = "icondata.go"
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if want := composeFile(t); string(got) != want {
		t.Fatalf("%s is stale against testdata; regenerate with:\n\tgo test -tags regenerate ./internal/kit/icondata -run TestRegenerateIconData", path)
	}
}

// composeFile converts every packaged icon and composes the whole content
// of icondata.go from it.
func composeFile(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(`// Code generated from the Lucide path data in testdata (ISC license at
// LICENSE-lucide.txt); DO NOT EDIT. Regenerate with:
//
//	go test -tags regenerate ./internal/kit/icondata -run TestRegenerateIconData

package icondata

// Check is the "check" icon; the constants below are the packaged set.
const (
	Check = "` + convertIcon(t, "check") + `"

`)
	for _, ic := range packagedIcons()[1:] {
		b.WriteString("\t// " + ic.name + " is the \"" + ic.file + "\" icon.\n")
		b.WriteString("\t" + ic.name + " = \"" + convertIcon(t, ic.file) + "\"\n")
	}
	b.WriteString(")\n")
	return b.String()
}

// packagedIcon is one packaged icon: testdata file base name and constant
// name, in the order of the constants file. Keep it in step with
// kit.IconName.
type packagedIcon struct {
	file string
	name string
}

// packagedIcons lists the packaged set.
func packagedIcons() []packagedIcon {
	return []packagedIcon{
		{"check", "Check"},
		{"chevron-down", "ChevronDown"},
		{"chevron-left", "ChevronLeft"},
		{"chevron-right", "ChevronRight"},
		{"search", "Search"},
		{"x", "X"},
		{"eye", "Eye"},
		{"eye-off", "EyeOff"},
		{"copy", "Copy"},
		{"rotate-ccw", "RotateCCW"},
		{"circle-help", "CircleHelp"},
		{"pin", "Pin"},
		{"plus", "Plus"},
		{"ellipsis", "Ellipsis"},
		{"terminal", "Terminal"},
		{"pencil", "Pencil"},
		{"file-text", "FileText"},
		{"file-code", "FileCode"},
		{"globe", "Globe"},
		{"wrench", "Wrench"},
		{"git-commit", "GitCommit"},
		{"wrap-text", "WrapText"},
		{"folder", "Folder"},
		{"git-branch", "GitBranch"},
		{"git-pull-request", "GitPullRequest"},
		{"message-square", "MessageSquare"},
		{"users", "Users"},
		{"inbox", "Inbox"},
		{"circle-user", "CircleUser"},
		{"settings", "Settings"},
		{"key-round", "KeyRound"},
		{"bot", "Bot"},
		{"lock", "Lock"},
		{"arrow-left", "ArrowLeft"},
		{"arrow-right", "ArrowRight"},
		{"arrow-up", "ArrowUp"},
		{"arrow-down", "ArrowDown"},
		{"maximize-2", "Maximize2"},
		{"minimize-2", "Minimize2"},
		{"columns-2", "Columns2"},
		{"panel-left", "PanelLeft"},
		{"app-window", "WindowFrame"},
	}
}

// convertIcon reads one Lucide data file and returns its converted path
// data: one line per SVG <path> element, each starting its own coordinate
// space, so relative commands never carry across lines.
func convertIcon(t *testing.T, file string) string {
	t.Helper()
	raw, err := fs.ReadFile(rawIcons, "testdata/"+file+".txt")
	if err != nil {
		t.Fatalf("read the Lucide data of %s: %v", file, err)
	}
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var p rawParser
		if err := p.run(strings.TrimSpace(line)); err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		out.WriteString(encode(p.out))
	}
	if out.Len() == 0 {
		t.Fatalf("%s: no path data", file)
	}
	return out.String()
}

// rawSubpath is one run of segments starting at Move.
type rawSubpath struct {
	Move   rawPoint
	Segs   []rawSeg
	Closed bool
}

// rawSeg is one line or cubic; exactly one of the two is used. The parser
// converts every other SVG command (h, v, s, q, t, a) to one of these while
// parsing, so the encoder only ever emits M, L, C and Z.
type rawSeg struct {
	To      rawPoint
	C0, C1  rawPoint
	End     rawPoint
	IsCubic bool
}

// rawPoint is a coordinate pair on the 24 grid.
type rawPoint struct{ X, Y float32 }

// The parse errors of raw Lucide path data.
var (
	errRawEmpty   = errors.New("empty path data")
	errRawCommand = errors.New("bad path command")
	errRawShort   = errors.New("path data ended early")
	errRawNumber  = errors.New("bad number")
	errRawOrphan  = errors.New("command with no preceding m")
	errRawSmooth  = errors.New("t with no preceding q")
)

// rawParser parses the SVG path data Lucide publishes: M m L l H h V v C c
// S s Q q T t A a and Z z, with implicit repetition after any of them, as
// the SVG grammar allows. Arcs become cubics while parsing.
type rawParser struct {
	toks  []string
	out   []rawSubpath
	cur   rawPoint
	sub   *rawSubpath
	lastC rawPoint
	lastQ rawPoint
	haveC bool
	haveQ bool
}

// run parses one path-data string into subpaths.
func (p *rawParser) run(d string) error {
	p.toks = tokenizeRaw(d)
	if len(p.toks) == 0 {
		return errRawEmpty
	}
	for len(p.toks) > 0 {
		if err := p.command(); err != nil {
			return err
		}
	}
	return nil
}

// command reads one command letter and all its repetitions.
func (p *rawParser) command() error {
	cmd := p.toks[0]
	p.toks = p.toks[1:]
	if len(cmd) != 1 {
		return fmt.Errorf("%w: %q", errRawCommand, cmd)
	}
	rel := cmd[0] >= 'a' && cmd[0] <= 'z'
	switch c := cmd[0] | 0x20; c {
	case 'm':
		return p.move(rel)
	case 'l', 'h', 'v':
		return p.lines(c, rel)
	case 'c', 's':
		return p.cubics(c, rel)
	case 'q', 't':
		return p.quadratics(c, rel)
	case 'a':
		return p.arcs(rel)
	case 'z':
		return p.close()
	default:
		return fmt.Errorf("%w: %q", errRawCommand, cmd)
	}
}

// pop reads the next n numbers.
func (p *rawParser) pop(n int) ([]float64, error) {
	if len(p.toks) < n {
		return nil, fmt.Errorf("%w: want %d, have %d", errRawShort, n, len(p.toks))
	}
	vals := make([]float64, n)
	for i := range vals {
		v, err := strconv.ParseFloat(p.toks[i], 64)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", errRawNumber, p.toks[i])
		}
		vals[i] = v
	}
	p.toks = p.toks[n:]
	return vals, nil
}

// pt resolves one coordinate pair: relative commands offset the cursor,
// absolute ones name it directly.
func (p *rawParser) pt(rel bool, x, y float64) rawPoint {
	if rel {
		return rawPoint{p.cur.X + float32(x), p.cur.Y + float32(y)}
	}
	return rawPoint{float32(x), float32(y)}
}

// line appends a line segment and moves the cursor.
func (p *rawParser) line(rel bool, x, y float64) {
	to := p.pt(rel, x, y)
	p.sub.Segs = append(p.sub.Segs, rawSeg{To: to})
	p.cur = to
	p.haveC, p.haveQ = false, false
}

// cubic appends a cubic segment and moves the cursor.
func (p *rawParser) cubic(rel bool, x0, y0, x1, y1, x2, y2 float64) {
	c0, c1, end := p.pt(rel, x0, y0), p.pt(rel, x1, y1), p.pt(rel, x2, y2)
	p.sub.Segs = append(p.sub.Segs, rawSeg{C0: c0, C1: c1, End: end, IsCubic: true})
	p.cur, p.lastC, p.haveC = end, c1, true
	p.haveQ = false
}

// move handles M/m and the implicit line pairs that follow it; a leading m
// is absolute because there is no cursor yet.
func (p *rawParser) move(rel bool) error {
	v, err := p.pop(2)
	if err != nil {
		return err
	}
	to := p.pt(rel && p.sub != nil, v[0], v[1])
	p.out = append(p.out, rawSubpath{Move: to})
	p.sub = &p.out[len(p.out)-1]
	p.cur = to
	p.haveC, p.haveQ = false, false
	for moreRaw(p.toks, 2) {
		v, err := p.pop(2)
		if err != nil {
			return err
		}
		p.line(rel, v[0], v[1])
	}
	return nil
}

// lines handles L/l, H/h and V/v: L takes a pair, H and V one coordinate.
func (p *rawParser) lines(kind byte, rel bool) error {
	if p.sub == nil {
		return fmt.Errorf("%w: %c", errRawOrphan, kind)
	}
	arity := 1
	if kind == 'l' {
		arity = 2
	}
	for {
		v, err := p.pop(arity)
		if err != nil {
			return err
		}
		switch kind {
		case 'l':
			p.line(rel, v[0], v[1])
		case 'h':
			x := v[0]
			if rel {
				x += float64(p.cur.X)
			}
			p.sub.Segs = append(p.sub.Segs, rawSeg{To: rawPoint{float32(x), p.cur.Y}})
			p.cur = rawPoint{float32(x), p.cur.Y}
			p.haveC, p.haveQ = false, false
		case 'v':
			y := v[0]
			if rel {
				y += float64(p.cur.Y)
			}
			p.sub.Segs = append(p.sub.Segs, rawSeg{To: rawPoint{p.cur.X, float32(y)}})
			p.cur = rawPoint{p.cur.X, float32(y)}
			p.haveC, p.haveQ = false, false
		}
		if !moreRaw(p.toks, arity) {
			return nil
		}
	}
}

// cubics handles C/c and S/s; S reflects the last control point.
func (p *rawParser) cubics(kind byte, rel bool) error {
	if p.sub == nil {
		return fmt.Errorf("%w: %c", errRawOrphan, kind)
	}
	for {
		if kind == 'c' {
			if err := p.explicitCubic(rel); err != nil {
				return err
			}
		} else {
			if err := p.smoothCubic(rel); err != nil {
				return err
			}
		}
		if !moreRaw(p.toks, 6) && !moreRaw(p.toks, 4) {
			return nil
		}
	}
}

// explicitCubic reads one C/c repetition: all three points given.
func (p *rawParser) explicitCubic(rel bool) error {
	v, err := p.pop(6)
	if err != nil {
		return err
	}
	p.cubic(rel, v[0], v[1], v[2], v[3], v[4], v[5])
	return nil
}

// smoothCubic reads one S/s repetition: the first control point reflects
// the previous cubic's second one.
func (p *rawParser) smoothCubic(rel bool) error {
	v, err := p.pop(4)
	if err != nil {
		return err
	}
	c0 := p.cur
	if p.haveC {
		c0 = rawPoint{2*p.cur.X - p.lastC.X, 2*p.cur.Y - p.lastC.Y}
	}
	c1, end := p.pt(rel, v[0], v[1]), p.pt(rel, v[2], v[3])
	p.sub.Segs = append(p.sub.Segs, rawSeg{C0: c0, C1: c1, End: end, IsCubic: true})
	p.cur, p.lastC, p.haveC = end, c1, true
	p.haveQ = false
	return nil
}

// quadratics handles Q/q and T/t, elevating quadratics to cubics.
func (p *rawParser) quadratics(kind byte, rel bool) error {
	if p.sub == nil {
		return fmt.Errorf("%w: %c", errRawOrphan, kind)
	}
	for {
		v, err := p.pop(4)
		if err != nil {
			return err
		}
		var q1 rawPoint
		if kind == 'q' {
			q1 = p.pt(rel, v[0], v[1])
		} else {
			if !p.haveQ {
				return errRawSmooth
			}
			q1 = rawPoint{2*p.cur.X - p.lastQ.X, 2*p.cur.Y - p.lastQ.Y}
		}
		end := p.pt(rel, v[2], v[3])
		c0 := rawPoint{p.cur.X + (q1.X-p.cur.X)*2/3, p.cur.Y + (q1.Y-p.cur.Y)*2/3}
		c1 := rawPoint{end.X + (q1.X-end.X)*2/3, end.Y + (q1.Y-end.Y)*2/3}
		p.sub.Segs = append(p.sub.Segs, rawSeg{C0: c0, C1: c1, End: end, IsCubic: true})
		p.cur, p.lastQ, p.haveQ = end, q1, true
		p.haveC = false
		if !moreRaw(p.toks, 4) {
			return nil
		}
	}
}

// arcs handles A/a, converting every arc to cubics.
func (p *rawParser) arcs(rel bool) error {
	if p.sub == nil {
		return fmt.Errorf("%w: a", errRawOrphan)
	}
	for {
		v, err := p.pop(7)
		if err != nil {
			return err
		}
		end := p.pt(rel, v[5], v[6])
		p.sub.Segs = append(p.sub.Segs, arcToCubics(p.cur, v[0], v[1], v[2], v[3] != 0, v[4] != 0, end)...)
		p.cur = end
		p.haveC, p.haveQ = false, false
		if !moreRaw(p.toks, 7) {
			return nil
		}
	}
}

// close handles Z/z: back to the subpath's move point.
func (p *rawParser) close() error {
	if p.sub == nil {
		return errRawOrphan
	}
	p.sub.Closed = true
	if p.cur != p.sub.Move {
		p.sub.Segs = append(p.sub.Segs, rawSeg{To: p.sub.Move})
	}
	p.cur = p.sub.Move
	p.haveC, p.haveQ = false, false
	return nil
}

// moreRaw reports whether the next tokens continue the current command's
// argument list (implicit repetition).
func moreRaw(toks []string, n int) bool {
	if len(toks) < n {
		return false
	}
	_, err := strconv.ParseFloat(toks[0], 64)
	return err == nil
}

// arcToCubics converts one SVG elliptical arc to cubics with the
// endpoint-to-center parameterization of the SVG specification appendix,
// splitting arcs wider than 90 degrees so each cubic stays accurate.
func arcToCubics(from rawPoint, rx, ry, xrotDeg float64, large, sweep bool, to rawPoint) []rawSeg {
	if rx == 0 || ry == 0 {
		return []rawSeg{{To: to}}
	}
	phi := xrotDeg * math.Pi / 180
	cx, cy, theta1, dTheta := arcCenter(from, to, rx, ry, phi, large, sweep)
	n := int(math.Ceil(math.Abs(dTheta) / (math.Pi / 2)))
	segs := make([]rawSeg, 0, n)
	dt := dTheta / float64(n)
	k := (4.0 / 3) * math.Tan(dt/4) // the standard cubic-arc control factor
	t := theta1
	for range n {
		t2 := t + dt
		p0 := arcPoint(cx, cy, rx, ry, phi, t)
		p1 := arcPoint(cx, cy, rx, ry, phi, t2)
		d0x, d0y := arcDeriv(rx, ry, phi, t)
		d1x, d1y := arcDeriv(rx, ry, phi, t2)
		segs = append(segs, rawSeg{
			C0:      rawPoint{quantize(p0.X + float32(k*d0x)), quantize(p0.Y + float32(k*d0y))},
			C1:      rawPoint{quantize(p1.X - float32(k*d1x)), quantize(p1.Y - float32(k*d1y))},
			End:     p1,
			IsCubic: true,
		})
		t = t2
	}
	return segs
}

// quantize rounds a converted coordinate to the nearest thousandth of the
// 24 grid before it becomes the committed constant. The arc conversion
// calls the platform's transcendental functions, whose last ulp differs
// between macOS and Linux; rounding to a thousandth (an error of at most
// 0.0005 grid units, invisible at any icon size) makes the generator's
// output identical on every platform, so the committed file does not drift.
func quantize(v float32) float32 {
	return float32(math.Round(float64(v)*1000) / 1000)
}

// arcCenter finds the arc's center and start and sweep angles, scaling the
// radii up when they are too small for the endpoints (SVG spec, steps 1-3).
func arcCenter(from, to rawPoint, rx, ry, phi float64, large, sweep bool) (cx, cy, theta1, dTheta float64) {
	cosPhi, sinPhi := math.Cos(phi), math.Sin(phi)
	dx, dy := float64(from.X-to.X)/2, float64(from.Y-to.Y)/2
	x1 := float64(cosPhi*dx) + float64(sinPhi*dy)
	y1 := float64(-sinPhi*dx) + float64(cosPhi*dy)
	if lambda := float64(x1*x1/(rx*rx)) + float64(y1*y1/(ry*ry)); lambda > 1 {
		s := math.Sqrt(lambda)
		rx, ry = rx*s, ry*s
	}
	num := float64(rx*rx*ry*ry) - float64(rx*rx*y1*y1) - float64(ry*ry*x1*x1)
	if num < 0 {
		num = 0
	}
	co := math.Sqrt(num / (float64(rx*rx*y1*y1) + float64(ry*ry*x1*x1)))
	if large == sweep {
		co = -co
	}
	cx1 := co * rx * y1 / ry
	cy1 := -co * ry * x1 / rx
	cx = float64(cosPhi*cx1) - float64(sinPhi*cy1) + float64(from.X+to.X)/2
	cy = float64(sinPhi*cx1) + float64(cosPhi*cy1) + float64(from.Y+to.Y)/2
	angle := func(ux, uy, vx, vy float64) float64 {
		dot, l := float64(ux*vx)+float64(uy*vy), math.Hypot(ux, uy)*math.Hypot(vx, vy)
		a := math.Acos(min(max(dot/l, -1), 1))
		if float64(ux*vy)-float64(uy*vx) < 0 {
			return -a
		}
		return a
	}
	ux, uy := (x1-cx1)/rx, (y1-cy1)/ry
	vx, vy := (-x1-cx1)/rx, (-y1-cy1)/ry
	theta1 = angle(1, 0, ux, uy)
	dTheta = angle(ux, uy, vx, vy)
	if !sweep && dTheta > 0 {
		dTheta -= 2 * math.Pi
	} else if sweep && dTheta < 0 {
		dTheta += 2 * math.Pi
	}
	return cx, cy, theta1, dTheta
}

// arcPoint evaluates the ellipse (center cx,cy, radii rx,ry, rotated phi)
// at parameter t.
func arcPoint(cx, cy, rx, ry, phi, t float64) rawPoint {
	cosT, sinT := math.Cos(t), math.Sin(t)
	return rawPoint{
		X: quantize(float32(cx + float64(rx*cosT*math.Cos(phi)) - float64(ry*sinT*math.Sin(phi)))),
		Y: quantize(float32(cy + float64(rx*cosT*math.Sin(phi)) + float64(ry*sinT*math.Cos(phi)))),
	}
}

// arcDeriv is the derivative of the same ellipse at t.
func arcDeriv(rx, ry, phi, t float64) (float64, float64) {
	cosT, sinT := math.Cos(t), math.Sin(t)
	return float64(-rx*sinT*math.Cos(phi)) - float64(ry*cosT*math.Sin(phi)),
		float64(-rx*sinT*math.Sin(phi)) + float64(ry*cosT*math.Cos(phi))
}

// encode renders parsed subpaths as absolute M/L/C/Z path data, the
// shortest form that round-trips float32.
func encode(subs []rawSubpath) string {
	var b strings.Builder
	num := func(v float32) string {
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	}
	for _, sub := range subs {
		b.WriteString("M" + num(sub.Move.X) + " " + num(sub.Move.Y))
		for _, seg := range sub.Segs {
			if seg.IsCubic {
				b.WriteString("C" + num(seg.C0.X) + " " + num(seg.C0.Y) + " " +
					num(seg.C1.X) + " " + num(seg.C1.Y) + " " + num(seg.End.X) + " " + num(seg.End.Y))
			} else {
				b.WriteString("L" + num(seg.To.X) + " " + num(seg.To.Y))
			}
		}
		if sub.Closed {
			b.WriteString("Z")
		}
	}
	return b.String()
}

// isRawSpace reports whether c is whitespace or a comma, both separators in
// SVG path data.
func isRawSpace(c byte) bool {
	return c == ' ' || c == ',' || c == '\n' || c == '\r' || c == '\t'
}

// isRawLetter reports whether c is a command letter.
func isRawLetter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// isRawExp reports whether c can appear inside a number as an exponent
// marker or sign (SVG has no E command, so the letters are free).
func isRawExp(c byte) bool {
	return c == 'e' || c == 'E'
}

// tokenizeRaw splits SVG path data into command letters and numbers,
// splitting at minus signs and second decimal points the way SVG data
// relies on ("1.5.5" is 1.5 0.5; "1e-2" is one number).
func tokenizeRaw(d string) []string {
	var toks []string
	for i := 0; i < len(d); {
		c := d[i]
		switch {
		case isRawSpace(c):
			i++
		case isRawLetter(c):
			toks = append(toks, string(c))
			i++
		default:
			j := numberEnd(d, i)
			if j == i {
				return nil // a number that never started
			}
			toks = append(toks, d[i:j])
			i = j
		}
	}
	return toks
}

// numberEnd returns the index after the number starting at i, stopping at
// the first character a number cannot contain.
func numberEnd(d string, i int) int {
	dot := false
	for j := i; j < len(d); j++ {
		ch := d[j]
		switch {
		case isRawDigit(ch):
		case ch == '.' && !dot:
			dot = true
		case ch == '-' || ch == '+' || isRawExp(ch):
			if signEndsNumber(d, j, i) {
				return j
			}
		default:
			return j
		}
	}
	return len(d)
}

// isRawDigit reports whether c is a decimal digit.
func isRawDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// signEndsNumber reports whether the sign at j ends the number that started
// at i: any sign that neither opens the number nor follows an exponent
// marker starts a new one.
func signEndsNumber(d string, j, i int) bool {
	if d[j] == 'e' || d[j] == 'E' {
		return false
	}
	return j != i && !isRawExp(d[j-1])
}
