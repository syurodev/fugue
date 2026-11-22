package appwin

import (
	"context"
	"fmt"
	"image/color"
	"runtime"
	"sync"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/secrets"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
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
	workers := startAppWorkers(runCtx, m, w)
	defer workers.stop()
	defer stopSideGoroutines(cancel, startFeeder(runCtx, w, updates, q), startCloser(runCtx, w))

	shaper, err := fonts.Shaper()
	if err != nil {
		return fmt.Errorf("build the text shaper: %w", err)
	}
	light, dark := theme.Light(shaper), theme.Dark(shaper)
	env := newEnv(shaper, &light)
	attach(env, w)
	sc := newScreens(m, workers.fetcher, workers.poster, workers.wsMut)
	sc.vaultWorker = workers.vaultW
	sc.inboxWorker = workers.inboxW
	sc.accountsWorker = workers.accountsW
	sc.settingsWorker = workers.settingsW
	sc.secretsWorker = workers.secretsW
	sc.viewerWorker = workers.viewerW
	sc.layoutWorker = workers.layoutW
	// The restored window geometry applies once the appstate snapshot
	// lands; the option reaches the compositor from the window goroutine.
	sc.grid.applyGeometry = func(widthDp, heightDp int) {
		if widthDp > 0 && heightDp > 0 {
			w.Option(app.Size(unit.Dp(widthDp), unit.Dp(heightDp)))
		}
	}
	return eventLoop(w, env, sc, q, &light, &dark)
}

type appWorkers struct {
	fetcher   *session.Fetcher
	poster    *session.Poster
	wsMut     *workspace.Mutator
	vaultW    *vault.Worker
	inboxW    *inbox.Worker
	accountsW *accounts.Worker
	settingsW *settings.Worker
	secretsW  *secrets.Worker
	viewerW   *fileViewWorker
	layoutW   *layoutWorker
}

func startAppWorkers(ctx context.Context, m *hostconn.Manager, w *app.Window) appWorkers {
	fetcher := session.NewFetcher(m.Client)
	fetcher.Start(ctx)
	poster := session.NewPoster(m.Client)
	poster.Start(ctx)
	wsMut := workspace.NewMutator(func() workspace.MutatorClient { return m.Client() })
	wsMut.Start(ctx)
	vaultW := vault.NewWorker(func() vault.Client {
		if c := m.Client(); c != nil {
			return c
		}
		return nil
	})
	vaultW.Start(ctx)
	inboxW := inbox.NewWorker(func() inbox.Client {
		if c := m.Client(); c != nil {
			return c
		}
		return nil
	}, w.Invalidate)
	inboxW.Start(ctx)
	accountsW := accounts.NewWorker(func() accounts.Client {
		if c := m.Client(); c != nil {
			return c
		}
		return nil
	}, w.Invalidate)
	accountsW.Start(ctx)
	settingsW := settings.NewWorker(func() settings.Client {
		if c := m.Client(); c != nil {
			return c
		}
		return nil
	}, w.Invalidate)
	settingsW.Start(ctx)
	secretsW := secrets.NewWorker(func() secrets.Client {
		if c := m.Client(); c != nil {
			return c
		}
		return nil
	}, w.Invalidate)
	secretsW.Start(ctx)
	viewerW := newFileViewWorker(func() fileViewClient {
		if c := m.Client(); c != nil {
			return c
		}
		return nil
	})
	viewerW.Start(ctx)
	layoutW := newLayoutWorker(func() layoutClient {
		if c := m.Client(); c != nil {
			return c
		}
		return nil
	}, w.Invalidate)
	layoutW.Start(ctx)
	return appWorkers{fetcher: fetcher, poster: poster, wsMut: wsMut, vaultW: vaultW, inboxW: inboxW, accountsW: accountsW, settingsW: settingsW, secretsW: secretsW, viewerW: viewerW, layoutW: layoutW}
}

func (a appWorkers) stop() {
	a.layoutW.Stop()
	a.viewerW.Stop()
	a.secretsW.Stop()
	a.settingsW.Stop()
	a.accountsW.Stop()
	a.inboxW.Stop()
	a.vaultW.Stop()
	a.wsMut.Stop()
	a.poster.Stop()
	a.fetcher.Stop()
}

// newEnv builds the window's kit.Env over the theme it starts in (F10.13):
// everything a kit piece draws with lives here, and the window owns it. The
// keyboard notation follows the platform (kitgallery does the same); the UI
// scale starts at the default — the interface-size setting and its source
// are F10.5's — and the motion driver rides in the zero value of the Env.
func newEnv(sh *text.Shaper, light *theme.Theme) *kit.Env {
	env := &kit.Env{Th: light, Sh: sh}
	if runtime.GOOS == "darwin" {
		env.Platform = kit.PlatformMac
	}
	return env
}

