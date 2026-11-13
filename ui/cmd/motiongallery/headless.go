package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"slices"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/ui/internal/hostconn"
)

// frameStep is the simulated frame interval: headless runs step a fake clock
// at 60 Hz so springs see the same steps as in a window.
const frameStep = time.Second / 60

// snapshots are the script times written by -frames, each catching an
// animation half way.
func snapshots() []time.Duration {
	return []time.Duration{ms(520), ms(1120), ms(1500), ms(2080), ms(2950), ms(3420), ms(4400), ms(6100), ms(6250)}
}

// rig is a headless window with the autoplaying gallery and a fake clock.
type rig struct {
	win   *headless.Window
	g     *gallery
	ops   op.Ops
	size  image.Point
	scale float32
	start time.Time
	now   time.Time
}

func newRig(scale float32, light bool) (*rig, error) {
	th, err := newTheme(light)
	if err != nil {
		return nil, err
	}
	size := image.Pt(int(windowW*scale), int(windowH*scale))
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		return nil, fmt.Errorf("open a headless window: %w", err)
	}
	t0 := time.Unix(1_700_000_000, 0)
	return &rig{win: win, g: newGallery(&th, true), ops: op.Ops{}, size: size, scale: scale, start: t0, now: t0}, nil
}

// layout builds one frame's ops at the rig's time.
func (r *rig) layout() {
	r.ops.Reset()
	gtx := frameContext(&r.ops, layout.Exact(r.size), r.scale)
	gtx.Now = r.now
	r.g.frame(&gtx)
}

// step lays out and renders one frame, then advances the clock.
func (r *rig) step() error {
	r.layout()
	if err := r.win.Frame(&r.ops); err != nil {
		return fmt.Errorf("render a frame: %w", err)
	}
	r.now = r.now.Add(frameStep)
	return nil
}

func (r *rig) elapsed() time.Duration { return r.now.Sub(r.start) }

// renderFrames writes a tar stream of PNG frames to w: the snapshots, or
// every frame of one loop at fps. The app never opens files itself (arch
// test TestAppWritesOnlyThroughHostAPI); the shell redirects the stream.
func renderFrames(w io.Writer, scale float32, fps int, light bool) error {
	r, err := newRig(scale, light)
	if err != nil {
		return err
	}
	defer r.win.Release()
	times := snapshots()
	if fps > 0 {
		times = times[:0]
		for t := time.Duration(0); t < autoplayLoop; t += time.Second / time.Duration(fps) {
			times = append(times, t)
		}
	}
	tw := tar.NewWriter(w)
	for _, want := range times {
		for r.elapsed() < want {
			if err := r.step(); err != nil {
				return err
			}
		}
		if err := r.step(); err != nil {
			return err
		}
		if err := r.writeFrame(tw, fmt.Sprintf("frame-%05dms.png", want.Milliseconds())); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish the frame stream: %w", err)
	}
	return nil
}

// writeFrame adds the last rendered frame to the tar stream as name.
func (r *rig) writeFrame(tw *tar.Writer, name string) error {
	img := image.NewRGBA(image.Rectangle{Max: r.size})
	if err := r.win.Screenshot(img); err != nil {
		return fmt.Errorf("read the frame back: %w", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(buf.Len()), ModTime: r.now, Format: tar.FormatPAX}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write the header of %s: %w", name, err)
	}
	if _, err := tw.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// runBench measures every frame of one scripted loop and reports how many
// frames the script needed: a settled gallery must draw none.
func runBench(w io.Writer, scale float32, light bool) error {
	r, err := newRig(scale, light)
	if err != nil {
		return err
	}
	defer r.win.Release()
	clk := hostconn.RealClock{}
	var layoutCost, frameCost []time.Duration
	drawn := 0
	for r.elapsed() < autoplayLoop {
		t0 := clk.Now()
		r.layout()
		t1 := clk.Now()
		if ferr := r.win.Frame(&r.ops); ferr != nil {
			return fmt.Errorf("render a frame: %w", ferr)
		}
		layoutCost, frameCost = append(layoutCost, t1.Sub(t0)), append(frameCost, clk.Now().Sub(t0))
		drawn++
		r.now = r.nextFrame()
	}
	if err = report(w, "layout", layoutCost); err != nil {
		return err
	}
	if err = report(w, "layout+render", frameCost); err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "frames drawn over the %v loop: %d (a window that always drew would need %d)\n", autoplayLoop, drawn, int(autoplayLoop/frameStep))
	if err != nil {
		return fmt.Errorf("write the report: %w", err)
	}
	return nil
}

// nextFrame is when the gallery asked to be drawn again: the next 60 Hz tick
// while something moves, otherwise the next scripted cue.
func (r *rig) nextFrame() time.Time {
	at, ok := r.g.driver.Next()
	switch {
	case ok && at.IsZero():
		return r.now.Add(frameStep)
	case ok:
		return at
	default:
		return r.start.Add(autoplayLoop)
	}
}

// report writes the percentiles of one measured series.
func report(w io.Writer, what string, d []time.Duration) error {
	slices.Sort(d)
	pct := func(p float64) time.Duration {
		return d[min(len(d)-1, int(p*float64(len(d))))].Round(10 * time.Microsecond)
	}
	_, err := fmt.Fprintf(w, "%-14s p50=%v p95=%v p99=%v max=%v\n", what, pct(0.5), pct(0.95), pct(0.99), d[len(d)-1].Round(10*time.Microsecond))
	if err != nil {
		return fmt.Errorf("write the report: %w", err)
	}
	return nil
}
