package appwin

import (
	"bytes"
	"encoding/json"
	"image"
	"sync"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
)

type geomFakeClock struct {
	mu     sync.Mutex
	armed  chan time.Duration
	timers []*geomFakeTimer
}

func newGeomFakeClock() *geomFakeClock {
	return &geomFakeClock{armed: make(chan time.Duration, 8)}
}

func (c *geomFakeClock) NewTimer(d time.Duration) hostconn.Timer {
	t := &geomFakeTimer{ch: make(chan time.Time, 1)}
	c.mu.Lock()
	c.timers = append(c.timers, t)
	c.mu.Unlock()
	c.armed <- d
	return t
}

func (c *geomFakeClock) fireLast() {
	c.mu.Lock()
	t := c.timers[len(c.timers)-1]
	c.mu.Unlock()
	t.fire()
}

func (c *geomFakeClock) lastStopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.timers[len(c.timers)-1]
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

type geomFakeTimer struct {
	mu      sync.Mutex
	ch      chan time.Time
	stopped bool
	fired   bool
}

func (t *geomFakeTimer) C() <-chan time.Time { return t.ch }

func (t *geomFakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasPending := !t.stopped && !t.fired
	t.stopped = true
	return wasPending
}

func (t *geomFakeTimer) fire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.fired {
		return
	}
	t.fired = true
	t.ch <- time.Unix(1, 0)
}

func drawTestWindowFrame(env *kit.Env, sc *screens, device unit.Metric, sizePx image.Point) {
	var ops op.Ops
	router := new(input.Router)
	sc.deviceMetric = device
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      env.Metric(device),
		Constraints: layout.Exact(sizePx),
		Source:      router.Source(),
		Now:         time.Unix(1_700_000_000, 0),
	}
	env.Begin(gtx.Now)
	env.TrackInput(&gtx)
	drawWindow(&gtx, env, sc)
	env.FrameEnd(&gtx)
}

func snapshotWithWindow(t *testing.T, geom WindowGeometry) []byte {
	t.Helper()
	doc := DefaultLayout()
	doc.Window = geom
	fake := newFakeLayoutClient()
	fake.seed(doc)
	return snapshotPayload(t, fake)
}