// attach wires the env to the window: kit pieces ask for redraws from any
// goroutine through Invalidate — TreeView's async loads and the future async
// kit pieces need it (F10.17b) — and only the window can grant them.
func attach(env *kit.Env, w *app.Window) {
	env.Invalidate = w.Invalidate
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

// eventLoop is the window's event loop until it is destroyed. Every frame
// drives the env the way kitgallery's window does (F10.13): the UI scale on
// the metric, the driver's frame step, the modality and the overlay's window
// size before layout, and the frame-end invalidations after. A destroy while
// a session's turn still runs opens the close confirmation instead (DA-E08,
// F10.5e); the confirmed close re-performs the action past the dialog.
func eventLoop(w *app.Window, env *kit.Env, sc *screens, q *updateQueue, light, dark *theme.Theme) error {
	var ops op.Ops
	sc.failure.closeDlg.requestClose = func() { w.Perform(system.ActionClose) }
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			// The intercept keeps the loop alive so the dialog renders; the
			// destroy is accepted only once nothing asks. Returning e.Err is
			// the loop's way of closing the window, which is why the close
			// action itself cannot live inside requestWindowClose here.
			if !requestWindowClose(sc, func() {}) {
				w.Invalidate()
				continue
			}
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			gtx.Metric = env.Metric(gtx.Metric)
			env.Begin(gtx.Now)
			env.TrackInput(&gtx)
			frame(&gtx, env, sc, q, light, dark)
			env.FrameEnd(&gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// frame drains the deliveries, read results and write results into UI state
// on the window goroutine, handles the theme toggle, and lays the window out.
func frame(gtx *layout.Context, env *kit.Env, sc *screens, q *updateQueue, light, dark *theme.Theme) {
	// Deliveries, read results and write results become UI state here, on
	// the window goroutine.
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
	posts := make([]session.PostResult, 0, 4)
drainPosts:
	for {
		select {
		case p := <-sc.poster.Results():
			posts = append(posts, p)
		default:
			break drainPosts
		}
	}
	sc.update(q.drain(), results, posts, drainMutResults(sc.wsMut))
	sc.drainWorkerResults()
	sc.grid.maybeSave(sc.layoutWorker)
	sc.applyLivePreferences(env, light, dark)
	toggleTheme(gtx, env, sc, light, dark)
	drawWindow(gtx, env, sc)
}

func (s *screens) drainWorkerResults() {
	if s.vaultWorker != nil {
		s.applyVaultResults(drainVaultResults(s.vaultWorker))
	}
	if s.inboxWorker != nil {
		s.applyInboxResults(drainInboxResults(s.inboxWorker))
	}
	if s.accountsWorker != nil {
		s.applyAccountsResults(drainAccountsResults(s.accountsWorker))
	}
	if s.settingsWorker != nil {
		s.applySettingsResults(drainSettingsResults(s.settingsWorker))
	}
	if s.secretsWorker != nil {
		s.applySecretsResults(drainSecretsResults(s.secretsWorker))
	}
	s.applyFileViewResults(drainFileViewResults(s.viewerWorker))
	if s.layoutWorker != nil {
		s.applyLayoutResults(drainLayoutResults(s.layoutWorker))
	}
}

// toggleTheme handles the window's Ctrl+D theme toggle (DA-08). Letters
// arrive as their upper-case name on every platform Gio supports, and the
// window starts light: Gio v0.10.3 exposes no OS light/dark preference to
// follow (F10.2c plan), so light is the default.
func toggleTheme(gtx *layout.Context, env *kit.Env, sc *screens, light, dark *theme.Theme) {
	for {
		ke, ok := gtx.Event(key.Filter{Required: key.ModCtrl, Name: key.Name("D")})
		if !ok {
			return
		}
		if k, isKey := ke.(key.Event); isKey && k.State == key.Press {
			if env.Th == light {
				env.Th = dark
				sc.settings.Prefs.Current.ThemeMode = settings.ThemeDark
			} else {
				env.Th = light
				sc.settings.Prefs.Current.ThemeMode = settings.ThemeLight
			}
		}
	}
}

// drawWindow lays out the screens — the sidebar, the transcript pane and the
// composer (F10.4a, F10.4b, F10.14) — and the status bar with the connection
// state (F10.2a). The open dialog draws last, over everything (UK-07). Every
// color comes from the theme tokens (F10.2c); appwin defines none of its own.
func drawWindow(gtx *layout.Context, env *kit.Env, sc *screens) {
	paint.Fill(gtx.Ops, env.Th.Chrome().AppBg)
	// The window's current size rides every layout save, so the next start
	// opens the same size (host-api §5.2: the geometry is the document's).
	sc.grid.markGeometryDirty(pxToDp(gtx, gtx.Constraints.Max.X), pxToDp(gtx, gtx.Constraints.Max.Y))
	state, reason := sc.m.State()
	status := statusLine(state, reason)
	layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{
				Top: theme.SpaceMD, Left: theme.SpaceLG, Right: theme.SpaceLG,
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				sc.layout(&local, env)
				return layout.Dimensions{Size: gtx.Constraints.Max}
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{
				Left: theme.SpaceLG, Right: theme.SpaceLG, Bottom: theme.SpaceMD,
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						local := gtx
						return kit.Label(env, &local, theme.TypeSmall(), statusColor(env.Th, state), status)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						local := gtx
						return sc.vaultGate.layoutLockButton(&local, env, sc)
					}),
				)
			})
		}),
	)
	// The start and workspace dialogs (F10.4b, F10.14), the active-session
	// close confirmation (F10.5e) draw over the whole window, last, with the
	// full frame's gtx: the scrim dims everything beneath whichever dialog is
	// open.
	sc.composer.start.layout(env, gtx, sc, sc.composer)
	sc.dlg.layout(env, gtx, sc)
	sc.secretsView.layoutDialogs(env, gtx, sc)
	sc.failure.closeDlg.layout(env, gtx, sc)
}

// statusColor maps the connection state onto the theme's status tokens
// (F10.2c): connected is success, connecting is quiet, disconnected is
// danger and an incompatible host is a warning only the version flow can
// resolve.
func statusColor(th *theme.Theme, state hostconn.State) color.NRGBA {
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
