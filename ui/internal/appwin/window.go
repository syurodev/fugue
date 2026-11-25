package appwin

import (
	"context"
	"crypto/rand"
	"image"
	"image/color"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/accounts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/secrets"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/team"
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
	// Clock drives reconnect backoff and the startup layout wait; nil uses RealClock.
	Clock hostconn.Clock
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

// Run opens the app's windows and runs until every window is destroyed or ctx
// ends. It owns the single shared hostconn Manager and stops it on return.
func Run(ctx context.Context, opts Options) error {
	runCtx, cancel := context.WithCancel(ctx)
	updates := make(chan hostconn.Update, 128)
	clk := opts.Clock
	if clk == nil {
		clk = hostconn.RealClock{}
	}
	m := hostconn.NewManager(&hostconn.Config{
		DataDir:   opts.DataDir,
		AutoStart: true,
		Params:    opts.Params,
		Topics:    opts.Topics,
		Updates:   updates,
		Clock:     clk,
	})
	m.Start(runCtx)
	defer m.Stop()

	buffered, primaryRecord := awaitInitialAppState(runCtx, updates, clk, initialLayoutWait)

	var wg sync.WaitGroup
	var coord *multiWindow
	coord = newMultiWindow(m, func(windowID string, record *client.AppStateWindow, doc *LayoutDoc) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if winErr := runWindow(runCtx, m, coord, windowID, record, doc, nil); winErr != nil {
				slog.Error("a secondary window exited with an error", "window_id", windowID, "error", winErr)
			}
		}()
	})
	ready := make(chan struct{})
	feederDone := startFeeder(runCtx, ready, buffered, updates, coord)
	defer func() {
		cancel()
		<-feederDone
	}()

	err := runWindow(runCtx, m, coord, "", primaryRecord, nil, ready)
	wg.Wait()
	return err
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
	teamW     *team.Worker
}

func startAppWorkers(ctx context.Context, m *hostconn.Manager, w *app.Window) appWorkers {
	fetcher := session.NewFetcher(m.Client, w.Invalidate)
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
	return appWorkers{fetcher: fetcher, poster: poster, wsMut: wsMut, vaultW: vaultW, inboxW: inboxW, accountsW: accountsW, settingsW: settingsW, secretsW: secretsW, viewerW: viewerW, layoutW: layoutW, teamW: startTeamWorker(ctx, m, w)}
}

// startTeamWorker gives each window its own team worker, so a window's
// results wake that window only.
func startTeamWorker(ctx context.Context, m *hostconn.Manager, w *app.Window) *team.Worker {
	return team.Start(ctx, func() team.Client {
		if c := m.Client(); c != nil {
			return c
		}
		return nil
	}, w.Invalidate, rand.Text)
}

func (a appWorkers) stop() {
	a.teamW.Stop()
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

// startFeeder runs the update feeder: once ready closes, it replays any
// startup updates buffered during the initial layout wait and then fans
// live deliveries out to every active window through coord.
func startFeeder(ctx context.Context, ready <-chan struct{}, buffered []hostconn.Update, updates <-chan hostconn.Update, coord *multiWindow) <-chan struct{} {
	feederDone := make(chan struct{})
	go func() {
		defer close(feederDone)
		select {
		case <-ready:
		case <-ctx.Done():
			return
		}
		for i := range buffered {
			coord.deliver(buffered[i])
		}
		for {
			select {
			case u := <-updates:
				coord.deliver(u)
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

const layoutFinishTimeout = 2 * time.Second

// eventLoop is the window's event loop until it is destroyed. Every frame
// drives the env the way kitgallery's window does (F10.13): the UI scale on
// the metric, the driver's frame step, the modality and the overlay's window
// size before layout, and the frame-end invalidations after. Closing the last
// window while a session's turn still runs opens the close confirmation
// instead (DA-E08, F10.5e); closing a secondary window removes its stored
// layout without stopping any running turns (desktop-app §5.3, host-api §5.2).
func eventLoop(ctx context.Context, w *app.Window, env *kit.Env, sc *screens, q *updateQueue, coord *multiWindow, winReg *windowUpdates, light, dark *theme.Theme) error {
	var ops op.Ops
	sc.failure.closeDlg.requestClose = func() { w.Perform(system.ActionClose) }
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			isLast := coord == nil || coord.beginClose(winReg)
			if isLast && !requestWindowClose(sc, func() {}) {
				w.Invalidate()
				continue
			}
			finishCtx, finishCancel := context.WithTimeout(ctx, layoutFinishTimeout)
			if finishErr := sc.finishWindowLayout(finishCtx, !isLast); finishErr != nil {
				sc.fail(finishErr)
			}
			finishCancel()
			return e.Err
		case app.ConfigEvent:
			sc.titleBar.applyConfig(e.Config)
			sc.handleWindowConfig(w, e.Config)
		case app.FrameEvent:
			sc.deviceMetric = e.Metric
			gtx := app.NewContext(&ops, e)
			gtx.Metric = env.Metric(gtx.Metric)
			env.Begin(gtx.Now)
			env.TrackInput(&gtx)
			sc.skipGeometry = sc.applyPendingNormalRestore(w)
			frame(&gtx, env, sc, q, coord, light, dark)
			sc.skipGeometry = false
			env.FrameEnd(&gtx)
			e.Frame(gtx.Ops)
			if acts := sc.titleBar.takeActions(); acts != 0 {
				w.Perform(acts)
			}
		}
	}
}

// frame drains the deliveries, read results and write results into UI state
// on the window goroutine, handles the theme toggle, and lays the window out.
func frame(gtx *layout.Context, env *kit.Env, sc *screens, q *updateQueue, coord *multiWindow, light, dark *theme.Theme) {
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
	sc.applyLivePreferences(env, light, dark)
	toggleTheme(gtx, env, sc, light, dark)
	sc.onThemeToggle = func() { sc.applyLivePreferences(env, light, dark) }
	drawWindow(gtx, env, sc)
	if coord != nil {
		sc.openSlotWindow(coord.openNew)
	}
	sc.grid.saveLayoutFrame(gtx, sc.layoutWorker)
}

func (s *screens) drainWorkerResults() {
	s.drainTeamResults()
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
	if !sc.skipGeometry {
		// The window's current size rides every layout save, measured in system
		// dp (before env.UIScale) so restore calls app.Size in the same unit.
		sc.grid.markGeometryDirty(
			systemPxToDp(gtx, env, sc.deviceMetric, gtx.Constraints.Max.X),
			systemPxToDp(gtx, env, sc.deviceMetric, gtx.Constraints.Max.Y),
		)
	}
	barH := sc.titleBar.layout(env, gtx, sc)
	statusH := 0
	if !sc.vaultGate.active(sc.vault) {
		statusH = gtx.Dp(theme.StatusBarHeight)
	}
	totalW, totalH := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	bodyH := max(totalH-barH-statusH, 0)
	env.Offset(gtx, image.Pt(0, barH), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Exact(image.Pt(totalW, bodyH))
		sc.layout(&local, env)
	})
	if statusH > 0 && totalH >= barH+statusH {
		env.Offset(gtx, image.Pt(0, totalH-statusH), func(gtx *layout.Context) {
			local := *gtx
			local.Constraints = layout.Exact(image.Pt(totalW, statusH))
			sc.layoutStatusBar(&local, env)
		})
	}
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
	if state == "" {
		return ""
	}
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
