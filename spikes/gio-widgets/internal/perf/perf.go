// Package perf measures what desktop-app §9 budgets: frame rate, layout time
// (p50/p95/max), CPU of this process and resident memory, per scripted phase.
package perf

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
)

// Phase is one scripted measurement window. Idle phases request no frames, so
// they measure what the app costs while nothing changes.
type Phase struct {
	Name string
	Dur  time.Duration
	Idle bool
	// Tick runs once per frame, before layout, and drives the scenario
	// (scroll, stream, toggle). n counts frames inside the phase.
	Tick func(n int)
}

// Runner walks the phases and records them.
type Runner struct {
	phases []Phase
	cur    int
	start  time.Time
	cpu0   time.Duration
	frames int
	last   time.Time
	dts    []float64
	lays   []float64
	rens   []float64
	laying time.Time
	out    io.Writer
	rows   []string
	wake   func()
	armed  bool
}

// NewRunner returns a runner; wake must invalidate the window (safe from any
// goroutine). Results go to out as they complete.
func NewRunner(phases []Phase, wake func(), out io.Writer) *Runner {
	return &Runner{phases: phases, cur: -1, wake: wake, out: out}
}

// Frame must be called at the start of every frame. It reports false once
// every phase has finished, which is when the caller closes the window.
func (r *Runner) Frame(gtx layout.Context) bool {
	now := time.Now()
	if r.cur >= 0 && now.Sub(r.start) >= r.phases[r.cur].Dur {
		r.finish(now)
		r.cur++
		r.begin(now)
	} else if r.cur < 0 {
		r.cur = 0
		r.begin(now)
	}
	if r.cur >= len(r.phases) {
		return false
	}
	p := r.phases[r.cur]
	if !r.last.IsZero() && !p.Idle {
		r.dts = append(r.dts, float64(now.Sub(r.last).Microseconds())/1000)
	}
	r.last = now
	r.frames++
	if p.Tick != nil {
		p.Tick(r.frames)
	}
	if !p.Idle {
		gtx.Execute(op.InvalidateCmd{})
	} else if !r.armed {
		r.armed = true
		d := p.Dur - now.Sub(r.start)
		time.AfterFunc(d+50*time.Millisecond, r.wake)
	}
	return true
}

// Layout times fn as this frame's layout cost.
func (r *Runner) Layout(fn func()) {
	t := time.Now()
	fn()
	r.lays = append(r.lays, float64(time.Since(t).Microseconds())/1000)
}

// Render times fn as this frame's GPU submit (headless runs only).
func (r *Runner) Render(fn func()) {
	t := time.Now()
	fn()
	r.rens = append(r.rens, float64(time.Since(t).Microseconds())/1000)
}

func (r *Runner) begin(now time.Time) {
	r.start, r.cpu0, r.frames, r.last = now, cpuTime(), 0, time.Time{}
	r.dts, r.lays, r.rens, r.armed = r.dts[:0], r.lays[:0], r.rens[:0], false
}

func (r *Runner) finish(now time.Time) {
	p := r.phases[r.cur]
	el := now.Sub(r.start).Seconds()
	cpu := (cpuTime() - r.cpu0).Seconds() / el * 100
	rss, hwm := memKB()
	fps := 0.0
	if !p.Idle {
		fps = float64(r.frames) / el
	}
	row := fmt.Sprintf("| %s | %.0f | %.2f | %.2f | %.2f | %.2f | %.2f | %.1f%% | %d / %d MB |",
		p.Name, fps, pct(r.dts, 50), pct(r.dts, 95), pct(r.lays, 50), pct(r.lays, 95), pct(r.rens, 95), cpu, rss/1024, hwm/1024)
	r.rows = append(r.rows, row)
	fmt.Fprintln(r.out, row)
}

// Header is the markdown table header matching the rows.
const Header = "| phase | fps | frame p50 ms | frame p95 ms | layout p50 ms | layout p95 ms | render p95 ms | CPU (1 core) | RSS / peak |\n|---|---|---|---|---|---|---|---|---|"

func pct(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[min(len(c)-1, int(float64(len(c))*p/100))]
}

func cpuTime() time.Duration {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+2:])
	u, _ := strconv.Atoi(f[11])
	k, _ := strconv.Atoi(f[12])
	return time.Duration(u+k) * 10 * time.Millisecond // USER_HZ is 100
}

func memKB() (rss, hwm int) {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		var dst *int
		switch {
		case strings.HasPrefix(l, "VmRSS:"):
			dst = &rss
		case strings.HasPrefix(l, "VmHWM:"):
			dst = &hwm
		default:
			continue
		}
		*dst, _ = strconv.Atoi(strings.Fields(l)[1])
	}
	return
}
