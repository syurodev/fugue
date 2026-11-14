package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kitspike"
	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// frameStep is the simulated frame interval: headless runs step a fake clock
// at 60 Hz so springs see the same steps as in a window.
const frameStep = time.Second / 60

// rig is a headless GPU window with a gallery, a fake clock and its own
// input router, so scripts can queue real key and pointer events.
type rig struct {
	router  *input.Router
	win     *headless.Window
	ops     op.Ops
	size    image.Point
	scale   float32
	uiscale float32
	start   time.Time
	now     time.Time
	th      *theme.Theme
	light   bool
	g       *gallery
}

func newRig(light bool, scale, uiscale float32) (*rig, error) {
	sh, err := fonts.Shaper()
	if err != nil {
		return nil, fmt.Errorf("build the text shaper: %w", err)
	}
	var th theme.Theme
	if light {
		th = theme.Light(sh)
	} else {
		th = theme.Dark(sh)
	}
	g := &gallery{env: kitspike.Env{Th: &th, Sh: sh, Driver: &motion.Driver{}}}
	g.pages = newPages(&g.env)
	size := image.Pt(int(windowW*scale*uiscale), int(windowH*scale*uiscale))
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		return nil, fmt.Errorf("open a headless window: %w", err)
	}
	t0 := time.Unix(1_700_000_000, 0)
	return &rig{
		router: new(input.Router), win: win, size: size, scale: scale, uiscale: uiscale,
		start: t0, now: t0, th: &th, light: light, g: g,
	}, nil
}

func (r *rig) release() { r.win.Release() }

// layout builds and renders one frame at the rig's time and hands the ops to
// the router.
func (r *rig) layout() error {
	r.ops.Reset()
	gtx := frameContext(&r.ops, layout.Exact(r.size), uiMetric(r.scale, r.uiscale))
	gtx.Now = r.now
	gtx.Source = r.router.Source()
	r.g.frame(&gtx)
	if err := r.win.Frame(&r.ops); err != nil {
		return fmt.Errorf("render a frame: %w", err)
	}
	r.router.Frame(&r.ops)
	return nil
}

// step lays out and renders one frame, then advances the clock.
func (r *rig) step() error {
	if err := r.layout(); err != nil {
		return err
	}
	r.now = r.now.Add(frameStep)
	return nil
}

// stepUntil advances to the given elapsed time.
func (r *rig) stepUntil(elapsed time.Duration) error {
	for r.now.Sub(r.start) < elapsed {
		if err := r.step(); err != nil {
			return err
		}
	}
	return nil
}

func (r *rig) elapsed() time.Duration { return r.now.Sub(r.start) }

// pressName queues a press and release of a named key for the next frame.
func (r *rig) pressName(n key.Name) {
	r.router.Queue(key.Event{Name: n, State: key.Press})
	r.router.Queue(key.Event{Name: n, State: key.Release})
}

// focusTag focuses one tag through the router, the way a user's first Tab
// would.
func (r *rig) focusTag(tag any) {
	r.ops.Reset()
	gtx := frameContext(&r.ops, layout.Exact(r.size), uiMetric(r.scale, r.uiscale))
	gtx.Now = r.now
	gtx.Source = r.router.Source()
	r.g.frame(&gtx)
	gtx.Execute(key.FocusCmd{Tag: tag})
	r.router.Frame(&r.ops)
}

// shoot renders the current scene to dir/name.png.
func (r *rig) shoot(dir, name string) error {
	if err := r.layout(); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rectangle{Max: r.size})
	if err := r.win.Screenshot(img); err != nil {
		return fmt.Errorf("read the frame back: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return fmt.Errorf("encode %s: %w", name, err)
	}
	path := filepath.Join(dir, name+".png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writeShots writes the whole matrix into dir: every page, in light and
// dark, at pixel scale 1 and 1.25, plus the UI-scale variants of §3.0.
func writeShots(dir string, pxScale float32) error {
	type variant struct {
		tag     string
		light   bool
		px      float32
		uiscale float32
	}
	variants := []variant{
		{"dark-px1", false, 1, 1}, {"light-px1", true, 1, 1},
		{"dark-px125", false, pxScale, 1}, {"light-px125", true, pxScale, 1},
		{"dark-ui90", false, 1, 0.9}, {"dark-ui125", false, 1, 1.25},
		{"light-ui90", true, 1, 0.9}, {"light-ui125", true, 1, 1.25},
	}
	for _, v := range variants {
		r, err := newRig(v.light, v.px, v.uiscale)
		if err != nil {
			return err
		}
		// Warm up one frame per page so fonts and icons parse before the
		// first measured shot.
		for i := range pageNames {
			r.g.pages.cur = pageID(i)
			if err := r.step(); err != nil {
				return err
			}
		}
		for i, name := range pageNames {
			r.g.pages.cur = pageID(i)
			if pageID(i) == pageMenu {
				if err := shootMenu(r, dir, name+"-"+v.tag); err != nil {
					return err
				}
				continue
			}
			if err := r.shoot(dir, fmt.Sprintf("%s-%s", name, v.tag)); err != nil {
				return err
			}
		}
		r.release()
	}
	return writeIndex(dir)
}

// shootMenu writes the four menu states: closed, open, submenu open, and
// closed again after Escape.
func shootMenu(r *rig, dir, base string) error {
	r.g.pages.cur = pageMenu
	if err := r.shoot(dir, base+"-closed"); err != nil {
		return err
	}
	// Open the menu the way a user does: focus the trigger, press Enter.
	r.g.pages.overlay.CloseAll()
	r.focusTag(r.g.pages.menu.trigger.Tag())
	r.pressName(key.NameEnter)
	for i := 0; i < 30; i++ { // let the open spring settle
		if err := r.step(); err != nil {
			return err
		}
	}
	if err := r.shoot(dir, base+"-open"); err != nil {
		return err
	}
	// Walk to the Export item and open its submenu with Right.
	r.pressName(key.NameDownArrow)
	r.pressName(key.NameDownArrow)
	r.pressName(key.NameRightArrow)
	for i := 0; i < 30; i++ {
		if err := r.step(); err != nil {
			return err
		}
	}
	if err := r.shoot(dir, base+"-submenu"); err != nil {
		return err
	}
	// Escape closes and returns focus to the opener.
	r.pressName(key.NameEscape)
	for i := 0; i < 40; i++ {
		if err := r.step(); err != nil {
			return err
		}
	}
	return r.shoot(dir, base+"-closed-again")
}
