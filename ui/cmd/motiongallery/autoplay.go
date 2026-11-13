package main

import "time"

// autoplayLoop is the length of the scripted demo.
const autoplayLoop = 9 * time.Second

// cue is one scripted action.
type cue struct {
	at time.Duration
	do func(g *gallery)
}

// autoplay drives the demos without input, for headless frames, the
// benchmark and a hands-off window. Between cues nothing is scheduled but
// the next cue, so a settled gallery draws no frames.
type autoplay struct {
	start time.Time
	last  time.Duration
	cues  []cue
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func newAutoplay() *autoplay {
	return &autoplay{start: time.Time{}, last: -1, cues: []cue{
		{ms(400), func(g *gallery) { g.menu.open.Show(true) }},
		{ms(800), func(g *gallery) { g.menu.forced = 1 }},
		{ms(1050), func(g *gallery) { g.menu.forced = 3 }},
		{ms(1400), func(g *gallery) { g.menu.forced = -1; g.menu.choose(3) }},
		{ms(2000), func(g *gallery) { g.toggle.on = true }},
		{ms(2400), func(g *gallery) { g.seg.selected = 0 }},
		{ms(2800), func(g *gallery) { g.section.open = true }},
		{ms(3300), func(g *gallery) { g.feed.add(g.now) }},
		{ms(3600), func(g *gallery) { g.run.forced = true }},
		{ms(3750), func(g *gallery) { g.run.forced = false }},
		{ms(4200), func(g *gallery) { g.feed.add(g.now) }},
		{ms(4900), func(g *gallery) { g.section.open = false }},
		{ms(5300), func(g *gallery) { g.toggle.on = false }},
		{ms(5600), func(g *gallery) { g.seg.selected = 2 }},
		// Interrupt the menu half way through opening: it must turn round.
		{ms(6000), func(g *gallery) { g.menu.open.Show(true) }},
		{ms(6120), func(g *gallery) { g.menu.open.Show(false) }},
		{ms(6600), func(g *gallery) { g.feed.add(g.now) }},
	}}
}

// step fires the cues due since the last frame and schedules the next one.
func (a *autoplay) step(g *gallery) {
	if a.start.IsZero() || g.now.Sub(a.start) >= autoplayLoop {
		a.start, a.last = g.now, -1
	}
	elapsed := g.now.Sub(a.start)
	next := autoplayLoop
	for _, c := range a.cues {
		if c.at > a.last && c.at <= elapsed {
			c.do(g)
		}
		if c.at > elapsed && c.at < next {
			next = c.at
		}
	}
	a.last = elapsed
	g.driver.WakeAt(a.start.Add(next))
}
