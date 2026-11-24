package appwin

import (
	"image"
	"testing"

	"gioui.org/gpu/headless"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

const (
	shotFUG65Shell3Slot       = "shell-3slot"
	shotFUG65ArrangeMenuEdge  = "arrange-menu-right-edge"
	shotFUG65Accounts300      = "accounts-slot-300dp"
	shotFUG65SettingsSlot     = "settings-slot"
	shotFUG65MainEmpty        = "main-empty"
	shotFUG65FooterUnlocked   = "shell-footer-unlocked"
	shotFUG65SidebarCollapsed = "shell-sidebar-collapsed"
)

func fug65ShotSpecs() []shotSpec {
	themes := []struct {
		name  string
		light bool
	}{{testThemeLight, true}, {testThemeDark, false}}
	scales := []struct {
		suffix string
		scale  float32
	}{{"ui100", 1.0}, {"ui125", 1.25}}
	scenes := []string{
		shotFUG65Shell3Slot,
		shotFUG65ArrangeMenuEdge,
		shotFUG65Accounts300,
		shotFUG65SettingsSlot,
		shotFUG65MainEmpty,
		shotFUG65FooterUnlocked,
		shotFUG65SidebarCollapsed,
	}
	specs := make([]shotSpec, 0, len(scenes)*len(themes)*len(scales))
	for _, s := range scenes {
		for _, sc := range scales {
			for _, th := range themes {
				specs = append(specs, shotSpec{
					name: s + "-" + th.name + "-" + sc.suffix, scene: s, light: th.light, uiScale: sc.scale,
				})
			}
		}
	}
	return specs
}

func buildFUG65ShotScreens(t *testing.T, scene string) (*screens, image.Point) {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-fug65-shot"})
	sc.accounts.ApplyListAccounts("inc-fug65-shot", []client.Account{
		{
			Vendor: client.VendorClaude, State: client.AccountStateNeedsLogin, QuotaKnown: true, QuotaGate: 0.85,
			Windows: []client.QuotaWindow{{Name: "five_hour", Utilization: 0.42, Status: client.QuotaStatusAllowed}},
		},
		{Vendor: client.VendorCodex, State: client.AccountStateOK, QuotaKnown: false},
	})
	switch scene {
	case shotFUG65Shell3Slot:
		sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
		sc.grid.openCompanion(SlotRef{Kind: KindSettings})
		return sc, image.Pt(1500, 900)
	case shotFUG65ArrangeMenuEdge:
		sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
		sc.grid.openCompanion(SlotRef{Kind: KindSettings})
		sc.grid.openArrange(Pos{Col: 1, Row: 0})
		return sc, image.Pt(1500, 900)
	case shotFUG65Accounts300:
		sc.grid.openCompanion(SlotRef{Kind: KindAccounts})
		return sc, image.Pt(1100, 720)
	case shotFUG65SettingsSlot:
		sc.grid.openCompanion(SlotRef{Kind: KindSettings})
		return sc, image.Pt(1200, 800)
	case shotFUG65MainEmpty:
		return sc, image.Pt(1200, 800)
	case shotFUG65FooterUnlocked:
		attachFUG65UnlockedVault(t, sc)
		return sc, image.Pt(1200, 800)
	case shotFUG65SidebarCollapsed:
		attachFUG65UnlockedVault(t, sc)
		doc := cloneLayout(sc.grid.doc)
		doc.SidebarCollapsed = true
		sc.grid.edited(doc)
		return sc, image.Pt(1200, 800)
	default:
		return sc, image.Pt(1200, 800)
	}
}

func attachFUG65UnlockedVault(t *testing.T, sc *screens) {
	t.Helper()
	vw := vault.NewWorker(func() vault.Client { return newFakeAppVaultClient() })
	vw.Start(t.Context())
	t.Cleanup(vw.Stop)
	sc.vaultWorker = vw
	sc.applyConnectionUpdate(hostconn.StateConnected, "")
}

func renderFUG65Shot(t *testing.T, spec shotSpec) *image.RGBA {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Dark(sh)
	if spec.light {
		th = theme.Light(sh)
	}
	env := &kit.Env{Th: &th, Sh: sh, UIScale: spec.uiScale, ReducedMotion: true}
	sc, size := buildFUG65ShotScreens(t, spec.scene)
	var win *headless.Window
	win, err = glwin.Open(size.X, size.Y)
	if err != nil {
		t.Fatalf("open headless window for %s: %v", spec.name, err)
	}
	return renderShotFrames(t, win, env, sc, size, spec.name)
}

// TestFUG65ShellVisualPassGoldens compares the FUG-65 scenes in light and dark
// at 100% and 125% against their checked-in goldens.
func TestFUG65ShellVisualPassGoldens(t *testing.T) {
	t.Parallel()
	for _, spec := range fug65ShotSpecs() {
		img := renderFUG65Shot(t, spec)
		shottest.Compare(t, spec.name, img)
		if isUniformRGBA(img) {
			t.Fatalf("shot %s is blank/uniform", spec.name)
		}
	}
}
