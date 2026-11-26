package appwin

import (
	"context"
	"time"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
)

// initialLayoutWait is how long the primary window waits for the initial
// appstate snapshot before opening at the default landscape size.
const initialLayoutWait = 500 * time.Millisecond

// awaitInitialAppState buffers startup deliveries from updates until the
// initial appstate snapshot lands or wait expires on clk. Nothing is dropped:
// buffered holds every update read during the wait so the feeder can replay
// them to the registered windows in order.
func awaitInitialAppState(ctx context.Context, updates <-chan hostconn.Update, clk hostconn.Clock, wait time.Duration) ([]hostconn.Update, *client.AppStateWindow) {
	if clk == nil {
		clk = hostconn.RealClock{}
	}
	timer := clk.NewTimer(wait)
	defer timer.Stop()

	var buffered []hostconn.Update
	for {
		select {
		case <-ctx.Done():
			return buffered, nil
		case <-timer.C():
			return buffered, nil
		case u, ok := <-updates:
			if !ok {
				return buffered, nil
			}
			buffered = append(buffered, u)
			if u.Topic == topicAppState && u.Snapshot != nil {
				return buffered, primaryWindowFromSnapshot(u.Snapshot.State)
			}
			if u.State == hostconn.StateIncompatible {
				return buffered, nil
			}
		}
	}
}

func primaryWindowFromSnapshot(state []byte) *client.AppStateWindow {
	snap, err := client.DecodeAppStateSnapshot(state)
	if err != nil {
		return nil
	}
	sorted := sortAppStateWindows(snap.Windows)
	if len(sorted) == 0 {
		return nil
	}
	return &sorted[0]
}

// resolveWindowGeometry picks a new window's opening size (clamped to the
// screen work area when known) and maximized state from its stored record or
// initial doc, falling back to the 1280x800 dp landscape default.
func resolveWindowGeometry(record *client.AppStateWindow, doc *LayoutDoc, area func() (scale, screenW, screenH float64)) WindowGeometry {
	geom := WindowGeometry{WidthDp: requestedW, HeightDp: requestedH, Maximized: false}
	if stored, ok := geometryFromSource(record, doc); ok {
		if stored.WidthDp > 0 && stored.HeightDp > 0 {
			geom.WidthDp = stored.WidthDp
			geom.HeightDp = stored.HeightDp
		}
		geom.Maximized = stored.Maximized
	}
	return clampGeometry(geom, area)
}

func geometryFromSource(record *client.AppStateWindow, doc *LayoutDoc) (WindowGeometry, bool) {
	if record != nil {
		res, err := DecodeLayout(record.Layout)
		if err != nil || res.HigherSchema {
			return WindowGeometry{WidthDp: 0, HeightDp: 0, Maximized: false}, false
		}
		return res.Doc.Window, true
	}
	if doc != nil {
		return doc.Window, true
	}
	return WindowGeometry{WidthDp: 0, HeightDp: 0, Maximized: false}, false
}

func clampGeometry(geom WindowGeometry, area func() (scale, screenW, screenH float64)) WindowGeometry {
	w, h := geom.WidthDp, geom.HeightDp
	if w <= 0 || h <= 0 {
		w, h = requestedW, requestedH
	}
	if area != nil {
		scale, screenW, screenH := area()
		w, h = ClampDp(w, h, scale, screenW, screenH)
	}
	return WindowGeometry{WidthDp: w, HeightDp: h, Maximized: geom.Maximized}
}

// applyWindowOptions applies normal Size first, then Maximized when flagged.
// Before the window driver exists, only Size is queued in initialOpts and the
// caller arms pendingMaximize so Gio stores the normal size before maximizing.
func applyWindowOptions(w *app.Window, geom WindowGeometry) {
	if geom.WidthDp > 0 && geom.HeightDp > 0 {
		w.Option(app.Size(unit.Dp(geom.WidthDp), unit.Dp(geom.HeightDp)))
	}
	if geom.Maximized {
		w.Option(app.Maximized.Option())
	}
}

func applyInitialWindowOptions(w *app.Window, geom WindowGeometry) {
	if geom.WidthDp > 0 && geom.HeightDp > 0 {
		w.Option(app.Size(unit.Dp(geom.WidthDp), unit.Dp(geom.HeightDp)))
	}
}

func (s *screens) seedInitialWindowMode(initGeom WindowGeometry) {
	if !initGeom.Maximized {
		return
	}
	s.pendingMaximize = true
	s.grid.windowMode = app.Maximized
	if s.titleBar != nil {
		s.titleBar.maximized = true
		s.titleBar.deco.Maximized = true
	}
	s.restoreNormalOnUnmaximize = true
	s.restoreNormalSize = initGeom
}

type windowOptioner interface {
	Option(opts ...app.Option)
}

// handleConfigEvent applies a platform ConfigEvent to the title bar and window
// geometry state in event-loop order.
func (s *screens) handleConfigEvent(w windowOptioner, cfg app.Config) {
	if s.restoreMaximizedFromMinimize(w, cfg) {
		s.titleBar.reapplyPlatformChrome()
		return
	}
	s.titleBar.applyConfig(cfg)
	s.handleWindowConfig(w, cfg)
	s.titleBar.reapplyPlatformChrome()
}

