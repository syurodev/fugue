package motion

import "time"

// visibleFloor is the progress below which a leaving element is gone.
const visibleFloor = 0.001

// Presence animates an element in and out. Progress runs from 0 (absent) to
// 1 (present) on a spring, so toggling it half way reverses from where it is
// with its current velocity instead of restarting. Entering and leaving may
// use different specs: things usually leave faster than they arrive.
type Presence struct {
	spring Spring
	in     Spec
	out    Spec
	shown  bool
}

// NewPresence returns an absent element that enters with in and leaves with
// out.
func NewPresence(in, out Spec) Presence {
	return Presence{spring: NewSpringSpec(0, out), in: in, out: out, shown: false}
}

// Show asks the element to enter (true) or leave (false).
func (p *Presence) Show(v bool) {
	if v == p.shown {
		return
	}
	p.shown = v
	if v {
		p.spring.Use(p.in)
		p.spring.Target = 1
		return
	}
	p.spring.Use(p.out)
	p.spring.Target = 0
}

// Shown reports whether the element was last asked to be present.
func (p *Presence) Shown() bool { return p.shown }

// Advance moves the animation by dt and reports whether it is still moving.
func (p *Presence) Advance(dt time.Duration) bool {
	p.spring.Advance(dt)
	return !p.spring.AtRest()
}

// Progress is how present the element is; a bouncy spec may overshoot 1.
func (p *Presence) Progress() float32 { return p.spring.Value }

// Visible reports whether the element must still be drawn: present, or not
// yet faded out.
func (p *Presence) Visible() bool {
	return p.shown || p.spring.Value > visibleFloor
}

// Follower is a value that springs towards whatever it is told to follow,
// such as a measured height or the position of a selection. The first value
// it is given is taken as is, so nothing animates in from zero on the first
// frame.
type Follower struct {
	spring Spring
	ready  bool
}

// NewFollower returns a follower tuned by spec.
func NewFollower(spec Spec) Follower {
	return Follower{spring: NewSpringSpec(0, spec), ready: false}
}

// Follow sets the target, advances by dt and returns the value to draw.
func (f *Follower) Follow(target float32, dt time.Duration) float32 {
	if !f.ready {
		f.ready = true
		f.spring.Snap(target)
		return target
	}
	f.spring.Target = target
	f.spring.Advance(dt)
	return f.spring.Value
}

// Moving reports whether the value has not settled yet.
func (f *Follower) Moving() bool { return !f.spring.AtRest() }

// Value is the current value without advancing.
func (f *Follower) Value() float32 { return f.spring.Value }
