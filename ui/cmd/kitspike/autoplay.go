package main

import (
	"time"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kitspike"
)

// autoplay drives the interactive window through a short script so the
// menu's open/close and the focus ring can be watched live. The screenshots
// use the router-driven script in headless.go instead; this one exists for
// `-autoplay`.
type autoplay struct {
	env  *kitspike.Env
	pg   *pages
	cues []cue
	next int
}

// cue is one scripted moment.
type cue struct {
	at   time.Duration
	step func(a *autoplay, gtx *layout.Context)
}

func newAutoplay(env *kitspike.Env, pg *pages) *autoplay {
	a := &autoplay{env: env, pg: pg}
	a.cues = []cue{
		{at: ms(0), step: func(a *autoplay, _ *layout.Context) {
			a.pg.cur = pageMenu
		}},
		{at: ms(600), step: func(a *autoplay, _ *layout.Context) {
			// Highlight the trigger so the focus ring is visible, then open
			// the menu through the overlay (the same call the click makes).
			a.pg.env.SimFocus = a.pg.menu.trigger.Tag()
			a.pg.overlay.Open(a.pg.menu.items, a.pg.menu.anchorForShots(), a.pg.menu.trigger.Tag())
		}},
		{at: ms(2200), step: func(a *autoplay, _ *layout.Context) {
			a.pg.env.SimFocus = nil
			a.pg.overlay.CloseAll()
		}},
		{at: ms(4200), step: func(a *autoplay, _ *layout.Context) {
			a.pg.cur = pageSplit
		}},
	}
	return a
}

// step runs the cues due at or before now.
func (a *autoplay) step(gtx *layout.Context) {
	for a.next < len(a.cues) && a.cues[a.next].at <= a.env.Now().Sub(startEpoch) {
		a.cues[a.next].step(a, gtx)
		a.next++
	}
}

func ms(v int) time.Duration { return time.Duration(v) * time.Millisecond }

// startEpoch is the fake clock's origin, matching the headless rig.
var startEpoch = time.Unix(1_700_000_000, 0)
