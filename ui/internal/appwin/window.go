package appwin

import (
	"context"
	"fmt"
	"image/color"
	"sync"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Options are the window's inputs: what to connect to and how it looks.
type Options struct {
	// DataDir locates the host's discovery file.
	DataDir string
	// Params are the app's Hello identity; Run dials with KindApp (DA-01).
	Params client.Params
	// Topics are the event topics the window streams.
	Topics []string
}

// updateQueue is the bridge from the connection goroutines to the window
// goroutine: it buffers deliveries, and each frame drains it. The queue is
// not UI state — the window goroutine turns it into UI state on every frame
// (desktop-app §4). The screens of F10.4 consume the topic payloads; the
// skeleton drains them so the pipeline keeps flowing.
type updateQueue struct {
	mu      sync.Mutex
	pending []hostconn.Update
}

// push appends one update; the window is woken separately.
func (q *updateQueue) push(u hostconn.Update) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pending = append(q.pending, u)
}

// drain hands over every buffered update.
func (q *updateQueue) drain() []hostconn.Update {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.pending
	q.pending = nil
	return out
}

// Run opens the app's one window and runs its event loop until the window is
// destroyed or ctx ends. It owns the hostconn Manager and stops it on return.
func Run(ctx context.Context, opts Options) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	updates := make(chan hostconn.Update, 128)
	m := hostconn.NewManager(&hostconn.Config{
		DataDir:   opts.DataDir,
		AutoStart: true,
		Params:    opts.Params,
		Topics:    opts.Topics,
		Updates:   updates,
	})
	m.Start(runCtx)
	defer m.Stop()

	// The initial size is the preferred one clamped to the work area the
	// screen reports (F10.1: 900x1000 dp overflows an 864 dp-high 1080p
	// screen at scale 1.25); where no work area is known the compositor
	// clamps the granted size instead.
	scale, screenW, screenH := workArea()
	initW, initH := ClampDp(requestedW, requestedH, scale, screenW, screenH)
	w := new(app.Window)
	w.Option(app.Title("Fugue"))
	w.Option(app.Size(unit.Dp(initW), unit.Dp(initH)))

	q := &updateQueue{}
	// The fetcher owns the read side's one goroutine: it runs the blocking
	// reads off the window goroutine and hands the results back (F10.4a).
	fetcher := session.NewFetcher(m.Client)
	fetcher.Start(runCtx)
	defer fetcher.Stop()
	defer stopSideGoroutines(cancel, startFeeder(runCtx, w, updates, q), startCloser(runCtx, w))

	shaper, err := fonts.Shaper()
	if err != nil {
		return fmt.Errorf("build the text shaper: %w", err)
	}
	light, dark := theme.Light(shaper), theme.Dark(shaper)
	return eventLoop(w, m, newScreens(m, fetcher), q, light, dark)
}

// startFeeder runs the update feeder: it moves deliveries into the queue and
// wakes the window. It owns no UI state and ends with the run context; the
// returned channel closes when it has ended.
func startFeeder(ctx context.Context, w *app.Window, updates <-chan hostconn.Update, q *updateQueue) <-chan struct{} {
	feederDone := make(chan struct{})
	go func() {
		defer close(feederDone)
		for {
			select {
			case u := <-updates:
				q.push(u)
				w.Invalidate()
			case <-ctx.Done():
				return
			}
		}
	}()
	return feederDone
}

// startCloser runs the closer: it turns a canceled context into the window's
// close action, so the caller can stop the app. It ends with the run
// context.
func startCloser(ctx context.Context, w *app.Window) <-chan struct{} {
	closerDone := make(chan struct{})
	go func() {
		defer close(closerDone)
		<-ctx.Done()
		w.Perform(system.ActionClose)
	}()
	return closerDone
}

// stopSideGoroutines waits for the feeder and the closer after canceling the
// run context, so Run leaves no goroutine behind: both end on the context.
func stopSideGoroutines(cancel context.CancelFunc, feederDone, closerDone <-chan struct{}) {
	cancel()
	<-feederDone
	<-closerDone
}

