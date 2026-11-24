package appwin

import (
	"image"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/motion"
)

// startAnim arms a Smooth slide of refKey from from to wherever it lays out
// next (§5.3, §3.7).
func (g *slotGrid) startAnim(refKey string, from image.Rectangle) {
	if from.Empty() {
		return
	}
	g.anims[refKey] = &slotAnim{from: from, spring: motion.NewSpringSpec(0, motion.Smooth())}
	g.anims[refKey].spring.Target = 1
}

func (g *slotGrid) startClosing(ref SlotRef, rect image.Rectangle) {
	p := motion.NewPresence(motion.Smooth(), motion.Exit())
	p.Snap(true)
	p.Show(false)
	g.closing = append(g.closing, &closingSlot{ref: ref, rect: rect, presence: p})
}

// advanceAnims steps every animation and drops the finished ones.
func (g *slotGrid) advanceAnims(env *kit.Env) {
	if env.ReducedMotion {
		clear(g.anims)
		g.closing = nil
		return
	}
	dt := env.DT()
	for key, anim := range g.anims {
		anim.spring.Advance(dt)
		if anim.spring.AtRest() {
			delete(g.anims, key)
			continue
		}
		env.Keep()
	}
	kept := g.closing[:0]
	for _, cs := range g.closing {
		if cs.presence.Advance(dt) {
			env.Keep()
		}
		if cs.presence.Visible() {
			kept = append(kept, cs)
		}
	}
	g.closing = kept
}

// animOf returns the in-flight animation of a ref, or nil.
func (g *slotGrid) animOf(ref SlotRef) *slotAnim {
	return g.anims[keyOfRef(ref)]
}

// keyOfRef names a ref for the animation table: kind and id, the identity a
// moving slot keeps.
func keyOfRef(ref SlotRef) string { return string(ref.Kind) + ":" + ref.ID }