func decodeSnapshotPayload(t *testing.T, raw []byte) client.AppStateSnapshot {
	t.Helper()
	var snap client.AppStateSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

// TestWindowDefaultSizeAndMonitorClamp pins FUG-83's landscape 1280x800 dp
// default and clamping of stored sizes larger than a smaller screen.
func TestWindowDefaultSizeAndMonitorClamp(t *testing.T) {
	t.Parallel()
	if requestedW != 1280 || requestedH != 800 {
		t.Fatalf("default = %dx%d, want 1280x800", requestedW, requestedH)
	}
	// Unknown work area (Linux): default stays 1280x800.
	gotLinux := resolveWindowGeometry(nil, nil, func() (float64, float64, float64) { return 0, 0, 0 })
	wantLinux := WindowGeometry{WidthDp: 1280, HeightDp: 800, Maximized: false}
	if !cmp.Equal(gotLinux, wantLinux) {
		t.Fatalf("unknown work area default = %+v, want %+v", gotLinux, wantLinux)
	}

	raw, err := EncodeLayout(LayoutDoc{
		SchemaVersion: LayoutSchemaVersion,
		Columns:       DefaultLayout().Columns,
		Window:        WindowGeometry{WidthDp: 1600, HeightDp: 1000, Maximized: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := &client.AppStateWindow{WindowID: primaryWindowID, Revision: 1, Layout: raw}
	gotClamped := resolveWindowGeometry(rec, nil, func() (float64, float64, float64) { return 2, 2560, 1440 })
	wantClamped := WindowGeometry{WidthDp: 1280, HeightDp: 720, Maximized: true}
	if !cmp.Equal(gotClamped, wantClamped) {
		t.Fatalf("clamped stored geometry = %+v, want %+v", gotClamped, wantClamped)
	}
}

// TestWindowGeometryRoundTripAtUIScale125 pins that saving, restoring, and
// re-saving at UIScale 1.25 measures in system dp and never shrinks or grows.
func TestWindowGeometryRoundTripAtUIScale125(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		device unit.Metric
	}{
		{"1x backing scale", unit.Metric{PxPerDp: 1, PxPerSp: 1}},
		{"2x backing scale", unit.Metric{PxPerDp: 2, PxPerSp: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verifyUIScale125RoundTrip(t, tc.device, 1280, 800)
		})
	}
}

func verifyUIScale125RoundTrip(t *testing.T, device unit.Metric, wantW, wantH int) {
	t.Helper()
	env1 := newInboxTestEnv(t)
	env1.UIScale = 1.25
	sc1, _ := newTestScreens(t)
	sc1.applyWindowRecord(nil)

	sizePx := image.Pt(int(float32(wantW)*device.PxPerDp), int(float32(wantH)*device.PxPerDp))
	drawTestWindowFrame(env1, sc1, device, sizePx)

	raw, err := EncodeLayout(sc1.grid.doc)
	if err != nil {
		t.Fatal(err)
	}
	res, err := DecodeLayout(raw)
	if err != nil {
		t.Fatal(err)
	}
	wantGeom := WindowGeometry{WidthDp: wantW, HeightDp: wantH, Maximized: false}
	if !cmp.Equal(res.Doc.Window, wantGeom) {
		t.Fatalf("first save = %+v, want %+v", res.Doc.Window, wantGeom)
	}

	env2 := newInboxTestEnv(t)
	env2.UIScale = 1.25
	sc2, _ := newTestScreens(t)
	var applied WindowGeometry
	sc2.grid.applyGeometry = func(g WindowGeometry) { applied = g }
	sc2.applyWindowRecord(&client.AppStateWindow{WindowID: primaryWindowID, Revision: 2, Layout: raw})
	if !cmp.Equal(applied, wantGeom) {
		t.Fatalf("restored geometry = %+v, want %+v", applied, wantGeom)
	}

	restoredPx := image.Pt(int(float32(applied.WidthDp)*device.PxPerDp), int(float32(applied.HeightDp)*device.PxPerDp))
	drawTestWindowFrame(env2, sc2, device, restoredPx)
	if !cmp.Equal(sc2.grid.doc.Window, wantGeom) {
		t.Fatalf("second save = %+v, want %+v", sc2.grid.doc.Window, wantGeom)
	}
}

// TestWindowMaximizePreservesNormalSizeAndRestoresIt pins that maximizing
// keeps the last normal size in WindowGeometry and unmaximizing returns to it.
func TestWindowMaximizePreservesNormalSizeAndRestoresIt(t *testing.T) {
	t.Parallel()
	env := newInboxTestEnv(t)
	sc, _ := newTestScreens(t)
	sc.applyWindowRecord(nil)
	device := unit.Metric{PxPerDp: 1, PxPerSp: 1}

	drawTestWindowFrame(env, sc, device, image.Pt(1180, 760))
	if got := sc.grid.doc.Window; got != (WindowGeometry{WidthDp: 1180, HeightDp: 760, Maximized: false}) {
		t.Fatalf("normal geometry = %+v", got)
	}

	sc.grid.setWindowMode(app.Maximized)
	drawTestWindowFrame(env, sc, device, image.Pt(1920, 1048))
	wantMax := WindowGeometry{WidthDp: 1180, HeightDp: 760, Maximized: true}
	if !cmp.Equal(sc.grid.doc.Window, wantMax) {
		t.Fatalf("maximized geometry overwrote normal size: got %+v, want %+v", sc.grid.doc.Window, wantMax)
	}

	raw, err := EncodeLayout(sc.grid.doc)
	if err != nil {
		t.Fatal(err)
	}
	rec := &client.AppStateWindow{WindowID: primaryWindowID, Revision: 4, Layout: raw}
	initGeom := resolveWindowGeometry(rec, nil, func() (float64, float64, float64) { return 0, 0, 0 })
	if !cmp.Equal(initGeom, wantMax) {
		t.Fatalf("reopen initial geometry = %+v, want %+v", initGeom, wantMax)
	}

	sc2, _ := newTestScreens(t)
	sc2.applyWindowRecord(rec)
	sc2.seedInitialWindowMode(initGeom)
	sc2.grid.setWindowMode(app.Windowed)
	drawTestWindowFrame(env, sc2, device, image.Pt(initGeom.WidthDp, initGeom.HeightDp))
	wantUnmax := WindowGeometry{WidthDp: 1180, HeightDp: 760, Maximized: false}
	if !cmp.Equal(sc2.grid.doc.Window, wantUnmax) {
		t.Fatalf("after unmaximize = %+v, want %+v", sc2.grid.doc.Window, wantUnmax)
	}
}

// TestDecodeLayoutOldDocWithoutMaximized pins backward compatibility with old
// layout documents that predate the maximized field, and omitempty when false.
func TestDecodeLayoutOldDocWithoutMaximized(t *testing.T) {
	t.Parallel()
	oldRaw := []byte(`{"schema_version":1,"columns":[{"width_weight":1,"slots":[{"height_weight":1,"kind":"session"}]}],"window":{"width_dp":1536,"height_dp":832}}`)
	res, err := DecodeLayout(oldRaw)
	if err != nil {
		t.Fatalf("DecodeLayout(old) failed: %v", err)
	}
	want := WindowGeometry{WidthDp: 1536, HeightDp: 832, Maximized: false}
	if !cmp.Equal(res.Doc.Window, want) {
		t.Fatalf("decoded window = %+v, want %+v", res.Doc.Window, want)
	}

	plainBytes, err := EncodeLayout(res.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plainBytes, []byte("maximized")) {
		t.Fatalf("false maximized was not omitted: %s", plainBytes)
	}
	res.Doc.Window.Maximized = true
	maxBytes, err := EncodeLayout(res.Doc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(maxBytes, []byte(`"maximized":true`)) {
		t.Fatalf("maximized flag missing in wire JSON: %s", maxBytes)
	}
	resMax, err := DecodeLayout(maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Equal(resMax.Doc.Window, res.Doc.Window) {
		t.Fatalf("round-trip maximized = %+v, want %+v", resMax.Doc.Window, res.Doc.Window)
	}
}

type waitTestResult struct {
	buffered []hostconn.Update
	primary  *client.AppStateWindow
}

// TestStartupLayoutWaitAppliesRecordInTime pins that a snapshot arriving before
// the wait timer fires is applied as the initial window geometry from frame 0.
func TestStartupLayoutWaitAppliesRecordInTime(t *testing.T) {
	t.Parallel()
	clk := newGeomFakeClock()
	updates := make(chan hostconn.Update, 4)
	wantGeom := WindowGeometry{WidthDp: 1420, HeightDp: 860, Maximized: true}
	snapBytes := snapshotWithWindow(t, wantGeom)

	done := make(chan waitTestResult, 1)
	go func() {
		b, p := awaitInitialAppState(t.Context(), updates, clk, initialLayoutWait)
		done <- waitTestResult{buffered: b, primary: p}
	}()

	if d := <-clk.armed; d != initialLayoutWait {
		t.Fatalf("wait duration = %v, want %v", d, initialLayoutWait)
	}
	updates <- hostconn.Update{State: hostconn.StateConnected}
	updates <- hostconn.Update{
		Topic:    topicAppState,
		Snapshot: &client.Snapshot{Sequence: 1, State: snapBytes},
	}

	res := <-done
	if len(res.buffered) != 2 {
		t.Fatalf("buffered = %d updates, want 2", len(res.buffered))
	}
	if res.primary == nil {
		t.Fatal("primary record was nil despite snapshot arriving in time")
	}
	if !clk.lastStopped() {
		t.Fatal("startup wait timer was not stopped after snapshot arrived")
	}
	got := resolveWindowGeometry(res.primary, nil, func() (float64, float64, float64) { return 0, 0, 0 })
	if !cmp.Equal(got, wantGeom) {
		t.Fatalf("resolved geometry = %+v, want %+v", got, wantGeom)
	}
}

// TestStartupLayoutWaitTimeoutUsesDefault pins that firing the wait timer
// before any snapshot arrives opens the window at the 1280x800 dp default.
func TestStartupLayoutWaitTimeoutUsesDefault(t *testing.T) {
	t.Parallel()
	clk := newGeomFakeClock()
	updates := make(chan hostconn.Update, 4)

	done := make(chan waitTestResult, 1)
	go func() {
		b, p := awaitInitialAppState(t.Context(), updates, clk, initialLayoutWait)
		done <- waitTestResult{buffered: b, primary: p}
	}()

	<-clk.armed
	clk.fireLast()

	res := <-done
	if res.primary != nil {
		t.Fatalf("expected nil primary record on timeout, got %+v", res.primary)
	}
	got := resolveWindowGeometry(res.primary, nil, func() (float64, float64, float64) { return 0, 0, 0 })
	want := WindowGeometry{WidthDp: requestedW, HeightDp: requestedH, Maximized: false}
	if !cmp.Equal(got, want) {
		t.Fatalf("timeout default geometry = %+v, want %+v", got, want)
	}
}

// TestStartupLateRecordAppliesWhenUnresized pins that a late snapshot still
// restores size and maximized state if the user has not resized yet.
func TestStartupLateRecordAppliesWhenUnresized(t *testing.T) {
	t.Parallel()
	env := newInboxTestEnv(t)
	sc, _ := newTestScreens(t)
	var applied []WindowGeometry
	sc.grid.applyGeometry = func(g WindowGeometry) { applied = append(applied, g) }

	drawTestWindowFrame(env, sc, unit.Metric{PxPerDp: 1, PxPerSp: 1}, image.Pt(1280, 800))

	wantGeom := WindowGeometry{WidthDp: 1400, HeightDp: 850, Maximized: true}
	snap := decodeSnapshotPayload(t, snapshotWithWindow(t, wantGeom))
	sc.applyLayoutSnapshot(&snap)
	if len(applied) != 1 || !cmp.Equal(applied[0], wantGeom) {
		t.Fatalf("late applyGeometry calls = %+v, want [%+v]", applied, wantGeom)
	}
}

// TestStartupLateRecordDoesNotFightUserResize pins that a late snapshot never
// overrides a size or maximized change the user already made after timeout.
func TestStartupLateRecordDoesNotFightUserResize(t *testing.T) {
	t.Parallel()
	env := newInboxTestEnv(t)
	sc, _ := newTestScreens(t)
	var applied []WindowGeometry
	sc.grid.applyGeometry = func(g WindowGeometry) { applied = append(applied, g) }
	device := unit.Metric{PxPerDp: 1, PxPerSp: 1}

	drawTestWindowFrame(env, sc, device, image.Pt(1280, 800))
	drawTestWindowFrame(env, sc, device, image.Pt(1000, 700))

	storedGeom := WindowGeometry{WidthDp: 1500, HeightDp: 920, Maximized: true}
	snap := decodeSnapshotPayload(t, snapshotWithWindow(t, storedGeom))
	sc.applyLayoutSnapshot(&snap)
	if len(applied) != 0 {
		t.Fatalf("late record overwrote user resize: applyGeometry called with %+v", applied)
	}
	wantUser := WindowGeometry{WidthDp: 1000, HeightDp: 700, Maximized: false}
	if !cmp.Equal(sc.grid.doc.Window, wantUser) {
		t.Fatalf("stored user window = %+v, want %+v", sc.grid.doc.Window, wantUser)
	}
}

type recordingWindow struct {
	configs []app.Config
}

func (r *recordingWindow) Option(opts ...app.Option) {
	metric := unit.Metric{PxPerDp: 1, PxPerSp: 1}
	for _, opt := range opts {
		var cfg app.Config
		opt(metric, &cfg)
		r.configs = append(r.configs, cfg)
	}
}

func (r *recordingWindow) clear() {
	r.configs = nil
}

func (r *recordingWindow) sizeOptions() []image.Point {
	var sizes []image.Point
	for _, cfg := range r.configs {
		if cfg.Size != (image.Point{}) {
			sizes = append(sizes, cfg.Size)
		}
	}
	return sizes
}

func (r *recordingWindow) hasModeOption(mode app.WindowMode) bool {
	for _, cfg := range r.configs {
		if cfg.Mode == mode && cfg.Size == (image.Point{}) {
			return true
		}
	}
	return false
}

func runTestWindowFrame(env *kit.Env, sc *screens, w windowOptioner, device unit.Metric, sizePx image.Point) {
	sc.skipGeometry = sc.applyPendingNormalRestore(w)
	drawTestWindowFrame(env, sc, device, sizePx)
	sc.skipGeometry = false
}

func newMaximizedRecord(t *testing.T, geom WindowGeometry) *client.AppStateWindow {
	t.Helper()
	doc := DefaultLayout()
	doc.Window = geom
	raw, err := EncodeLayout(doc)
	if err != nil {
		t.Fatal(err)
	}
	return &client.AppStateWindow{WindowID: primaryWindowID, Revision: 2, Layout: raw}
}

// TestWindowRestoreFromMinimizeOpenedMaximized pins FUG-98 (a): restoring a
// window that opened maximized from minimize keeps Maximized=true, keeps the
// title bar in restore state, avoids app.Size, and re-asserts app.Maximized.
func TestWindowRestoreFromMinimizeOpenedMaximized(t *testing.T) {
	t.Parallel()
	env := newInboxTestEnv(t)
	sc, _ := newTestScreens(t)
	w := &recordingWindow{}
	device := unit.Metric{PxPerDp: 1, PxPerSp: 1}
	wantGeom := WindowGeometry{WidthDp: 1180, HeightDp: 760, Maximized: true}

	rec := newMaximizedRecord(t, wantGeom)
	sc.applyWindowRecord(rec)
	sc.seedInitialWindowMode(wantGeom)
	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1180, 760), Focused: true})
	sc.handleConfigEvent(w, app.Config{Mode: app.Maximized, Size: image.Pt(1920, 1048), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))
	w.clear()

	sc.handleConfigEvent(w, app.Config{Mode: app.Minimized, Size: image.Pt(1920, 1048)})
	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1920, 1048), Focused: true})

	if sc.pendingNormalRestore {
		t.Fatal("pendingNormalRestore armed on minimize restore")
	}
	if sizes := w.sizeOptions(); len(sizes) != 0 {
		t.Fatalf("unexpected app.Size options on minimize restore: %+v", sizes)
	}
	if !w.hasModeOption(app.Maximized) {
		t.Fatalf("expected app.Maximized.Option() after minimize restore, got %+v", w.configs)
	}
	if !sc.titleBar.maximized {
		t.Fatal("titleBar.maximized = false, want true")
	}
	if !cmp.Equal(sc.grid.doc.Window, wantGeom) {
		t.Fatalf("doc.Window = %+v, want %+v", sc.grid.doc.Window, wantGeom)
	}

	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))
	if sizes := w.sizeOptions(); len(sizes) != 0 {
		t.Fatalf("unexpected app.Size option on frame during minimize restore: %+v", sizes)
	}
	if !cmp.Equal(sc.grid.doc.Window, wantGeom) {
		t.Fatalf("frame during restore overwrote doc.Window: got %+v, want %+v", sc.grid.doc.Window, wantGeom)
	}
}

