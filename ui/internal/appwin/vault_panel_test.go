package appwin

import (
	"image"
	"image/color"
	"math"
	"regexp"
	"strings"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
)

// TestVaultPanelPerHostAndVaultState pins the gate's one panel for every pair
// of host state and vault state (FUG-57): a form shows only for a connected
// host whose vault state is known, and the unknown states fail closed.
func TestVaultPanelPerHostAndVaultState(t *testing.T) {
	t.Parallel()
	known := func(state string) *vault.Model {
		m := vault.NewModel()
		m.ApplyStatus(vault.Status{State: state, Revision: 1, Incarnation: "panel"})
		return m
	}
	hosts := []struct {
		name  string
		state hostconn.State
	}{
		{"unset", ""},
		{"connecting", hostconn.StateConnecting},
		{"connected", hostconn.StateConnected},
		{"disconnected", hostconn.StateDisconnected},
		{"incompatible", hostconn.StateIncompatible},
	}
	vaults := []struct {
		name string
		m    *vault.Model
	}{
		{"unknown", vault.NewModel()},
		{"not_configured", known(client.VaultStateNotConfigured)},
		{vaultLockedLabel, known(client.VaultStateLocked)},
	}
	want := map[string]vaultPanel{
		"unset/unknown": panelConnecting, "unset/not_configured": panelConnecting, "unset/locked": panelConnecting,
		"connecting/unknown": panelConnecting, "connecting/not_configured": panelConnecting, "connecting/locked": panelConnecting,
		"connected/unknown": panelChecking, "connected/not_configured": panelSetup, "connected/locked": panelUnlock,
		"disconnected/unknown": panelUnavailable, "disconnected/not_configured": panelUnavailable, "disconnected/locked": panelUnavailable,
		"incompatible/unknown": panelUnavailable, "incompatible/not_configured": panelUnavailable, "incompatible/locked": panelUnavailable,
	}
	for _, h := range hosts {
		for _, v := range vaults {
			g := newVaultGate()
			if got := g.panel(h.state, v.m); got != want[h.name+"/"+v.name] {
				t.Errorf("host %s, vault %s: panel %d, want %d", h.name, v.name, got, want[h.name+"/"+v.name])
			}
		}
	}
}

// TestVaultPanelRecoveryModeAndKey pins the two overrides: the recovery
// entry replaces the unlock panel of a connected locked vault, and a key
// still to be saved outranks every host and vault state.
func TestVaultPanelRecoveryModeAndKey(t *testing.T) {
	t.Parallel()
	m := vault.NewModel()
	m.ApplyStatus(vault.Status{State: client.VaultStateLocked, Revision: 1, Incarnation: "panel"})
	g := newVaultGate()
	g.recoveryMode = true
	if got := g.panel(hostconn.StateConnected, m); got != panelRecovery {
		t.Fatalf("recovery mode panel = %d, want recovery entry", got)
	}
	if got := g.panel(hostconn.StateDisconnected, m); got != panelUnavailable {
		t.Fatalf("recovery mode with a lost host = %d, want unavailable", got)
	}
	g.recovery = []byte("key")
	for _, h := range []hostconn.State{"", hostconn.StateConnecting, hostconn.StateConnected, hostconn.StateDisconnected} {
		if got := g.panel(h, vault.NewModel()); got != panelRecoveryKey {
			t.Fatalf("host %q with a key to save: panel %d, want the recovery key", h, got)
		}
	}
}

// TestVaultGateAppliesConnectionUpdates checks the window feeds the gate: a
// connection update moves the panel, and a topic delivery without a state
// leaves it.
func TestVaultGateAppliesConnectionUpdates(t *testing.T) {
	t.Parallel()
	sc := buildVaultShotScreens(shotVaultUnlock)
	if got := sc.vaultGate.panel(sc.hostState, sc.vault); got != panelUnlock {
		t.Fatalf("connected locked panel = %d", got)
	}
	sc.applyUpdate(hostconn.Update{State: hostconn.StateDisconnected, Reason: testHostNotRunning})
	if got := sc.vaultGate.panel(sc.hostState, sc.vault); got != panelUnavailable {
		t.Fatalf("after disconnect panel = %d, want unavailable", got)
	}
	sc.applyUpdate(hostconn.Update{Topic: "unknown-topic"})
	if sc.hostState != hostconn.StateDisconnected || sc.hostReason != testHostNotRunning {
		t.Fatalf("a topic update changed the host state to %q (%q)", sc.hostState, sc.hostReason)
	}
}

const testHostNotRunning = "host not running"

