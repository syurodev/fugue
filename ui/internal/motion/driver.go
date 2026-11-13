package motion

import "time"

// maxFrameStep caps the time one frame may advance animations: after the
// window was idle or stalled, motion resumes smoothly instead of jumping.
const maxFrameStep = time.Second / 20

// Driver tracks frame time and whether any animation still needs frames.
// Call Begin at the start of a frame, Keep from every animation that has not
// finished, and Next at the end to learn when the next frame is needed.
type Driver struct {
	last   time.Time
	active bool
	wakeAt time.Time
}

// Begin starts a frame at now and returns the time step to advance springs
// by, capped to maxFrameStep. The first frame advances by zero.
func (d *Driver) Begin(now time.Time) time.Duration {
	dt := time.Duration(0)
	if !d.last.IsZero() {
		dt = min(max(now.Sub(d.last), 0), maxFrameStep)
	}
	d.last = now
	d.active = false
	d.wakeAt = time.Time{}
	return dt
}

// Keep marks that something is still moving and needs the next frame.
func (d *Driver) Keep() { d.active = true }

// WakeAt asks for a frame at t even when nothing moves now, for a scheduled
// change such as a scripted step. The earliest request wins.
func (d *Driver) WakeAt(t time.Time) {
	if d.wakeAt.IsZero() || t.Before(d.wakeAt) {
		d.wakeAt = t
	}
}

// Next reports whether another frame is needed and when: immediately (zero
// time, true) while something moves, at the earliest wake time otherwise, and
// never (false) when the scene is idle.
func (d *Driver) Next() (time.Time, bool) {
	if d.active {
		return time.Time{}, true
	}
	if !d.wakeAt.IsZero() {
		return d.wakeAt, true
	}
	return time.Time{}, false
}
