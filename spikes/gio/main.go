// Throwaway spike: can Gio render a Fugue-style transcript (markdown, tool
// cards, diffs, streaming, 2000+ items, Vietnamese IME) cheaply enough for a
// weak-CPU Ubuntu machine? Not product code.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"log"
	"os"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"gioui.org/x/markdown"
	"gioui.org/x/richtext"
)

var (
	benchMode  = flag.Bool("bench", false, "run the automated bench and exit")
	nItems     = flag.Int("items", 2000, "transcript items preloaded")
	streamSecs = flag.Int("stream-secs", 15, "seconds of simulated streaming")
	rate       = flag.Int("rate", 600, "streamed characters per second")
	scrollSecs = flag.Int("scroll-secs", 10, "seconds of programmatic scrolling")
	idleSecs   = flag.Int("idle-secs", 5, "seconds idle (nothing changes)")
)

type kind int

const (
	kUser kind = iota
	kAssistant
	kTool
	kDiff
)

type item struct {
	kind     kind
	md       string
	spans    []richtext.SpanStyle
	state    richtext.InteractiveText
	title    string
	body     string
	expanded widget.Clickable
	open     bool
	dirty    bool
}

type ui struct {
	th       *material.Theme
	md       *markdown.Renderer
	items    []*item
	list     widget.List
	editor   widget.Editor
	frames   []time.Duration // per-frame CPU-side layout time
	gaps     []time.Duration // time between frames
	lastF    time.Time
	phase    string
	phaseT0  time.Time
	cpu0     time.Duration
	streamed int
	results  []string
}

const assistantMD = `## Kế hoạch sửa lỗi

Mình sẽ đọc **internal/coordinator** trước, rồi chạy test với ` + "`-race`" + `.

- Bước 1: tìm chỗ slot bị rò khi observation bị từ chối
- Bước 2: thêm test ` + "`TestRejectedTerminalObservationFreesTheSlot`" + `
- Bước 3: chạy lại ` + "`task check`" + `

` + "```go" + `
func (s *Session) serve(wCtx context.Context, w *Work) {
	s.cfg.Executor.Execute(wCtx, w, s.report)
	s.served(&w.Lease)
}
` + "```" + `

Kết quả: mọi slot được giải phóng đúng một lần. Xem thêm https://example.invalid/docs.
`

const toolBody = `$ task test-pkg PKG=./internal/coordinator RUN=Slot COUNT=50
ok  	github.com/syurodev/fugue/internal/coordinator	12.481s
PASS
coverage: 94.7% of statements`

const diffBody = `@@ -34,21 +34,15 @@ func (s *Session) work(wCtx context.Context) error {
-	terminal := &atomic.Bool{}
-	s.cfg.Executor.Execute(wCtx, w, func(obs *Observation) {
-		if obs != nil && isTerminalObservation(obs.Command) {
-			terminal.Store(true)
-		}
-		s.report(obs)
-	})
+	s.cfg.Executor.Execute(wCtx, w, s.report)
+	s.served(&w.Lease)`

func newUI() *ui {
	u := &ui{th: material.NewTheme(), md: markdown.NewRenderer()}
	u.th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	u.md.Config.DefaultSize = 14
	u.md.Config.MonospaceFont = font.Font{Typeface: "Go Mono"}
	u.list.Axis = layout.Vertical
	u.list.ScrollToEnd = true
	u.editor.Submit = true
	for i := 0; i < *nItems; i++ {
		switch i % 4 {
		case 0:
			u.items = append(u.items, &item{kind: kUser, md: fmt.Sprintf("Tin nhắn %d: sửa giúp mình lỗi rò slot trong coordinator, nhớ chạy test với -race nhé.", i), dirty: true})
		case 1:
			u.items = append(u.items, &item{kind: kAssistant, md: assistantMD, dirty: true})
		case 2:
			u.items = append(u.items, &item{kind: kTool, title: "Bash · task test-pkg", body: toolBody, open: i%8 == 2})
		case 3:
			u.items = append(u.items, &item{kind: kDiff, title: "internal/coordinator/worker.go", body: diffBody})
		}
	}
	return u
}

