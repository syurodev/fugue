package motion

import "time"

// step is the fixed integration step: springs are integrated in steps of at
// most this length so a long frame cannot make them explode.
const step = time.Second / 240

// Settle thresholds: a spring is at rest when both its distance to the target
// and its speed are below these, in the spring's own units.
const (
	restDistance = 0.001
	restSpeed    = 0.01
	relativeRest = 1e-6
)

// Spring is a damped spring moving Value toward Target. Stiffness and
// Damping are per unit mass; build one from a Spec with NewSpringSpec.
type Spring struct {
	Stiffness float32
	Damping   float32
	Value     float32
	Velocity  float32
	Target    float32
}

// Advance integrates the spring over dt with semi-implicit Euler in fixed
// steps. It snaps to the target once at rest.
func (s *Spring) Advance(dt time.Duration) {
	for dt > 0 {
		h := min(dt, step)
		dt -= h
		sec := float32(h.Seconds())
		accel := -s.Stiffness*(s.Value-s.Target) - s.Damping*s.Velocity
		s.Velocity += accel * sec
		s.Value += s.Velocity * sec
	}
	if s.AtRest() {
		s.Value, s.Velocity = s.Target, 0
	}
}

// AtRest reports whether the spring has settled on its target. Both
// thresholds grow with the target's magnitude: a float32 near 16000 cannot
// resolve 0.001, so a spring following such a value stalls a few thousandths
// short with a small residual speed and would otherwise never rest.
func (s *Spring) AtRest() bool {
	scale := abs(s.Target) * relativeRest
	return abs(s.Value-s.Target) < max(restDistance, scale) && abs(s.Velocity) < max(restSpeed, 10*scale)
}

// Snap puts the spring at rest on v.
func (s *Spring) Snap(v float32) {
	s.Value, s.Velocity, s.Target = v, 0, v
}

func abs(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
