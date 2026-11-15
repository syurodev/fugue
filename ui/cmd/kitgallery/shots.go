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
)

// The headless shot matrix (UK-09): every page, in both themes, at UI
// scale 90, 100 and 125% and pixel scale 1 and 1.25. Frames run on a fake
// 60 Hz clock so the busy spinners read as spinning; the interaction
// states render through the same Preview path the window uses.

// frameStep is the fake frame interval, one 60 Hz frame.
const frameStep = time.Second / 60

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

// windowSize is the pixel size of the headless window for a pixel scale:
// the dp window times the pixel scale and the UI scale, so the zoomed
// page fits.
func windowSize(px, ui float32) image.Point {
	return image.Pt(int(float32(windowW)*px*ui), int(float32(windowH)*px*ui))
}

// newRig opens the headless window for one combo of the matrix.
func newRig(combo shotCombo) (*rig, error) {
	env, err := newEnv(combo.light)
	if err != nil {
		return nil, err
	}
	env.UIScale = combo.ui
	size := windowSize(combo.px, combo.ui)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		return nil, fmt.Errorf("open a headless window: %w", err)
	}
	return &rig{
		router: new(input.Router),
		win:    win,
		g:      newGallery(env, combo.light),
		size:   size,
		px:     combo.px,
		now:    time.Unix(1_700_000_000, 0),
	}, nil
}

// release frees the GPU window.
func (r *rig) release() { r.win.Release() }

// step lays one frame out at the fake clock, renders it and hands the ops
// to the router.
func (r *rig) step() error {
	r.ops.Reset()
	gtx := frameContext(&r.ops, r.g.env, r.size, r.px)
	gtx.Source = r.router.Source()
	gtx.Now = r.now
	if err := r.g.frame(&gtx); err != nil {
		return err
	}
	if err := r.win.Frame(&r.ops); err != nil {
		return fmt.Errorf("render a frame: %w", err)
	}
	r.router.Frame(&r.ops)
	r.now = r.now.Add(frameStep)
	return nil
}

// writeShots renders the whole matrix as PNGs into a tar stream on w.
func writeShots(w io.Writer) error {
	tw := tar.NewWriter(w)
	for _, combo := range shotCombos() {
		if err := shootCombo(tw, combo); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("finish the shot stream: %w", err)
	}
	return nil
}

// shootCombo renders one combo: every page, settled, with the busy
// spinners given a spin before the shot.
func shootCombo(tw *tar.Writer, combo shotCombo) error {
	r, err := newRig(combo)
	if err != nil {
		return err
	}
	defer r.release()
	for i := range pageNames() {
		r.g.cur = i
		for range 24 {
			if err := r.step(); err != nil {
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
