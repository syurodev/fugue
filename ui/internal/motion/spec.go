package motion

import (
	"math"
	"time"
)

// Spec describes a spring the way designers tune one: Response is the period
// of the undamped oscillation, roughly how long the motion takes, and
// DampingFraction is 1 for no bounce and below 1 for a bounce.
type Spec struct {
	Response        time.Duration
	DampingFraction float32
}

// Stiffness is the spring constant per unit mass of the spec.
func (sp Spec) Stiffness() float32 {
	w := 2 * math.Pi / sp.Response.Seconds()
	return float32(w * w)
}

// Damping is the damping coefficient per unit mass of the spec.
func (sp Spec) Damping() float32 {
	return float32(4*math.Pi/sp.Response.Seconds()) * sp.DampingFraction
}

// The app's motion tokens. Every animation uses one of these so the whole
// app moves with one feel; tune them here, never per widget.

// Snappy is for direct responses to input: presses, menus opening, toggles.
func Snappy() Spec { return Spec{Response: 300 * time.Millisecond, DampingFraction: 0.86} }

// Smooth is for content moving or resizing: lists, sections, selections.
func Smooth() Spec { return Spec{Response: 450 * time.Millisecond, DampingFraction: 1} }

// Bouncy is for small playful elements such as a switch knob.
func Bouncy() Spec { return Spec{Response: 400 * time.Millisecond, DampingFraction: 0.68} }

// Exit is for things leaving the screen: quicker than they arrived, no bounce.
func Exit() Spec { return Spec{Response: 200 * time.Millisecond, DampingFraction: 1} }

// NewSpringSpec returns a spring at rest on value tuned by spec.
func NewSpringSpec(value float32, spec Spec) Spring {
	return Spring{Stiffness: spec.Stiffness(), Damping: spec.Damping(), Value: value, Velocity: 0, Target: value}
}

// Use retunes the spring to spec without touching its value or velocity, so
// an animation interrupted half way continues smoothly with the new feel.
func (s *Spring) Use(spec Spec) {
	s.Stiffness, s.Damping = spec.Stiffness(), spec.Damping()
}
