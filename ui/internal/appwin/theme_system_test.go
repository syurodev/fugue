package appwin

import (
	"context"
	"image"
	"sync"
	"sync/atomic"
	"testing"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/osappearance"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

type fakeAppearanceSource struct {
	mu     sync.Mutex
	cur    osappearance.Appearance
	notify chan struct{}
}

func newFakeAppearanceSource(initial osappearance.Appearance) *fakeAppearanceSource {
	return &fakeAppearanceSource{cur: initial, notify: make(chan struct{}, 16)}
}

func (f *fakeAppearanceSource) Current() osappearance.Appearance {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur
}

func (f *fakeAppearanceSource) Set(next osappearance.Appearance) {
	f.mu.Lock()
	changed := f.cur != next
	f.cur = next
	f.mu.Unlock()
	if changed {
		select {
		case f.notify <- struct{}{}:
		default:
		}
	}
}

func (f *fakeAppearanceSource) Watch(ctx context.Context, onChange func(osappearance.Appearance)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.notify:
			if onChange != nil {
				onChange(f.Current())
			}
		}
	}
}

func newAppearanceTestScreens(t *testing.T, src osappearance.Source, mode settings.ThemeMode) (*screens, *kit.Env, *theme.Theme, *theme.Theme) {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-appearance"})
	sc.osAppearance = src
	sc.settings.Prefs.Loaded = true
	sc.settings.Prefs.Current = settings.Preferences{ThemeMode: mode, UIScale: settings.Scale100}

	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	light, dark := theme.Light(sh), theme.Dark(sh)
	env := &kit.Env{Th: &light, Sh: sh, ReducedMotion: true}
	return sc, env, &light, &dark
}

func waitWindowInvalidation(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-t.Context().Done():
		t.Fatalf("timed out waiting for window invalidation: %v", t.Context().Err())
	}
}

func registerCountingWindow(coord *multiWindow) (*atomic.Int32, <-chan struct{}) {
	var count atomic.Int32
	ch := make(chan struct{}, 8)
	coord.register(&windowUpdates{
		queue: &updateQueue{},
		invalidate: func() {
			count.Add(1)
			ch <- struct{}{}
		},
	})
	return &count, ch
}

// TestSystemThemeFollowsOSAppearance verifies that System theme follows OS
// light -> dark -> light transitions and invalidates every registered window.
func TestSystemThemeFollowsOSAppearance(t *testing.T) {
	t.Parallel()
	fake := newFakeAppearanceSource(osappearance.Light)
	coord := newMultiWindow(nil, nil)
	inv1, invCh1 := registerCountingWindow(coord)
	inv2, invCh2 := registerCountingWindow(coord)

	watchCtx, cancel := context.WithCancel(t.Context())
	watchDone := startAppearanceWatcher(watchCtx, fake, coord)
	t.Cleanup(func() {
		cancel()
		<-watchDone
	})

	sc1, env1, light1, dark1 := newAppearanceTestScreens(t, fake, settings.ThemeSystem)
	sc2, env2, light2, dark2 := newAppearanceTestScreens(t, fake, settings.ThemeSystem)

	for _, step := range []struct {
		next     osappearance.Appearance
		wantDark bool
		wantInv  int32
	}{
		{osappearance.Light, false, 0},
		{osappearance.Dark, true, 1},
		{osappearance.Light, false, 2},
	} {
		if step.wantInv > 0 {
			fake.Set(step.next)
			waitWindowInvalidation(t, invCh1)
			waitWindowInvalidation(t, invCh2)
		}
		sc1.applyLivePreferences(env1, light1, dark1)
		sc2.applyLivePreferences(env2, light2, dark2)
		want1, want2 := light1, light2
		if step.wantDark {
			want1, want2 = dark1, dark2
		}
		if env1.Th != want1 || env2.Th != want2 {
			t.Fatalf("OS %v: env1=%p (want %p) env2=%p (want %p)", step.next, env1.Th, want1, env2.Th, want2)
		}
		if inv1.Load() != step.wantInv || inv2.Load() != step.wantInv {
			t.Fatalf("OS %v invalidations = (%d, %d), want %d", step.next, inv1.Load(), inv2.Load(), step.wantInv)
		}
	}

	cancel()
	select {
	case <-watchDone:
	case <-t.Context().Done():
		t.Fatalf("appearance watcher did not stop on cancel: %v", t.Context().Err())
	}
}