func (u *ui) layoutItem(gtx layout.Context, it *item) layout.Dimensions {
	inset := layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(12), Right: unit.Dp(12)}
	return inset.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		switch it.kind {
		case kUser, kAssistant:
			if it.dirty {
				spans, err := u.md.Render([]byte(it.md))
				if err == nil {
					it.spans = spans
				}
				it.dirty = false
			}
			bg := color.NRGBA{A: 0}
			if it.kind == kUser {
				bg = color.NRGBA{R: 0xee, G: 0xf2, B: 0xff, A: 0xff}
			}
			return card(gtx, bg, func(gtx layout.Context) layout.Dimensions {
				return richtext.Text(&it.state, u.th.Shaper, it.spans...).Layout(gtx)
			})
		case kTool:
			if it.expanded.Clicked(gtx) {
				it.open = !it.open
			}
			return card(gtx, color.NRGBA{R: 0xf6, G: 0xf6, B: 0xf6, A: 0xff}, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return material.Clickable(gtx, &it.expanded, func(gtx layout.Context) layout.Dimensions {
							l := material.Body2(u.th, map[bool]string{true: "▾ ", false: "▸ "}[it.open]+it.title)
							l.Font.Weight = font.Bold
							return l.Layout(gtx)
						})
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if !it.open {
							return layout.Dimensions{}
						}
						l := material.Body2(u.th, it.body)
						l.Font.Typeface = "Go Mono"
						return l.Layout(gtx)
					}),
				)
			})
		default:
			return card(gtx, color.NRGBA{R: 0xfb, G: 0xfb, B: 0xfb, A: 0xff}, func(gtx layout.Context) layout.Dimensions {
				lines := strings.Split(it.body, "\n")
				children := []layout.FlexChild{layout.Rigid(material.Body2(u.th, it.title).Layout)}
				for _, ln := range lines {
					ln := ln
					children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						l := material.Body2(u.th, ln)
						l.Font.Typeface = "Go Mono"
						switch {
						case strings.HasPrefix(ln, "+"):
							l.Color = color.NRGBA{G: 0x80, A: 0xff}
						case strings.HasPrefix(ln, "-"):
							l.Color = color.NRGBA{R: 0xb0, A: 0xff}
						}
						return l.Layout(gtx)
					}))
				}
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
			})
		}
	})
}

func card(gtx layout.Context, bg color.NRGBA, w layout.Widget) layout.Dimensions {
	macro := op.Record(gtx.Ops)
	dims := layout.UniformInset(unit.Dp(8)).Layout(gtx, w)
	call := macro.Stop()
	if bg.A > 0 {
		rr := gtx.Dp(unit.Dp(6))
		defer clip.UniformRRect(image.Rectangle{Max: dims.Size}, rr).Push(gtx.Ops).Pop()
		paint.Fill(gtx.Ops, bg)
	}
	call.Add(gtx.Ops)
	return dims
}

func (u *ui) layout(gtx layout.Context) layout.Dimensions {
	for {
		ev, ok := u.editor.Update(gtx)
		if !ok {
			break
		}
		if s, ok := ev.(widget.SubmitEvent); ok && strings.TrimSpace(s.Text) != "" {
			u.items = append(u.items, &item{kind: kUser, md: s.Text, dirty: true})
			u.editor.SetText("")
		}
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return material.List(u.th, &u.list).Layout(gtx, len(u.items), func(gtx layout.Context, i int) layout.Dimensions {
				return u.layoutItem(gtx, u.items[i])
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(unit.Dp(10)).Layout(gtx, material.Editor(u.th, &u.editor, "Nhắn cho Opus… (gõ Telex để thử IME)").Layout)
		}),
	)
}

func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func maxRSSMB() float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	if runtime.GOOS == "darwin" {
		return float64(ru.Maxrss) / (1 << 20)
	}
	return float64(ru.Maxrss) / (1 << 10)
}