var internalIDPattern = regexp.MustCompile(`\b(?:[A-Z]{1,3}\d?-(?:E)?\d+|F\d+\.\d+[a-z]?|FUG-\d+|DA-\d+)\b`)

// TestHostProblemTextNamesTheFixWithoutInternalIDs pins the failure text of
// each reason the manager reports: a title, a detail and the action, none
// carrying a design or issue ID.
func TestHostProblemTextNamesTheFixWithoutInternalIDs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		state  hostconn.State
		reason string
		fix    string
	}{
		{"unsafe executable", hostconn.StateDisconnected, hostconn.ErrUnsafeExecutable.Error(), "only your account can write"},
		{"untrusted host", hostconn.StateDisconnected, client.ErrUntrustedHost.Error(), "discovery file"},
		{"startup timeout", hostconn.StateDisconnected, hostconn.ErrStartupTimeout.Error(), "restart the app"},
		{testHostNotRunning, hostconn.StateDisconnected, testHostNotRunning, "reconnects on its own"},
		{"version mismatch", hostconn.StateIncompatible, "Fugue host v9.0.0 is newer; update the app", "same version"},
	}
	for _, tc := range cases {
		p := describeHostProblem(tc.state, tc.reason)
		all := p.title + " " + p.detail + " " + p.action
		if p.title == "" || p.detail == "" || !strings.Contains(all, tc.fix) {
			t.Errorf("%s: problem %+v misses %q", tc.name, p, tc.fix)
		}
		if internalIDPattern.MatchString(all) {
			t.Errorf("%s: text %q carries an internal ID", tc.name, all)
		}
	}
}

// TestVaultPanelIsCentredInWindow verifies that every vault gate panel is
// centred within 1 dp of the window body's centre at both 100% and 125% UI
// scale (FUG-68).
func TestVaultPanelIsCentredInWindow(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Light(sh)
	scenes := []string{
		shotVaultConnecting,
		shotVaultUnavailable,
		shotVaultSetup,
		shotVaultUnlock,
		shotVaultRecovery,
		shotVaultRecoveryKey,
	}
	scales := []float32{1.0, 1.25}
	for _, scale := range scales {
		env := &kit.Env{Th: &th, Sh: sh, UIScale: scale, ReducedMotion: true}
		var ops op.Ops
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
			Constraints: layout.Exact(image.Pt(shotWindowW, shotWindowH)),
		}
		probe := buildVaultShotScreens(shotVaultUnlock)
		barH := probe.titleBar.height(&gtx)
		body := image.Rect(0, barH, shotWindowW, shotWindowH)
		bodyCenter := image.Pt(body.Min.X+body.Dx()/2, body.Min.Y+body.Dy()/2)
		maxOffsetPx := int(math.Ceil(float64(scale)))

		for _, scene := range scenes {
			spec := shotSpec{name: scene, scene: scene, light: true, uiScale: scale}
			img := renderVaultShot(t, win, spec)
			bg := img.RGBAAt(0, shotWindowH-1)
			panel := nonBackgroundBounds(img, barH, bg)
			if panel.Empty() {
				t.Fatalf("%s @ %.0f%%: vault panel not visible in rendered frame", scene, scale*100)
			}
			panelCenter := image.Pt(panel.Min.X+panel.Dx()/2, panel.Min.Y+panel.Dy()/2)
			dx := absInt(panelCenter.X - bodyCenter.X)
			dy := absInt(panelCenter.Y - bodyCenter.Y)
			if dx > maxOffsetPx || dy > maxOffsetPx {
				t.Errorf("%s @ %.0f%%: panel %v center %v not within 1 dp (%d px) of window body center %v (delta %d,%d)",
					scene, scale*100, panel, panelCenter, maxOffsetPx, bodyCenter, dx, dy)
			}
		}
	}
}

func nonBackgroundBounds(img *image.RGBA, minY int, bg color.RGBA) image.Rectangle {
	b := img.Bounds()
	out := image.Rectangle{Min: image.Pt(b.Max.X, b.Max.Y), Max: image.Pt(b.Min.X, b.Min.Y)}
	found := false
	for y := max(b.Min.Y, minY); y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if absInt(int(c.R)-int(bg.R)) > 2 || absInt(int(c.G)-int(bg.G)) > 2 || absInt(int(c.B)-int(bg.B)) > 2 {
				found = true
				out.Min.X = min(out.Min.X, x)
				out.Min.Y = min(out.Min.Y, y)
				out.Max.X = max(out.Max.X, x+1)
				out.Max.Y = max(out.Max.Y, y+1)
			}
		}
	}
	if !found {
		return image.Rectangle{}
	}
	return out
}
