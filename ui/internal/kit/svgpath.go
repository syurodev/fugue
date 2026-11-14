package kit

import (
	"errors"
	"fmt"
	"strconv"

	"gioui.org/f32"
)

// iconSubpath is one closed-or-open run of segments of an icon path, on the
// 24 dp Lucide grid.
type iconSubpath struct {
	Move   f32.Point
	Segs   []iconSeg
	Closed bool
}

// iconSeg is one line or cubic segment; exactly one of the two shapes is
// used. The generated icon data (ui/internal/kit/icondata) contains nothing
// else: the generator converts every arc and quadratic of the original
// Lucide path data to cubics, and emits absolute commands only.
type iconSeg struct {
	To          f32.Point // line
	C0, C1, End f32.Point // cubic
	Cubic       bool
}

// The parse errors of generated icon path data, wrapped with context.
var (
	errIconEmpty   = errors.New("empty path data")
	errIconCommand = errors.New("bad path command")
	errIconNumber  = errors.New("bad number")
	errIconShort   = errors.New("path data ended early")
	errIconOrphan  = errors.New("command with no preceding M")
)

// iconParser parses the path data of one icon: the absolute M, L, C and Z
// commands the generator emits, nothing else. Anything else is a broken
// generated file and fails loudly.
type iconParser struct {
	toks    []string
	out     []iconSubpath
	cur     f32.Point
	haveCur bool
}

// parseIconPath parses one path-data string into subpaths.
func parseIconPath(d string) ([]iconSubpath, error) {
	var p iconParser
	p.toks = tokenizeIconPath(d)
	if len(p.toks) == 0 {
		return nil, errIconEmpty
	}
	for len(p.toks) > 0 {
		if err := p.command(); err != nil {
			return nil, err
		}
	}
	return p.out, nil
}

// command reads one command and its arguments.
func (p *iconParser) command() error {
	switch p.toks[0] {
	case "M":
		return p.move()
	case "L":
		return p.lineTo()
	case "C":
		return p.cubeto()
	case "Z":
		return p.close()
	default:
		return fmt.Errorf("%w: %q", errIconCommand, p.toks[0])
	}
}

// numbers reads n numbers starting after the current token and advances.
func (p *iconParser) numbers(n int) ([]float32, error) {
	if len(p.toks) < n+1 {
		return nil, fmt.Errorf("%w: want %d, have %d", errIconShort, n, len(p.toks)-1)
	}
	vals := make([]float32, n)
	for i := range vals {
		v, err := strconv.ParseFloat(p.toks[i+1], 32)
		if err != nil {
			return nil, fmt.Errorf("%w: %q", errIconNumber, p.toks[i+1])
		}
		vals[i] = float32(v)
	}
	p.toks = p.toks[n+1:]
	return vals, nil
}

// move handles M: start a subpath at a new point.
func (p *iconParser) move() error {
	v, err := p.numbers(2)
	if err != nil {
		return err
	}
	p.out = append(p.out, iconSubpath{Move: f32.Pt(v[0], v[1])})
	p.cur, p.haveCur = p.out[len(p.out)-1].Move, true
	return nil
}

// lineTo handles L: a line from the cursor.
func (p *iconParser) lineTo() error {
	v, err := p.numbers(2)
	if err != nil {
		return err
	}
	if !p.haveCur {
		return fmt.Errorf("%w: L", errIconOrphan)
	}
	p.cur = f32.Pt(v[0], v[1])
	last := &p.out[len(p.out)-1]
	last.Segs = append(last.Segs, iconSeg{To: p.cur})
	return nil
}

// cubeto handles C: a cubic from the cursor.
func (p *iconParser) cubeto() error {
	v, err := p.numbers(6)
	if err != nil {
		return err
	}
	if !p.haveCur {
		return fmt.Errorf("%w: C", errIconOrphan)
	}
	p.cur = f32.Pt(v[4], v[5])
	last := &p.out[len(p.out)-1]
	last.Segs = append(last.Segs, iconSeg{
		C0: f32.Pt(v[0], v[1]), C1: f32.Pt(v[2], v[3]), End: p.cur, Cubic: true,
	})
	return nil
}

// close handles Z: back to the subpath's move point.
func (p *iconParser) close() error {
	if !p.haveCur {
		return fmt.Errorf("%w: Z", errIconOrphan)
	}
	last := &p.out[len(p.out)-1]
	last.Closed = true
	if p.cur != last.Move {
		last.Segs = append(last.Segs, iconSeg{To: last.Move})
	}
	p.cur = last.Move
	return nil
}

// isIconSep reports whether c ends a number run of generated path data.
func isIconSep(c byte) bool {
	return c == ' ' || (c >= 'A' && c <= 'Z')
}

// tokenizeIconPath splits generated path data into command letters and
// numbers. The generator always separates two numbers with a space and
// touches a number only on the left of a command letter, so a run of
// non-letter characters is exactly one number; anything else fails the
// number parse downstream.
func tokenizeIconPath(d string) []string {
	var toks []string
	for i := 0; i < len(d); {
		c := d[i]
		if c == ' ' {
			i++
			continue
		}
		if isIconSep(c) {
			toks = append(toks, d[i:i+1])
			i++
			continue
		}
		j := i
		for j < len(d) && !isIconSep(d[j]) {
			j++
		}
		toks = append(toks, d[i:j])
		i = j
	}
	return toks
}