func pct(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*p)]
}

func (u *ui) startPhase(name string, now time.Time) {
	u.phase, u.phaseT0, u.cpu0 = name, now, cpuTime()
	u.frames, u.gaps = u.frames[:0], u.gaps[:0]
}

func (u *ui) endPhase(now time.Time) {
	wall := now.Sub(u.phaseT0)
	cpu := cpuTime() - u.cpu0
	u.results = append(u.results, fmt.Sprintf(
		"%-7s frames=%4d fps=%5.1f layout_p50=%6.2fms p95=%6.2fms max=%6.2fms gap_p95=%6.1fms gap_max=%6.1fms cpu=%5.1f%%core rss_max=%.0fMB",
		u.phase, len(u.frames), float64(len(u.frames))/wall.Seconds(),
		ms(pct(u.frames, .5)), ms(pct(u.frames, .95)), ms(pct(u.frames, 1)),
		ms(pct(u.gaps, .95)), ms(pct(u.gaps, 1)), 100*cpu.Seconds()/wall.Seconds(), maxRSSMB()))
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// bench advances the scripted phases; it runs on the event goroutine.
func (u *ui) bench(gtx layout.Context, now time.Time) (done bool) {
	el := now.Sub(u.phaseT0)
	switch u.phase {
	case "":
		u.startPhase("open", now)
	case "open":
		if el > 2*time.Second {
			u.endPhase(now)
			u.items = append(u.items, &item{kind: kAssistant, md: "", dirty: true})
			u.startPhase("stream", now)
		}
	case "stream":
		target := int(el.Seconds() * float64(*rate))
		src := strings.Repeat(assistantMD+"\n", 40)
		if target > len(src) {
			target = len(src)
		}
		if target > u.streamed {
			last := u.items[len(u.items)-1]
			last.md = src[:target]
			last.dirty = true
			u.streamed = target
		}
		if el > time.Duration(*streamSecs)*time.Second {
			u.endPhase(now)
			u.list.ScrollToEnd = false
			u.list.Position.First, u.list.Position.Offset = 0, 0
			u.list.Position.BeforeEnd = true
			u.startPhase("scroll", now)
		}
	case "scroll":
		u.list.ScrollBy(0.6)
		if el > time.Duration(*scrollSecs)*time.Second {
			u.endPhase(now)
			u.startPhase("idle", now)
		}
	case "idle":
		if el > time.Duration(*idleSecs)*time.Second {
			u.endPhase(now)
			return true
		}
		return false // idle: do not request frames; measure CPU at rest
	}
	gtx.Execute(op.InvalidateCmd{})
	return false
}

func run(w *app.Window) error {
	u := newUI()
	var ops op.Ops
	var idleTimer <-chan time.Time
	events := make(chan struct{})
	_ = events
	for {
		if u.phase == "idle" && idleTimer == nil {
			idleTimer = time.After(time.Duration(*idleSecs)*time.Second + 200*time.Millisecond)
			go func() { <-idleTimer; w.Invalidate() }()
		}
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			t0 := time.Now()
			if !u.lastF.IsZero() {
				u.gaps = append(u.gaps, t0.Sub(u.lastF))
			}
			u.lastF = t0
			gtx := app.NewContext(&ops, e)
			paint.Fill(gtx.Ops, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff})
			u.layout(gtx)
			if *benchMode && u.bench(gtx, t0) {
				fmt.Printf("os=%s arch=%s cpus=%d items=%d rate=%dch/s\n", runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), *nItems, *rate)
				for _, r := range u.results {
					fmt.Println(r)
				}
				os.Exit(0)
			}
			e.Frame(gtx.Ops)
			u.frames = append(u.frames, time.Since(t0))
		}
	}
}

func main() {
	flag.Parse()
	go func() {
		w := new(app.Window)
		w.Option(app.Title("Fugue transcript spike"), app.Size(unit.Dp(900), unit.Dp(1000)))
		if err := run(w); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}
