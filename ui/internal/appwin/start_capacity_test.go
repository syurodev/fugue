package appwin

import (
	"errors"
	"fmt"
	"image"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/shottest"
)

const (
	shotStartRefused = "start-capacity-refused"
	testStartModel   = "opus"
	testStartEffort  = "high"
)

var errCapacityText = errors.New("the host already runs the maximum number of vendor sessions")

// capacityRefusal is the error the poster hands back for a start the host
// refused with vendor.capacity: a connect error whose typed detail carries the
// counts, wrapped the way the client wraps a failed call.
func capacityRefusal(t *testing.T, detail *hostv1.ErrorDetail) error {
	t.Helper()
	d, err := connect.NewErrorDetail(detail)
	if err != nil {
		t.Fatal(err)
	}
	cerr := connect.NewError(connect.CodeResourceExhausted, errCapacityText)
	cerr.AddDetail(d)
	return fmt.Errorf("start the session: %w", cerr)
}

// TestCapacityMessageNamesWhatIsFullAndWhatHelps pins the wording for each
// scope, and the fallback for a host that sent no counts.
func TestCapacityMessageNamesWhatIsFullAndWhatHelps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   client.Capacity
		want string
	}{
		{
			"machine",
			client.Capacity{Running: 8, Limit: 8, Scope: client.CapacityScopeMachine},
			"8 of 8 vendor sessions are running. Close a session, or raise max_sessions in the host config.",
		},
		{
			"account",
			client.Capacity{Running: 4, Limit: 4, Scope: client.CapacityScopeAccount},
			"4 of 4 sessions are running on this vendor account. Close a session, or raise per_account_sessions in the host config.",
		},
		{
			"no counts",
			client.Capacity{},
			"The host is at its vendor session limit. Close a session, or raise max_sessions in the host config.",
		},
	}
	for _, tc := range tests {
		if got := capacityMessage(tc.in); got != tc.want {
			t.Errorf("%s: message = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// startRefusedScreens opens the start dialog on a chosen workspace with the
// given model typed and an effort picked, and folds a capacity refusal back
// in. The shots pass no model: typing one opens the combobox's suggestions,
// which would cover the refusal line.
func startRefusedScreens(t *testing.T, model string) *screens {
	t.Helper()
	sc := buildShotScreens(t, "")
	f := sc.composer.start
	f.open(sc)
	f.chooseWorkspace("w1", sc)
	f.model.SetText(model)
	f.effortSel = testStartEffort
	f.busy = true
	sc.applyPostResult(session.PostResult{
		Kind: session.PostStart,
		Err: capacityRefusal(t, &hostv1.ErrorDetail{
			Code: "vendor.capacity", RunningSessions: 8, SessionLimit: 8, SessionLimitScope: "machine",
		}),
	})
	return sc
}

// TestStartRefusedOverCapacityShowsTheMessageAndKeepsTheForm pins the FUG-55
// refusal: the dialog stays open on the same workspace and model, the busy
// mark clears so the user can retry after closing a session, and the line
// carries the host's counts. A refusal never opens a session.
func TestStartRefusedOverCapacityShowsTheMessageAndKeepsTheForm(t *testing.T) {
	t.Parallel()
	sc := startRefusedScreens(t, testStartModel)
	f := sc.composer.start
	want := "8 of 8 vendor sessions are running. Close a session, or raise max_sessions in the host config."
	if f.lastErr != want || !f.refused {
		t.Fatalf("refusal line = %q (refused %v), want %q", f.lastErr, f.refused, want)
	}
	if f.busy {
		t.Fatal("the busy mark survived the refusal")
	}
	if !f.dialog.IsOpen() || f.wsID != "w1" || f.model.Text() != testStartModel || f.effortSel != testStartEffort {
		t.Fatalf("the form was lost: open %v, workspace %q, model %q, effort %q", f.dialog.IsOpen(), f.wsID, f.model.Text(), f.effortSel)
	}
	if sc.lastStarted != "" {
		t.Fatalf("a refused start marked session %q to open", sc.lastStarted)
	}
	// Another failure replaces the line and drops the warning tone.
	sc.applyPostResult(session.PostResult{Kind: session.PostStart, Err: errCapacityText})
	if f.refused || strings.Contains(f.lastErr, "Close a session") {
		t.Fatalf("a plain failure kept the refusal: %q (refused %v)", f.lastErr, f.refused)
	}
}

// TestStartRefusedOverCapacityShots renders the refused dialog, light and
// dark, against the goldens.
func TestStartRefusedOverCapacityShots(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range startRefusedShotSpecs() {
		img := renderAppShot(t, win, spec, startRefusedScreens(t, ""))
		shottest.Compare(t, spec.name, img)
		if isUniformRGBA(img) {
			t.Fatalf("shot %s is blank/uniform", spec.name)
		}
		rendered[spec.name] = img
	}
	if !imagesDifferRGBA(rendered[shotStartRefused+"-light-ui100"], rendered[shotStartRefused+"-dark-ui100"]) {
		t.Fatal("light and dark refusal shots are identical")
	}
}

func startRefusedShotSpecs() []shotSpec {
	return []shotSpec{
		{name: shotStartRefused + "-light-ui100", scene: shotStartRefused, light: true, uiScale: 1.0},
		{name: shotStartRefused + "-dark-ui100", scene: shotStartRefused, light: false, uiScale: 1.0},
	}
}