// TestWindowRestoreFromMinimizeMaximizedDuringSession pins FUG-98 (b):
// maximizing during a session, minimizing, and restoring keeps Maximized=true,
// titleBar.maximized=true, normal size intact, and re-asserts app.Maximized.
func TestWindowRestoreFromMinimizeMaximizedDuringSession(t *testing.T) {
	t.Parallel()
	env := newInboxTestEnv(t)
	sc, _ := newTestScreens(t)
	sc.applyWindowRecord(nil)
	w := &recordingWindow{}
	device := unit.Metric{PxPerDp: 1, PxPerSp: 1}

	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1180, 760), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1180, 760))
	sc.handleConfigEvent(w, app.Config{Mode: app.Maximized, Size: image.Pt(1920, 1048), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))
	w.clear()

	sc.handleConfigEvent(w, app.Config{Mode: app.Minimized, Size: image.Pt(1920, 1048)})
	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1920, 1048), Focused: true})

	wantGeom := WindowGeometry{WidthDp: 1180, HeightDp: 760, Maximized: true}
	if sc.pendingNormalRestore {
		t.Fatal("pendingNormalRestore armed on minimize restore")
	}
	if sizes := w.sizeOptions(); len(sizes) != 0 {
		t.Fatalf("unexpected app.Size options on minimize restore: %+v", sizes)
	}
	if !w.hasModeOption(app.Maximized) {
		t.Fatalf("expected app.Maximized.Option() after minimize restore, got %+v", w.configs)
	}
	if !sc.titleBar.maximized {
		t.Fatal("titleBar.maximized = false, want true")
	}
	if !cmp.Equal(sc.grid.doc.Window, wantGeom) {
		t.Fatalf("doc.Window = %+v, want %+v", sc.grid.doc.Window, wantGeom)
	}
}

