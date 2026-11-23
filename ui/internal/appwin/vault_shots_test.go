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
	shotVaultUnlock      = "vault-unlock"
	shotVaultSetup       = "vault-setup"
	shotVaultUnavailable = "vault-unavailable"
	shotVaultConnecting  = "vault-connecting"
	shotVaultRecoveryKey = "vault-recovery-key"
	shotVaultRecovery    = "vault-recovery"
)

func vaultShotSpecs() []shotSpec {
	scenes := []string{shotVaultUnavailable, shotVaultConnecting, shotVaultSetup, shotVaultRecoveryKey, shotVaultUnlock, shotVaultRecovery}
	modes := []struct {
		label   string
		light   bool
		uiScale float32
	}{
		{"light-ui100", true, 1.0},
		{"dark-ui100", false, 1.0},
		{"light-ui125", true, 1.25},
		{"dark-ui125", false, 1.25},
	}
	specs := make([]shotSpec, 0, len(scenes)*len(modes))
	for _, s := range scenes {
		for _, m := range modes {
			specs = append(specs, shotSpec{name: s + "-" + m.label, scene: s, light: m.light, uiScale: m.uiScale})
		}
	}
	return specs
}

func buildVaultShotScreens(scene string) *screens {
	m := hostconn.NewManager(&hostconn.Config{DataDir: "/home/user/.local/share/fugue"})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.applyConnectionUpdate(hostconn.StateConnected, "")
	status := func(state string) { sc.vault.ApplyStatus(vault.Status{State: state, Revision: 1, Incarnation: "shot"}) }
	switch scene {
	case shotVaultUnavailable:
		sc.applyConnectionUpdate(hostconn.StateDisconnected, "unsafe host executable")
	case shotVaultConnecting:
		sc.applyConnectionUpdate(hostconn.StateConnecting, "")
	case shotVaultSetup:
		status(client.VaultStateNotConfigured)
		sc.vaultGate.pass.SetText("passphrase123")
		sc.vaultGate.confirm.SetText("passphrase123")
	case shotVaultRecoveryKey:
		status(client.VaultStateUnlocked)
		sc.vaultGate.recovery = []byte("FUG-RK-7QX2-M9PA-4TDV-KE8C-1JHW-ZB6N-U3YS-R5LG")
	case shotVaultRecovery:
		status(client.VaultStateLocked)
		sc.vaultGate.recoveryMode = true
		sc.vaultGate.pass.SetText("recovery-key-value")
		sc.vaultGate.lastErr = vaultErrorMessage("vault.invalid_passphrase", 0, nil)
	default:
		status(client.VaultStateLocked)
		sc.vaultGate.pass.SetText("passphrase123")
	}
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
	for _, scene := range []string{shotVaultUnavailable, shotVaultConnecting, shotVaultSetup, shotVaultRecoveryKey, shotVaultUnlock, shotVaultRecovery} {
		if !imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-dark-ui100"]) {
			t.Fatalf("%s light and dark shots are identical", scene)
		}
	}
}
