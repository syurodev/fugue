// Command kitspike is the throwaway gallery of the UI kit spike (ui-kit §9):
// the four hardest components — Button, TextField, Menu, SplitPane — built
// to the spec, plus the type specimen, the icon comparison and the shadow
// measurement. It ships only for development. Run it from the ui directory:
//
//	go run ./cmd/kitspike                          # interactive window
//	go run ./cmd/kitspike -light                   # light mode
//	go run ./cmd/kitspike -shots DIR               # every screenshot of the matrix
//	go run ./cmd/kitspike -bench                   # frame cost numbers
//	go run ./cmd/kitspike -type-metrics            # the Q4 line-height table
//
// Owner: UI kit spike. Allowed callers: none, it is an entry point.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kitspike"
	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The window size in dp.
const (
	windowW = 900
	windowH = 640
)

// errUsage reports conflicting flags.
var errUsage = errors.New("use one of -shots, -bench, -type-metrics at a time")

func main() {
	shotsDir := flag.String("shots", "", "write the whole screenshot matrix as PNG files into this directory and exit")
	bench := flag.Bool("bench", false, "print the frame-cost numbers of Q6/Q7/Q8 and exit")
	typeMetrics := flag.Bool("type-metrics", false, "print the Q4 Vietnamese line-height table and exit")
	light := flag.Bool("light", false, "use the light theme")
	scale := flag.Float64("scale", 2, "pixels per dp for -shots and -bench")
	uiscale := flag.Float64("uiscale", 1, "UI scale on top of the platform dp (0.9, 1, 1.1, 1.25)")
	onePage := flag.Int("page", -1, "lay out only this page index once and exit (debug)")
	focusField := flag.Bool("focus-field", false, "start on the TextField page with the first field focused (for the IME check)")
	flag.Parse()

	if *onePage >= 0 {
		if err := debugPage(*light, *onePage, float32(*scale), float32(*uiscale)); err != nil {
			fmt.Fprintln(os.Stderr, "kitspike:", err)
			os.Exit(1)
		}
		return
	}
	var err error
	switch {
	case *shotsDir != "":
		err = writeShots(*shotsDir, float32(*scale))
	case *bench:
		err = runBench(os.Stdout, float32(*scale))
	case *typeMetrics:
		err = printTypeMetrics(os.Stdout, float32(*scale))
	default:
		go func() {
			code := 0
			if werr := runWindow(*light, float32(*uiscale), *focusField); werr != nil {
				fmt.Fprintln(os.Stderr, "kitspike:", werr)
				code = 1
			}
			os.Exit(code)
		}()
		app.Main()
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "kitspike:", err)
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

// debugPage lays one page out once in a headless window: the panic bisection
// helper behind -page.
func debugPage(light bool, page int, scale, uiscale float32) error {
	r, err := newRig(light, scale, uiscale)
	if err != nil {
		return err
	}
	defer r.release()
	r.g.pages.cur = pageID(page)
	return r.layout()
}

// runWindow opens the gallery and runs it until the window closes.
func runWindow(light bool, uiscale float32, focusField bool) error {
	th, err := newTheme(light)
	if err != nil {
		return err
	}
	g := newGallery(&th, false)
	g.focusField = focusField
	g.pages.cur = pageFields
	w := new(app.Window)
	w.Option(app.Title("Fugue kit spike"), app.Size(unit.Dp(windowW), unit.Dp(windowH)))
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

// uiMetric returns the metric of the platform scale with the UI scale
// applied on top (§3.0): the whole spike answers Q8 with this one override.
func uiMetric(pxPerDp float32, uiscale float32) unit.Metric {
	return unit.Metric{PxPerDp: pxPerDp * uiscale, PxPerSp: pxPerDp * uiscale}
}

// frameContext is the layout context of a headless frame.
func frameContext(ops *op.Ops, size layout.Constraints, metric unit.Metric) layout.Context {
	return layout.Context{Ops: ops, Metric: metric, Constraints: size}
}

// gallery is the window: one page per component plus the measurement pages.
type gallery struct {
	env    kitspike.Env
	script *autoplay
	pages  *pages
	// focusField focuses the first text field on the first frame; the
	// IME check uses it so keystrokes land in the editor immediately.
	focusField  bool
	focusedOnce bool
}

func newGallery(th *theme.Theme, autoplay bool) *gallery {
	g := &gallery{
		env: kitspike.Env{Th: th, Sh: th.Shaper(), Driver: &motion.Driver{}},
	}
	g.pages = newPages(&g.env)
	_ = g.pages
	if autoplay {
		g.script = newAutoplay(&g.env, g.pages)
	}
	return g
}

// frame draws one frame and schedules the next only if something moves (Q7).
func (g *gallery) frame(gtx *layout.Context) {
	g.env.Begin(gtx.Now)
	g.env.TrackInput(gtx)
	if g.script != nil {
		g.script.step(gtx)
	}
	g.pages.layout(gtx, &g.focusField, &g.focusedOnce)
	g.env.FrameEnd(gtx)
}

// runBench prints the bench numbers.
func runBench(w io.Writer, scale float32) error { return bench(w, scale) }
