package motion

import "time"

// Slots animates the items of a list to their new places when the list
// changes (the FLIP technique: first, last, invert, play). Each frame the
// caller places every item at its laid-out position; an item whose position
// moved since the last frame is drawn where it was and springs to the new
// place, so inserting or removing an item slides its neighbours instead of
// making them jump. New items appear in place; their entrance is the
// caller's Presence.
type Slots[K comparable] struct {
	spec  Spec
	items map[K]*slot
	frame uint64
}

type slot struct {
	pos    float32
	offset Spring
	seen   uint64
}

// NewSlots returns an empty set of slots tuned by spec.
func NewSlots[K comparable](spec Spec) Slots[K] {
	return Slots[K]{spec: spec, items: make(map[K]*slot), frame: 0}
}

// Begin starts a frame.
func (s *Slots[K]) Begin() { s.frame++ }

// Place records that key is laid out at pos this frame, advances its spring
// by dt and returns where to draw it.
func (s *Slots[K]) Place(key K, pos float32, dt time.Duration) float32 {
	it, ok := s.items[key]
	if !ok {
		it = &slot{pos: pos, offset: NewSpringSpec(0, s.spec), seen: s.frame}
		s.items[key] = it
		return pos
	}
	if pos != it.pos {
		// Keep the item where it is on screen, then let it travel.
		it.offset.Value += it.pos - pos
		it.pos = pos
	}
	it.seen = s.frame
	it.offset.Advance(dt)
	return pos + it.offset.Value
}

// End forgets items that were not placed this frame and reports whether any
// item is still travelling.
func (s *Slots[K]) End() bool {
	moving := false
	for k, it := range s.items {
		if it.seen != s.frame {
			delete(s.items, k)
			continue
		}
		moving = moving || !it.offset.AtRest()
	}
	return moving
}