// TestWindowRestoreFromMinimizeNormalWindow pins FUG-98 (c): minimizing and
// restoring a normal windowed window sends no options and keeps Windowed mode.
func TestWindowRestoreFromMinimizeNormalWindow(t *testing.T) {
	t.Parallel()
	env := newInboxTestEnv(t)
	sc, _ := newTestScreens(t)
	sc.applyWindowRecord(nil)
	w := &recordingWindow{}
	device := unit.Metric{PxPerDp: 1, PxPerSp: 1}

	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1180, 760), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1180, 760))
	w.clear()

	sc.handleConfigEvent(w, app.Config{Mode: app.Minimized, Size: image.Pt(1180, 760)})
	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1180, 760), Focused: true})

	if len(w.configs) != 0 {
		t.Fatalf("unexpected window options on normal minimize restore: %+v", w.configs)
	}
	if sc.grid.windowMode != app.Windowed {
		t.Fatalf("windowMode = %v, want Windowed", sc.grid.windowMode)
	}
	if sc.titleBar.maximized {
		t.Fatal("titleBar.maximized = true, want false")
	}
	wantGeom := WindowGeometry{WidthDp: 1180, HeightDp: 760, Maximized: false}
	if !cmp.Equal(sc.grid.doc.Window, wantGeom) {
		t.Fatalf("doc.Window = %+v, want %+v", sc.grid.doc.Window, wantGeom)
	}
}

