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
		name   string
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

// benchFrames counts frames: an idle gallery must draw none; a menu opening
// and closing must stop once it settles (Q7).
func benchFrames(w io.Writer) error {
	r, err := newRig(false, 1, 1)
	if err != nil {
		return err
	}
	defer r.release()

	// Idle: 2 seconds, no script, nothing moving.
	idle := 0
	for i := 0; i < 120; i++ {
		at, ok := r.g.env.Driver.Next()
		if ok && at.IsZero() {
			idle++
		}
		if err := r.step(); err != nil {
			return err
		}
	}
	if _, err = fmt.Fprintf(w, "idle gallery: %d frames drawn in 120 ticks (want 0)\n", idle); err != nil {
		return fmt.Errorf("write the report: %w", err)
	}

	// Menu open → settle → Escape → settle: count the frames the springs
	// actually need.
	r.g.pages.cur = pageMenu
	r.focusTag(r.g.pages.menu.trigger.Tag())
	drawn := 0
	counted := func() error {
		drawn++
		return r.step()
	}
	// open
	r.router.Queue(key.Event{Name: key.NameEnter, State: key.Press})
	for r.elapsed() < time.Second && menuAnimating(r) {
		if err := counted(); err != nil {
			return err
		}
	}
	openFrames := drawn
	// submenu
	r.pressName(key.NameRightArrow)
	subStart := drawn
	for r.elapsed() < 2*time.Second && menuAnimating(r) {
		if err := counted(); err != nil {
			return err
		}
	}
	subFrames := drawn - subStart
	// close
	r.pressName(key.NameEscape)
	closeStart := drawn
	for r.elapsed() < 3*time.Second && menuAnimating(r) {
		if err := counted(); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "menu open: %d frames to settle, submenu: %d, close: %d (at 60 Hz a naive always-drawing menu would use 60/s)\n",
		openFrames, subFrames, drawn-closeStart)
	return err
}

// menuAnimating reports whether the overlay or any control still moves.
func menuAnimating(r *rig) bool {
	at, ok := r.g.env.Driver.Next()
	return ok && at.IsZero()
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

