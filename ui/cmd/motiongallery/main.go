// Command motiongallery is a development gallery of the desktop app's motion:
// a dropdown menu, a press button, a switch, a segmented control, a
// disclosure section, and a message feed with sliding inserts, streaming
// text and a rolling counter. Every animation is a spring from
// ui/internal/motion and can be interrupted mid-way. It ships only for
// development: fugue-app never imports it. Run it from the ui directory:
//
//	go run ./cmd/motiongallery                    # interactive window
//	go run ./cmd/motiongallery -autoplay          # window driven by a script
//	go run ./cmd/motiongallery -frames > f.tar    # headless PNG snapshots
//	go run ./cmd/motiongallery -frames -fps 30 -scale 1 > loop.tar
//	go run ./cmd/motiongallery -bench             # frame cost over the script
//
// Owner: motion spike. Allowed callers: none, it is an entry point.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The window size in dp.
const (
	windowW = 880
	windowH = 600
)

// errUsage reports conflicting flags.
var errUsage = errors.New("use either -frames or -bench, not both")

func main() {
	frames := flag.Bool("frames", false, "write a tar stream of PNG frames of the script to standard output and exit")
	bench := flag.Bool("bench", false, "measure frame cost over the scripted demo and exit")
	autoplay := flag.Bool("autoplay", false, "drive the window with the scripted demo")
	light := flag.Bool("light", false, "use the light theme")
	fps := flag.Int("fps", 0, "with -frames: write one loop at this frame rate instead of the fixed snapshots")
	scale := flag.Float64("scale", 2, "pixels per dp for -frames and -bench")
	flag.Parse()
	var err error
	switch {
	case *frames && *bench:
		err = errUsage
	case *frames:
		err = renderFrames(os.Stdout, float32(*scale), *fps, *light)
	case *bench:
		err = runBench(os.Stdout, float32(*scale), *light)
	default:
		go func() {
			code := 0
			if werr := runWindow(*autoplay, *light); werr != nil {
				fmt.Fprintln(os.Stderr, "motiongallery:", werr)
				code = 1
			}
			os.Exit(code)
		}()
		app.Main()
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "motiongallery:", err)
		os.Exit(1)
	}
}

// newTheme builds the dark or light theme over the bundled fonts.
func newTheme(light bool) (theme.Theme, error) {
	sh, err := fonts.Shaper()
	if err != nil {
		return theme.Theme{}, fmt.Errorf("build the text shaper: %w", err)
	}
	if light {
		return theme.Light(sh), nil
	}
	return theme.Dark(sh), nil
}

// runWindow opens the gallery and runs it until the window is closed.
func runWindow(autoplay, light bool) error {
	th, err := newTheme(light)
	if err != nil {
		return err
	}
	g := newGallery(&th, autoplay)
	w := new(app.Window)
	w.Option(app.Title("Fugue motion gallery"), app.Size(unit.Dp(windowW), unit.Dp(windowH)))
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			g.frame(&gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// frameContext is the layout context of a headless frame.
func frameContext(ops *op.Ops, size layout.Constraints, scale float32) layout.Context {
	return layout.Context{Ops: ops, Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: size}
}