// TestWindowUnmaximizeAfterMinimizeRestoreRestoresNormalSize pins FUG-98 (d)
// and (e): after restoring a maximized window from minimize, a real unmaximize
// restores the saved normal size without intermediate frames overwriting it.
func TestWindowUnmaximizeAfterMinimizeRestoreRestoresNormalSize(t *testing.T) {
	t.Parallel()
	env := newInboxTestEnv(t)
	sc, _ := newTestScreens(t)
	w := &recordingWindow{}
	device := unit.Metric{PxPerDp: 1, PxPerSp: 1}
	initGeom := WindowGeometry{WidthDp: 1180, HeightDp: 760, Maximized: true}

	sc.applyWindowRecord(newMaximizedRecord(t, initGeom))
	sc.seedInitialWindowMode(initGeom)
	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1180, 760), Focused: true})
	sc.handleConfigEvent(w, app.Config{Mode: app.Maximized, Size: image.Pt(1920, 1048), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))
	w.clear()

	sc.handleConfigEvent(w, app.Config{Mode: app.Minimized, Size: image.Pt(1920, 1048)})
	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1920, 1048), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))
	if sc.pendingNormalRestore || len(w.sizeOptions()) != 0 || !w.hasModeOption(app.Maximized) {
		t.Fatalf("minimize restore misfired: pending=%v configs=%+v", sc.pendingNormalRestore, w.configs)
	}
	if !cmp.Equal(sc.grid.doc.Window, initGeom) {
		t.Fatalf("after minimize restore doc.Window = %+v, want %+v", sc.grid.doc.Window, initGeom)
	}
	sc.handleConfigEvent(w, app.Config{Mode: app.Maximized, Size: image.Pt(1920, 1048), Focused: true})
	w.clear()

	// Real user un-maximize (Windowed following Maximized).
	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1920, 1048), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))

	wantSizes := []image.Point{image.Pt(1180, 760)}
	if !cmp.Equal(w.sizeOptions(), wantSizes) {
		t.Fatalf("sizeOptions = %+v, want %+v", w.sizeOptions(), wantSizes)
	}
	if sc.titleBar.maximized {
		t.Fatal("titleBar.maximized = true after real unmaximize, want false")
	}
	wantUnmax := WindowGeometry{WidthDp: 1180, HeightDp: 760, Maximized: false}
	if !cmp.Equal(sc.grid.doc.Window, wantUnmax) {
		t.Fatalf("pre-resize restore frame overwrote doc.Window: got %+v, want %+v", sc.grid.doc.Window, wantUnmax)
	}

	runTestWindowFrame(env, sc, w, device, image.Pt(1180, 760))
	if !cmp.Equal(sc.grid.doc.Window, wantUnmax) {
		t.Fatalf("post-restore doc.Window = %+v, want %+v", sc.grid.doc.Window, wantUnmax)
	}
}

