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
	shotVaultUnlock = "vault-unlock"
	shotVaultSetup  = "vault-setup"
)

func vaultShotSpecs() []shotSpec {
	scenes := []string{shotVaultUnlock, shotVaultSetup}
	modes := []struct {
		label string
		light bool
	}{
		{"light-ui100", true},
		{"dark-ui100", false},
	}
	specs := make([]shotSpec, 0, len(scenes)*len(modes))
	for _, s := range scenes {
		for _, m := range modes {
			specs = append(specs, shotSpec{
				name:    s + "-" + m.label,
				scene:   s,
				light:   m.light,
				uiScale: 1.0,
			})
		}
	}
	return specs
}

func buildVaultShotScreens(scene string) *screens {
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	if scene == shotVaultSetup {
		sc.vault.ApplyStatus(vault.Status{State: client.VaultStateNotConfigured, Incarnation: "shot"})
		sc.vaultGate.pass.SetText("passphrase123")
		sc.vaultGate.confirm.SetText("passphrase123")
		return sc
	}
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateLocked, Revision: 1, Incarnation: "shot"})
	sc.vaultGate.pass.SetText("passphrase123")
	return sc
}

func renderVaultShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
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
	sc := buildVaultShotScreens(spec.scene)
	img := renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), spec.name)
	if !sc.vaultGate.active(sc.vault) {
		t.Fatalf("vault shot %s failed to render active vault gate", spec.name)
	}
	return img
}

// TestVaultScreenMasksInputAndRendersShots renders the vault unlock and setup
// gate screens in light and dark themes headlessly.
func TestVaultScreenMasksInputAndRendersShots(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range vaultShotSpecs() {
		img := renderVaultShot(t, win, spec)
		shottest.Compare(t, spec.name, img)
		if isUniformRGBA(img) {
			t.Fatalf("vault shot %s is blank/uniform", spec.name)
		}
		rendered[spec.name] = img
	}
	for _, scene := range []string{shotVaultUnlock, shotVaultSetup} {
		if !imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-dark-ui100"]) {
			t.Fatalf("%s light and dark shots are identical", scene)
		}
	}
}
