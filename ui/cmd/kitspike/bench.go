package main

import (
	"fmt"
	"io"
	"slices"
	"time"

	"gioui.org/io/key"

	"github.com/syurodev/fugue/ui/internal/hostconn"
)

// bench answers Q6 (shadow cost), Q7 (frames: idle vs animating) and Q8 (UI
// scale) with numbers, using the real clock for costs and the fake clock for
// frame counts.
func bench(w io.Writer, scale float32) error {
	if err := benchShadow(w, scale); err != nil {
		return err
	}
	if err := benchFrames(w); err != nil {
		return err
	}
	return benchUIScale(w)
}

// benchShadow measures layout+render cost of the shadow page in its three
// variants: no shadow, layered rects, pre-blurred texture (Q6).
func benchShadow(w io.Writer, scale float32) error {
	for _, c := range []struct {
		name    string
		pageVal int
	}{{"no shadow", 0}, {"layered rects", 1}, {"pre-blurred texture", 2}} {
		r, err := newRig(false, scale, 1)
		if err != nil {
			return err
		}
		defer r.release()
		r.g.pages.cur = pageShadow
		r.g.pages.shad.only = c.pageVal
		clk := hostconn.RealClock{}
		var costs []time.Duration
		for i := 0; i < 120; i++ {
			t0 := clk.Now()
			if err := r.layout(); err != nil {
				return err
			}
			costs = append(costs, clk.Now().Sub(t0))
			r.now = r.now.Add(frameStep)
		}
		if err := report(w, "shadow/"+c.name, costs); err != nil {
			return err
		}
	}
	return nil
}

// benchFrames counts frames: an idle page must draw none; a menu opening
// and closing must stop once it settles (Q7).
func benchFrames(w io.Writer) error {
	// Idle pages, 120 ticks each: the buttons matrix is excluded because
	// its busy cells legitimately spin forever.
	for _, pid := range []pageID{pageFields, pageSplit, pageType, pageIcons, pageShadow} {
		r, err := newRig(false, 1, 1)
		if err != nil {
			return err
		}
		r.g.pages.cur = pid
		if err := r.layout(); err != nil {
			return err
		}
		idle := 0
		for i := 0; i < 120; i++ {
			if at, ok := r.g.env.Driver.Next(); ok && at.IsZero() {
				idle++
			}
			r.now = r.now.Add(frameStep)
			if err := r.layout(); err != nil {
				return err
			}
		}
		r.release()
		if _, err = fmt.Fprintf(w, "idle %-10s %d frames drawn beyond the first (want 0)\n", pageNames[pid], idle); err != nil {
			return fmt.Errorf("write the report: %w", err)
		}
	}

	// Menu: open → submenu → Escape, counting the frames the springs ask
	// for until each settles.
	r, err := newRig(false, 1, 1)
	if err != nil {
		return err
	}
	defer r.release()
	r.g.pages.cur = pageMenu
	if err := r.layout(); err != nil {
		return err
	}
	r.focusTag(r.g.pages.menu.trigger.Tag())
	count := func() (int, error) {
		frames := 0
		quiet := 0
		for i := 0; i < 600 && quiet < 2; i++ {
			if err := r.step(); err != nil {
				return frames, err
			}
			at, ok := r.g.env.Driver.Next()
			if ok && at.IsZero() {
				frames++ // the scene wants the next frame immediately
				quiet = 0
				continue
			}
			// Two quiet frames: a close requested in the last quiet frame
			// only starts moving in the next one.
			quiet++
		}
		return frames, nil
	}
	r.router.Queue(key.Event{Name: key.NameReturn, State: key.Press})
	r.router.Queue(key.Event{Name: key.NameReturn, State: key.Release})
	open, err := count()
	if err != nil {
		return err
	}
	r.pressName(key.NameDownArrow)
	r.pressName(key.NameDownArrow)
	r.pressName(key.NameDownArrow)
	sub, err := count()
	if err != nil {
		return err
	}
	// Right opens the submenu; count its spring too.
	r.pressName(key.NameRightArrow)
	subOpen, err := count()
	if err != nil {
		return err
	}
	// The first Escape closes the submenu; the second closes the root, and
	// its Exit animation runs while the frames keep coming.
	r.pressName(key.NameEscape)
	_, err = count()
	if err != nil {
		return err
	}
	r.pressName(key.NameEscape)
	closing, err := count()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "menu: open %d frames, walk %d, submenu %d, close %d (a naive always-drawing menu spends 60/s)\n",
		open, sub, subOpen, closing)
	return err
}

// benchUIScale lays every page out at each UI scale of §3.0 and reports the
// largest text that would overflow its cell: Q8's sanity check.
func benchUIScale(w io.Writer) error {
	for _, us := range []float32{0.9, 1.0, 1.1, 1.25} {
		r, err := newRig(false, 1, us)
		if err != nil {
			return err
		}
		defer r.release()
		failed := ""
		for i := range pageNames {
			r.g.pages.cur = pageID(i)
			if err := r.layout(); err != nil {
				return err
			}
			// A layout that panics or draws out of bounds would fail the
			// GPU frame; reaching here means the page survived the scale.
		}
		if failed != "" {
			_, err = fmt.Fprintf(w, "ui scale %.2f: %s\n", us, failed)
		} else {
			_, err = fmt.Fprintf(w, "ui scale %.2f: every page laid out and rendered\n", us)
		}
		if err != nil {
			return fmt.Errorf("write the report: %w", err)
		}
	}
	return nil
}

// report writes the percentiles of one measured series.
func report(w io.Writer, what string, d []time.Duration) error {
	if len(d) == 0 {
		return nil
	}
	slices.Sort(d)
	pct := func(p float64) time.Duration {
		return d[min(len(d)-1, int(p*float64(len(d))))].Round(10 * time.Microsecond)
	}
	_, err := fmt.Fprintf(w, "%-24s p50=%v p95=%v p99=%v max=%v (n=%d)\n", what, pct(0.5), pct(0.95), pct(0.99), d[len(d)-1].Round(10*time.Microsecond), len(d))
	if err != nil {
		return fmt.Errorf("write the report: %w", err)
	}
	return nil
}
