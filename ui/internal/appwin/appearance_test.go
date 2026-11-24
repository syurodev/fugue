package appwin

import (
	"image"
	"sync"
	"sync/atomic"
	"testing"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

type fakeAppearanceSource struct {
	mu   sync.Mutex
	cur  osAppearance
	next int
	subs map[int]func()
}

func newFakeAppearanceSource(initial osAppearance) *fakeAppearanceSource {
	return &fakeAppearanceSource{
		cur:  initial,
		subs: map[int]func(){},
	}
}

func (f *fakeAppearanceSource) Current() osAppearance {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cur
}

func (f *fakeAppearanceSource) Subscribe(onChange func()) func() {
	f.mu.Lock()
	id := f.next
	f.next++
	f.subs[id] = onChange
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		delete(f.subs, id)
		f.mu.Unlock()
	}
}

func (f *fakeAppearanceSource) set(next osAppearance) {
	f.mu.Lock()
	f.cur = next
	cbs := make([]func(), 0, len(f.subs))
	for _, cb := range f.subs {
		if cb != nil {
			cbs = append(cbs, cb)
		}
	}
	f.mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

func newAppearanceTestScreens(t *testing.T, src appearanceSource) (*screens, *kit.Env, *theme.Theme, *theme.Theme) {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	light, dark := theme.Light(sh), theme.Dark(sh)
	env := newEnv(sh, &light)
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.appearance = src
	sc.settings.Prefs.Current = settings.DefaultPreferences()
	return sc, env, &light, &dark
}

func TestSystemThemeFollowsOSAppearance(t *testing.T) {
	t.Parallel()
	src := newFakeAppearanceSource(appearanceLight)
	coord := newMultiWindow(nil, nil)
	var inv1, inv2 atomic.Int32
	w1 := &windowUpdates{invalidate: func() { inv1.Add(1) }}
	w2 := &windowUpdates{invalidate: func() { inv2.Add(1) }}
	coord.register(w1)
	coord.register(w2)
	stop := src.Subscribe(coord.invalidateAll)
	t.Cleanup(stop)

	sc1, env1, light, dark := newAppearanceTestScreens(t, src)
	sc2, env2, _, _ := newAppearanceTestScreens(t, src)

	sc1.applyLivePreferences(env1, light, dark)
	sc2.applyLivePreferences(env2, light, dark)
	if env1.Th != light || env2.Th != light {
		t.Fatalf("initial System theme with light OS: got (%p, %p), want light (%p)", env1.Th, env2.Th, light)
	}

	src.set(appearanceDark)
	if inv1.Load() != 1 || inv2.Load() != 1 {
		t.Fatalf("expected both windows invalidated on dark switch, got (%d, %d)", inv1.Load(), inv2.Load())
	}
	sc1.applyLivePreferences(env1, light, dark)
	sc2.applyLivePreferences(env2, light, dark)
	if env1.Th != dark || env2.Th != dark {
		t.Fatalf("System theme after dark switch: got (%p, %p), want dark (%p)", env1.Th, env2.Th, dark)
	}

	// Verify Linux custom title bar follows the resolved theme.
	bar := newTitleBar("linux")
	bar.applyConfig(app.Config{Decorated: false, Title: "System title"})
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(400, 300)),
	}
	if h := bar.layout(env1, &gtx); h == 0 {
		t.Fatal("expected Linux title bar to draw when undecorated")
	}

	src.set(appearanceLight)
	if inv1.Load() != 2 || inv2.Load() != 2 {
		t.Fatalf("expected both windows invalidated on light switch, got (%d, %d)", inv1.Load(), inv2.Load())
	}
	sc1.applyLivePreferences(env1, light, dark)
	sc2.applyLivePreferences(env2, light, dark)
	if env1.Th != light || env2.Th != light {
		t.Fatalf("System theme after light switch: got (%p, %p), want light (%p)", env1.Th, env2.Th, light)
	}
}

func TestSystemThemeUnknownFallsBackToLight(t *testing.T) {
	t.Parallel()
	src := newFakeAppearanceSource(appearanceUnknown)
	sc, env, light, dark := newAppearanceTestScreens(t, src)
	sc.settings.Prefs.Current.ThemeMode = settings.ThemeSystem
	sc.applyLivePreferences(env, light, dark)
	if env.Th != light {
		t.Fatalf("System theme with unknown OS: got %p, want light %p", env.Th, light)
	}

	// Verify the Settings preferences card grows when OS appearance is unknown
	// because it renders the helper text.
	measureCard := func(s *screens) int {
		var ops op.Ops
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Constraints{Max: image.Pt(600, 800)},
		}
		dims := s.settingsView.layoutPreferencesCard(&gtx, env, s)
		return dims.Size.Y
	}
	unknownH := measureCard(sc)
	src.set(appearanceLight)
	knownH := measureCard(sc)
	if unknownH <= knownH {
		t.Fatalf("expected helper text to increase preferences card height when OS appearance is unknown (unknown=%d, known=%d)", unknownH, knownH)
	}
}

func TestExplicitThemeIgnoresOS(t *testing.T) {
	t.Parallel()
	src := newFakeAppearanceSource(appearanceDark)
	sc, env, light, dark := newAppearanceTestScreens(t, src)

	sc.settings.Prefs.Current.ThemeMode = settings.ThemeLight
	sc.applyLivePreferences(env, light, dark)
	if env.Th != light {
		t.Fatalf("ThemeLight with dark OS: got %p, want light %p", env.Th, light)
	}

	src.set(appearanceLight)
	sc.settings.Prefs.Current.ThemeMode = settings.ThemeDark
	sc.applyLivePreferences(env, light, dark)
	if env.Th != dark {
		t.Fatalf("ThemeDark with light OS: got %p, want dark %p", env.Th, dark)
	}

	src.set(appearanceUnknown)
	sc.applyLivePreferences(env, light, dark)
	if env.Th != dark {
		t.Fatalf("ThemeDark with unknown OS: got %p, want dark %p", env.Th, dark)
	}
}