func measureThemeModeControlRuns(env *kit.Env, sc *screens) (dims layout.Dimensions, runCount int) {
	var ops op.Ops
	var runs []image.Rectangle
	env.RunRecorder = func(r image.Rectangle) { runs = append(runs, r) }
	defer func() { env.RunRecorder = nil }()
	router := new(input.Router)
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(500, 300)),
		Source:      router.Source(),
	}
	env.Begin(gtx.Now)
	dims = sc.settingsView.layoutThemeModeControl(&gtx, env, sc, sc.settings.Prefs.Current)
	env.FrameEnd(&gtx)
	router.Frame(&ops)
	return dims, len(runs)
}

// TestSystemThemeUnknownFallsBackToLight verifies that Unknown OS appearance
// resolves System mode to light and displays the Settings helper text only when
// Unknown.
func TestSystemThemeUnknownFallsBackToLight(t *testing.T) {
	t.Parallel()
	fake := newFakeAppearanceSource(osappearance.Unknown)
	sc, env, light, dark := newAppearanceTestScreens(t, fake, settings.ThemeSystem)

	sc.applyLivePreferences(env, light, dark)
	if env.Th != light {
		t.Fatalf("System + Unknown resolved to %p, want light %p", env.Th, light)
	}

	unknownDims, unknownRuns := measureThemeModeControlRuns(env, sc)

	fake.Set(osappearance.Light)
	knownDims, knownRuns := measureThemeModeControlRuns(env, sc)

	if unknownRuns <= knownRuns {
		t.Fatalf("Unknown state text runs = %d, want > known state %d (helper text missing)", unknownRuns, knownRuns)
	}
	if unknownDims.Size.Y <= knownDims.Size.Y {
		t.Fatalf("Unknown state height = %d, want > known state %d (helper text did not render below control)", unknownDims.Size.Y, knownDims.Size.Y)
	}

	fake.Set(osappearance.Dark)
	sc.applyLivePreferences(env, light, dark)
	if env.Th != dark {
		t.Fatalf("System + Dark resolved to %p, want dark %p", env.Th, dark)
	}
	darkDims, darkRuns := measureThemeModeControlRuns(env, sc)
	if darkRuns != knownRuns || darkDims.Size.Y != knownDims.Size.Y {
		t.Fatalf("Dark state showed unexpected helper text: runs=%d height=%d, want (%d, %d)", darkRuns, darkDims.Size.Y, knownRuns, knownDims.Size.Y)
	}
}

// TestExplicitThemeIgnoresOS verifies that explicit Light and Dark preferences
// do not change when the OS appearance changes.
func TestExplicitThemeIgnoresOS(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		mode     settings.ThemeMode
		wantDark bool
	}{
		{"explicit light ignores OS", settings.ThemeLight, false},
		{"explicit dark ignores OS", settings.ThemeDark, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeAppearanceSource(osappearance.Light)
			sc, env, light, dark := newAppearanceTestScreens(t, fake, tc.mode)
			want := light
			if tc.wantDark {
				want = dark
			}

			for _, next := range []osappearance.Appearance{osappearance.Dark, osappearance.Unknown, osappearance.Light} {
				fake.Set(next)
				sc.applyLivePreferences(env, light, dark)
				if env.Th != want {
					t.Fatalf("mode %q with OS %v got %p, want %p", tc.mode, next, env.Th, want)
				}
			}
		})
	}
}
