package appwin

import (
	"image"
	"testing"

	"google.golang.org/protobuf/proto"

	"gioui.org/gpu/headless"

	"github.com/syurodev/fugue/pkg/client"
	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"

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

// shotFailureUnprovedStop is the hung-worker row's banner (activation §3): a
// closed session whose stop the host could not prove.
const shotFailureUnprovedStop = "failure-unproved-stop"

func failureShotSpecs() []shotSpec {
	return []shotSpec{
		{name: shotFailureUnprovedStop + "-light-ui100", scene: shotFailureUnprovedStop, light: true, uiScale: 1.0},
		{name: shotFailureUnprovedStop + "-dark-ui100", scene: shotFailureUnprovedStop, light: false, uiScale: 1.0},
	}
}

// buildFailureShotScreens opens a closed session whose transcript reported an
// unproved stop, the state the guidance banner is built from.
func buildFailureShotScreens(t *testing.T) *screens {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "failure"})
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: "s1", Vendor: client.VendorClaude, State: stateClosed},
	}})
	sc.open = "s1"
	sc.transcripts["s1"] = session.NewTranscript("s1")
	payload, err := proto.Marshal(&transcriptv1.TranscriptStateEvent{State: stateClosed, StopProof: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.transcripts["s1"].ApplyEvent(session.EventTranscriptState, 1, payload); err != nil {
		t.Fatal(err)
	}
	return sc
}

func renderFailureShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
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
	sc := buildFailureShotScreens(t)
	img := renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), spec.name)
	if sc.failure.unproved.Presence() <= 0 {
		t.Fatalf("failure shot %s did not show the unproved-stop banner", spec.name)
	}
	return img
}

// TestFailureUXUnprovedStopBannerRendersShots renders the restart-the-host
// guidance for a session closed without a stop proof, in light and dark.
func TestFailureUXUnprovedStopBannerRendersShots(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range failureShotSpecs() {
		img := renderFailureShot(t, win, spec)
		shottest.Compare(t, spec.name, img)
		if isUniformRGBA(img) {
			t.Fatalf("failure shot %s is blank/uniform", spec.name)
		}
		rendered[spec.name] = img
	}
	if !imagesDifferRGBA(rendered[shotFailureUnprovedStop+"-light-ui100"], rendered[shotFailureUnprovedStop+"-dark-ui100"]) {
		t.Fatal("unproved-stop light and dark shots are identical")
	}
}
