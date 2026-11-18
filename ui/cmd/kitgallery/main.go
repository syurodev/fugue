// Command kitgallery is the development gallery of the shared component
// kit (ui-kit UK-09): one page per component with every variant × size ×
// state in light and dark, keyboard-only and reduced-motion modes, and a
// headless shot mode that writes the whole matrix as PNGs to standard
// output as a tar stream — the app's arch rule keeps direct file writes
// out of the ui module, so redirect and extract:
//
//	go run ./cmd/kitgallery                     # interactive window
//	go run ./cmd/kitgallery -light              # start in light mode
//	go run ./cmd/kitgallery -keyboard           # keyboard-only: rings stay on
//	go run ./cmd/kitgallery -reduced            # reduced motion
//	go run ./cmd/kitgallery -slow               # every motion at a quarter speed
//	go run ./cmd/kitgallery -shots > shots.tar  # pages × themes × UI scale 90/100/125% × pixel scale 1/1.25
//
// In the window: Ctrl+D toggles the theme, M toggles reduced motion, K
// toggles keyboard-only, S toggles slow motion, Tab walks the controls,
// Enter and Space activate.
//
// Owner: F10.9. Allowed callers: none, it is an entry point; fugue-app
// never imports it.
package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"runtime"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func main() {
	light := flag.Bool("light", false, "start in light mode")
	keyboard := flag.Bool("keyboard", false, "start in keyboard-only mode: rings stay on")
	reduced := flag.Bool("reduced", false, "start with reduced motion")
	slow := flag.Bool("slow", false, "start in slow motion: every motion at a quarter speed")
	uiscale := flag.Float64("uiscale", 1, "interface size, a multiplier on dp and sp: 0.9, 1 or 1.25")
	shots := flag.Bool("shots", false, "write the shot matrix as a tar stream of PNGs to standard output and exit")
	flag.Parse()
	if *shots {
		if err := writeShots(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "kitgallery:", err)
			os.Exit(1)
		}
		return
	}
	go func() {
		code := 0
		if err := runWindow(*light, *keyboard, *reduced, *slow, float32(*uiscale)); err != nil {
			fmt.Fprintln(os.Stderr, "kitgallery:", err)
			code = 1
		}
		os.Exit(code)
	}()
	app.Main()
}

// runWindow opens the gallery window and runs it until it is closed.
func runWindow(light, keyboard, reduced, slow bool, uiscale float32) error {
	env, err := newEnv(light)
	if err != nil {
		return err
	}
	env.KeyboardOnly = keyboard
	env.ReducedMotion = reduced
	env.UIScale = uiscale
	g := newGallery(env, light)
	g.setSlow(slow)
	w := new(app.Window)
	w.Option(app.Title("Fugue kit gallery"), app.Size(windowW, windowH))
	env.Invalidate = w.Invalidate
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			gtx.Metric = env.Metric(gtx.Metric)
			if err := g.frame(&gtx); err != nil {
				return err
			}
			e.Frame(gtx.Ops)
		}
	}
}

// newEnv builds the window environment over the bundled fonts and the
// theme light picks.
func newEnv(light bool) (*kit.Env, error) {
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
	env := &kit.Env{Th: &th, Sh: sh}
	if runtime.GOOS == "darwin" {
		env.Platform = kit.PlatformMac
	}
	return env, nil
}

// frameContext is the layout context of a headless frame at pixel scale px
// and the env's UI scale, over a window of size pixels.
func frameContext(ops *op.Ops, env *kit.Env, size image.Point, px float32) layout.Context {
	device := unit.Metric{PxPerDp: px, PxPerSp: px}
	return layout.Context{
		Ops:         ops,
		Metric:      env.Metric(device),
		Constraints: layout.Exact(size),
	}
}