// TestWindowRestoreFramesDoNotOverwriteNormalSize pins FUG-98 (e): frames at
// the maximized pixel size during minimize recovery or unmaximize restore
// never overwrite the saved normal size in doc.Window.
func TestWindowRestoreFramesDoNotOverwriteNormalSize(t *testing.T) {
	t.Parallel()
	env := newInboxTestEnv(t)
	sc, _ := newTestScreens(t)
	sc.applyWindowRecord(nil)
	w := &recordingWindow{}
	device := unit.Metric{PxPerDp: 1, PxPerSp: 1}

	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1180, 760), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1180, 760))
	sc.handleConfigEvent(w, app.Config{Mode: app.Maximized, Size: image.Pt(1920, 1048), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))

	sc.handleConfigEvent(w, app.Config{Mode: app.Minimized, Size: image.Pt(1920, 1048)})
	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1920, 1048), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))
	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))

	wantMax := WindowGeometry{WidthDp: 1180, HeightDp: 760, Maximized: true}
	if !cmp.Equal(sc.grid.doc.Window, wantMax) {
		t.Fatalf("frames during minimize restore overwrote doc.Window: got %+v, want %+v", sc.grid.doc.Window, wantMax)
	}

	sc.handleConfigEvent(w, app.Config{Mode: app.Maximized, Size: image.Pt(1920, 1048), Focused: true})
	w.clear()
	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1920, 1048), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))
	// Second pre-resize configure/frame at maximized pixel size while restoreNormalAttempts > 0.
	sc.handleConfigEvent(w, app.Config{Mode: app.Windowed, Size: image.Pt(1920, 1048), Focused: true})
	runTestWindowFrame(env, sc, w, device, image.Pt(1920, 1048))

	wantUnmax := WindowGeometry{WidthDp: 1180, HeightDp: 760, Maximized: false}
	if !cmp.Equal(sc.grid.doc.Window, wantUnmax) {
		t.Fatalf("frames during unmaximize restore overwrote doc.Window: got %+v, want %+v", sc.grid.doc.Window, wantUnmax)
	}
	if sizes := w.sizeOptions(); len(sizes) != 2 || sizes[0] != image.Pt(1180, 760) {
		t.Fatalf("expected app.Size(1180, 760) restore options, got %+v", sizes)
	}
}
