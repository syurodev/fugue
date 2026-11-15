package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"image"
	"image/png"
	"io"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/op"

	"github.com/syurodev/fugue/ui/internal/glwin"
)

// The headless shot matrix (UK-09): every page, in both themes, at UI
// scale 90, 100 and 125% and pixel scale 1 and 1.25, all in the one fixed
// window of windowW×windowH dp — the zoom changes how much fits, never the
// window (UK-E09). Frames run on a fake 60 Hz clock so the busy spinners
// read as spinning and the pressed cells' ripples have spread; the
// interaction states render through the same Preview path the window uses.
// The GL windows come from glwin: one per pixel scale for the whole run, never
// released.

// frameStep is the fake frame interval, one 60 Hz frame; shotFrames is how
// many frames a page runs before its shot, enough for a ripple to be half spread.
const (
	frameStep  = time.Second / 60
	shotFrames = 12
)

// shotCombo is one cell of the matrix: a theme, a UI scale and a pixel
// scale, with the name the PNG carries.
type shotCombo struct {
	name  string
	light bool
	ui    float32
	px    float32
}

// shotCombos enumerates the matrix.
func shotCombos() []shotCombo {
	uis := []struct {
		f     float32
		label string
	}{{0.9, "ui090"}, {1, "ui100"}, {1.25, "ui125"}}
	pxs := []struct {
		f     float32
		label string
	}{{1, "px1"}, {1.25, "px125"}}
	out := make([]shotCombo, 0, len(uis)*len(pxs)*2)
	for _, ui := range uis {
		for _, px := range pxs {
			for _, light := range []bool{false, true} {
				mode := "dark"
				if light {
					mode = "light"
				}
				out = append(out, shotCombo{mode + "-" + ui.label + "-" + px.label, light, ui.f, px.f})
			}
		}
	}
	return out
}

// rig is a headless GPU window over a gallery with its own input router
// and fake clock.
type rig struct {
	router *input.Router
	win    *headless.Window
	g      *gallery
	ops    op.Ops
	size   image.Point
	px     float32
	now    time.Time
}

// windowSize is the pixel size of the fixed gallery window at a pixel scale:
// the dp window times the pixel scale, and nothing of the UI scale.
func windowSize(px float32) image.Point {
	return image.Pt(int(float32(windowW)*px), int(float32(windowH)*px))
}

// newRig builds the gallery for one combo of the matrix over the shared
// headless window.
func newRig(combo shotCombo, win *headless.Window) (*rig, error) {
	env, err := newEnv(combo.light)
	if err != nil {
		return nil, err
	}
	env.UIScale = combo.ui
	return &rig{
		router: new(input.Router),
		win:    win,
		g:      newGallery(env, combo.light),
		size:   windowSize(combo.px),
		px:     combo.px,
		now:    time.Unix(1_700_000_000, 0),
	}, nil
}

// step lays one frame out at the fake clock, hands the ops to the router
// and, when render is set, draws them into the window.
func (r *rig) step(render bool) error {
	r.ops.Reset()
	gtx := frameContext(&r.ops, r.g.env, r.size, r.px)
	gtx.Source = r.router.Source()
	gtx.Now = r.now
	if err := r.g.frame(&gtx); err != nil {
		return err
	}
	if render {
		if err := r.win.Frame(&r.ops); err != nil {
			return fmt.Errorf("render a frame: %w", err)
		}
	}
	r.router.Frame(&r.ops)
	r.now = r.now.Add(frameStep)
	return nil
}

// writeShots renders the whole matrix as PNGs into a tar stream on w.
func writeShots(w io.Writer) error {
	tw := tar.NewWriter(w)
	wins := map[float32]*headless.Window{}
	for _, combo := range shotCombos() {
		win, ok := wins[combo.px]
		if !ok {
			size := windowSize(combo.px)
			var err error
			if win, err = glwin.Open(size.X, size.Y); err != nil {
				return fmt.Errorf("take the gallery window: %w", err)
			}
			wins[combo.px] = win
		}
		if err := shootCombo(tw, combo, win); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish the shot stream: %w", err)
	}
	return nil
}

// shootCombo renders one combo: every page, settled, with the busy
// spinners given a spin and the ripples a spread before the shot.
func shootCombo(tw *tar.Writer, combo shotCombo, win *headless.Window) error {
	r, err := newRig(combo, win)
	if err != nil {
		return err
	}
	for i := range pageNames() {
		r.g.cur = i
		for f := range shotFrames {
			if err := r.step(f == shotFrames-1); err != nil {
				return err
			}
		}
		if err := r.shoot(tw, pageNames()[i]+"-"+combo.name+".png"); err != nil {
			return err
		}
	}
	return nil
}

// shoot screenshots the last rendered frame into the stream as name.
func (r *rig) shoot(tw *tar.Writer, name string) error {
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