// restoreMaximizedFromMinimize works around Gio v0.10.3 on Wayland
// (app/os_wayland.go): Configure case Minimized overwrites Mode=Maximized
// with Minimized, then gio_onSurfaceEnter forces Mode=Windowed on restore
// while gio_onToplevelConfigure ignores xdg_toplevel states. For the first
// Windowed immediately after Minimized when the pre-minimize mode was
// Maximized, keep the maximized window/title-bar state, skip normal restore,
// and re-assert app.Maximized.Option() (a compositor no-op when already
// maximized) so Gio's Mode returns to Maximized.
func (s *screens) restoreMaximizedFromMinimize(w windowOptioner, cfg app.Config) bool {
	if s.grid.windowMode != app.Minimized || s.preMinimizeMode != app.Maximized || cfg.Mode != app.Windowed {
		return false
	}
	s.preMinimizeMode = app.Windowed
	effective := cfg
	effective.Mode = app.Maximized
	s.titleBar.applyConfig(effective)
	s.grid.setWindowMode(app.Maximized)
	if !s.restoreNormalOnUnmaximize {
		if norm, ok := s.normalGeometryForRestore(); ok {
			s.restoreNormalOnUnmaximize = true
			s.restoreNormalSize = norm
			s.sawMaximized = true
			s.maximizedPxSize = cfg.Size
		}
	}
	if w != nil {
		w.Option(app.Maximized.Option())
	}
	return true
}

func (s *screens) normalGeometryForRestore() (WindowGeometry, bool) {
	if s.grid.loaded && s.grid.doc.Window.WidthDp > 0 && s.grid.doc.Window.HeightDp > 0 {
		return s.grid.doc.Window, true
	}
	if s.grid.lastMeasuredSize.X > 0 && s.grid.lastMeasuredSize.Y > 0 {
		return WindowGeometry{
			WidthDp:  s.grid.lastMeasuredSize.X,
			HeightDp: s.grid.lastMeasuredSize.Y,
		}, true
	}
	return WindowGeometry{}, false
}

func (s *screens) handleWindowConfig(w windowOptioner, cfg app.Config) {
	if s.pendingMaximize {
		s.pendingMaximize = false
		if w != nil {
			w.Option(app.Maximized.Option())
		}
		return
	}
	if cfg.Mode == app.Minimized && s.grid.windowMode != app.Minimized {
		s.preMinimizeMode = s.grid.windowMode
	} else if cfg.Mode != app.Minimized {
		s.preMinimizeMode = app.Windowed
	}
	s.trackUnmaximizeRestore(cfg)
	s.grid.setWindowMode(cfg.Mode)
}

func (s *screens) trackUnmaximizeRestore(cfg app.Config) {
	if s.restoreNormalOnUnmaximize {
		switch cfg.Mode {
		case app.Maximized:
			s.sawMaximized = true
			s.maximizedPxSize = cfg.Size
		case app.Windowed:
			if s.sawMaximized {
				s.restoreNormalOnUnmaximize = false
				s.sawMaximized = false
				s.pendingNormalRestore = true
				s.restoreNormalAttempts = 2
			}
		case app.Fullscreen, app.Minimized:
		}
		return
	}
	if cfg.Mode == app.Windowed && s.restoreNormalAttempts > 0 && cfg.Size == s.maximizedPxSize {
		s.pendingNormalRestore = true
	}
}

// applyPendingNormalRestore finishes unmaximizing a window that opened
// maximized: on Wayland the compositor finishes unset_maximized on a
// post-unmaximize configure, so applying Size here lands after its configure.
// Returns true while a restore was just issued so the caller skips recording
// the pre-restore maximized pixel size as the normal size.
func (s *screens) applyPendingNormalRestore(w windowOptioner) bool {
	if !s.pendingNormalRestore {
		return false
	}
	s.pendingNormalRestore = false
	if s.restoreNormalAttempts > 0 {
		s.restoreNormalAttempts--
	}
	if s.restoreNormalSize.WidthDp > 0 && s.restoreNormalSize.HeightDp > 0 {
		w.Option(app.Size(unit.Dp(s.restoreNormalSize.WidthDp), unit.Dp(s.restoreNormalSize.HeightDp)))
		s.titleBar.reapplyPlatformChrome()
	}
	return true
}

// systemPxToDp converts a pixel extent to system dp using the unscaled device
// metric, falling back to dividing out env.UIScale when drawn directly in tests.
func systemPxToDp(gtx *layout.Context, env *kit.Env, device unit.Metric, px int) int {
	pxPerDp := device.PxPerDp
	if pxPerDp <= 0 {
		pxPerDp = gtx.Metric.PxPerDp
		if env != nil && env.UIScale > 0 {
			pxPerDp /= env.UIScale
		}
	}
	if pxPerDp <= 0 {
		return px
	}
	return int(float32(px)/pxPerDp + 0.5)
}