// eventLoop is the window's event loop until it is destroyed.
func eventLoop(w *app.Window, m *hostconn.Manager, sc *screens, q *updateQueue, light, dark theme.Theme) error {
	var ops op.Ops
	// The window starts light: Gio v0.10.3 exposes no OS light/dark
	// preference to follow (F10.2c plan), so light is the default and
	// Ctrl+D toggles (DA-08).
	darkMode := false
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			// TODO(F9.9): Ask before closing when active-session counts are available (F10.5).
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			darkMode = frame(gtx, sc, q, light, dark, darkMode)
			e.Frame(gtx.Ops)
		}
	}
}

// frame drains the deliveries and read results into UI state on the window
// goroutine, handles the theme toggle, and lays the window out; it returns
// whether dark mode is on for the next frame.
func frame(gtx layout.Context, sc *screens, q *updateQueue, light, dark theme.Theme, darkMode bool) bool {
	// Deliveries and read results become UI state here, on the window
	// goroutine.
	results := make([]session.Result, 0, 8)
drain:
	for {
		select {
		case r := <-sc.fetcher.Results():
			results = append(results, r)
		default:
			break drain
		}
	}
	sc.update(q.drain(), results)
	for {
		// Letters arrive as their upper-case name on every platform Gio
		// supports.
		ke, ok := gtx.Event(key.Filter{Required: key.ModCtrl, Name: key.Name("D")})
		if !ok {
			break
		}
		if k, isKey := ke.(key.Event); isKey && k.State == key.Press {
			darkMode = !darkMode
		}
	}
	th := light
	if darkMode {
		th = dark
	}
	drawWindow(gtx, th, sc)
	return darkMode
}

// drawWindow lays out the screens — the sidebar and the read-only transcript
// pane (F10.4a) — and the status bar with the connection state (F10.2a).
// Every color comes from the theme tokens (F10.2c); appwin defines none of
// its own.
func drawWindow(gtx layout.Context, th theme.Theme, sc *screens) {
	paint.Fill(gtx.Ops, th.Chrome().AppBg)
	state, reason := sc.m.State()
	status := statusLine(state, reason)
	mt := th.Material()
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			inset := layout.Inset{
				Top: unit.Dp(8), Left: unit.Dp(12), Right: unit.Dp(12),
			}
			return inset.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				sc.layout(gtx, th)
				return layout.Dimensions{Size: gtx.Constraints.Max}
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{
				Left: unit.Dp(12), Right: unit.Dp(12), Bottom: unit.Dp(8),
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				bar := material.Label(mt, unit.Sp(13), status)
				bar.Color = statusColor(th, state)
				return bar.Layout(gtx)
			})
		}),
	)
}

// statusColor maps the connection state onto the theme's status tokens
// (F10.2c): connected is success, connecting is quiet, disconnected is
// danger and an incompatible host is a warning only the version flow can
// resolve.
func statusColor(th theme.Theme, state hostconn.State) color.NRGBA {
	switch state {
	case hostconn.StateConnected:
		return th.Status().Success
	case hostconn.StateConnecting:
		return th.Chrome().FgMuted
	case hostconn.StateDisconnected:
		return th.Status().Danger
	case hostconn.StateIncompatible:
		return th.Status().Warning
	}
	return th.Chrome().Fg
}

// statusLine renders the status bar text; "host not running" is its own
// phrase so the retry (and, from F10.2b, the auto-start) is visible.
func statusLine(state hostconn.State, reason string) string {
	if reason == "" {
		return "connection: " + string(state)
	}
	return "connection: " + string(state) + " (" + reason + ")"
}

// workArea returns the main screen's pixel work area and scale for the clamp.
func workArea() (scale, screenW, screenH float64) {
	w, h, s := WorkArea()
	return s, w, h
}
