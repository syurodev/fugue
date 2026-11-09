package perf

import (
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"strings"

	"gioui.org/app"
	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

var (
	auto  = flag.Bool("auto", false, "run the scripted phases in a real window, print the table and exit")
	bench = flag.Bool("bench", false, "run the scripted active phases headless (no display, no vsync) and print the table")
	only  = flag.String("only", "", "run only the phases whose name contains this")
)

// Spec describes one spike binary.
type Spec struct {
	Title  string
	Size   image.Point // dp
	Draw   func(gtx layout.Context)
	Phases func() []Phase
	Done   func() // facts to print after the table (e.g. selection kept)
	// Interactive runs once per frame in by-hand runs (not -auto or -bench).
	Interactive func(gtx layout.Context)
}

// Run starts the spike: interactive by default, scripted with -auto or
// -bench. Call after flag.Parse.
func Run(s Spec) {
	if *bench {
		runHeadless(s)
		return
	}
	go func() {
		w := new(app.Window)
		w.Option(app.Title(s.Title), app.Size(unit.Dp(s.Size.X), unit.Dp(s.Size.Y)))
		var rn *Runner
		if *auto {
			rn = NewRunner(filter(s.Phases()), w.Invalidate, os.Stdout)
			fmt.Println(Header)
		}
		var ops op.Ops
		for {
			switch e := w.Event().(type) {
			case app.DestroyEvent:
				os.Exit(0)
			case app.FrameEvent:
				gtx := app.NewContext(&ops, e)
				if rn != nil && !rn.Frame(gtx) {
					if s.Done != nil {
						s.Done()
					}
					os.Exit(0)
				}
				if rn != nil {
					rn.Layout(func() { s.Draw(gtx) })
				} else {
					if s.Interactive != nil {
						s.Interactive(gtx)
					}
					s.Draw(gtx)
				}
				e.Frame(gtx.Ops)
			}
		}
	}()
	app.Main()
}

func filter(ps []Phase) []Phase {
	var out []Phase
	for _, p := range ps {
		if strings.Contains(p.Name, *only) {
			out = append(out, p)
		}
	}
	return out
}

func runHeadless(s Spec) {
	var ps []Phase
	for _, p := range filter(s.Phases()) {
		if !p.Idle {
			ps = append(ps, p)
		}
	}
	w, err := headless.NewWindow(s.Size.X, s.Size.Y)
	if err != nil {
		log.Fatal(err)
	}
	defer w.Release()
	rn := NewRunner(ps, func() {}, os.Stdout)
	fmt.Println(Header)
	var ops op.Ops
	for {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Constraints: layout.Exact(s.Size), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
		if !rn.Frame(gtx) {
			break
		}
		rn.Layout(func() { s.Draw(gtx) })
		rn.Render(func() {
			if err := w.Frame(&ops); err != nil {
				log.Fatal(err)
			}
		})
	}
	if s.Done != nil {
		s.Done()
	}
}
