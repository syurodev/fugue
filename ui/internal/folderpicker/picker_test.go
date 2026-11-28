package folderpicker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// The platforms the picker tests stand in for.
const (
	testGoosLinux  = "linux"
	testGoosDarwin = "darwin"
)

// The static causes the tests script; wrapped, never constructed, in the
// test bodies.
var (
	errNoPortal     = errors.New("no portal")
	errNoPortalStub = errors.New("portal not under test")
)

// fakeRunner records the helper invocations the pick reached and answers
// with a scripted stdout and error, so the tests never run a real process.
type fakeRunner struct {
	calls []string
	out   string
	err   error
}

func (f *fakeRunner) Run(_ context.Context, h Helper) (string, error) {
	f.calls = append(f.calls, helperName(h))
	return strings.TrimSpace(f.out), f.err
}

// fakePortal is the stand-in for the portal FileChooser flow.
func fakePortal(out string, cancelled bool, err error) pickPortalFunc {
	return func(context.Context) (string, bool, error) { return out, cancelled, err }
}

// fakeExit is a helper's nonzero exit, the way the real runner wraps one.
type fakeExit struct{ code int }

func (f *fakeExit) Error() string { return "the helper failed" }
func (f *fakeExit) ExitCode() int { return f.code }

// TestSelectHelperFollowsThePlatformOrder pins the helper the pick uses per
// platform: macOS osascript; Linux portal, zenity, kdialog; nothing where no
// helper answered.
func TestSelectHelperFollowsThePlatformOrder(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		goos string
		av   Availability
		want Helper
	}{
		{"macos-has-osascript", "darwin", Availability{OSAScript: true}, HelperOSAScript},
		{"macos-none", "darwin", Availability{}, HelperNone},
		{"linux-portal-first", testGoosLinux, Availability{Portal: true, Zenity: true}, HelperPortal},
		{"linux-zenity-second", testGoosLinux, Availability{Zenity: true, KDialog: true}, HelperZenity},
		{"linux-kdialog-third", testGoosLinux, Availability{KDialog: true}, HelperKDialog},
		{"linux-none", testGoosLinux, Availability{}, HelperNone},
		{"other-none", "windows", Availability{OSAScript: true, Portal: true}, HelperNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := selectHelper(tc.av, tc.goos); got != tc.want {
				t.Fatalf("selectHelper(%+v, %q) = %v, want %v", tc.av, tc.goos, got, tc.want)
			}
		})
	}
}

// TestPickOrderRunsTheAvailableHelpersInOrder pins the fallback chain a pick
// walks: only the helpers the probe found, in the platform's order.
func TestPickOrderRunsTheAvailableHelpersInOrder(t *testing.T) {
	t.Parallel()
	linux := pickOrder(Availability{Portal: true, Zenity: true, KDialog: true}, "linux")
	if !slices.Equal(linux, []Helper{HelperPortal, HelperZenity, HelperKDialog}) {
		t.Fatalf("linux order = %v, want portal, zenity, kdialog", linux)
	}
	if got := pickOrder(Availability{OSAScript: true}, "darwin"); !slices.Equal(got, []Helper{HelperOSAScript}) {
		t.Fatalf("darwin order = %v, want osascript", got)
	}
	if got := pickOrder(Availability{}, "linux"); len(got) != 0 {
		t.Fatalf("empty order = %v, want none", got)
	}
}

// TestHelperCommandBuildsLiteralArgv pins that every CLI helper runs a
// literal program name and literal argv — no caller value names a program or
// rides a shell string — and that kdialog starts in the user's home through
// its working directory.
func TestHelperCommandBuildsLiteralArgv(t *testing.T) {
	t.Parallel()
	cmd, ok := helperCommand(t.Context(), HelperOSAScript)
	if !ok || !slices.Equal(cmd.Args[1:], []string{"-e", osascriptChooseFolder}) {
		t.Fatalf("osascript argv = %v, want the fixed script", cmd.Args)
	}
	cmd, ok = helperCommand(t.Context(), HelperZenity)
	if !ok || !slices.Equal(cmd.Args[1:], []string{"--file-selection", "--directory"}) {
		t.Fatalf("zenity argv = %v, want the fixed flags", cmd.Args)
	}
	cmd, ok = helperCommand(t.Context(), HelperKDialog)
	if !ok || !slices.Equal(cmd.Args[1:], []string{"--getexistingdirectory", "."}) {
		t.Fatalf("kdialog argv = %v, want the start directory as .", cmd.Args)
	}
	if cmd.Dir == "" {
		t.Fatal("kdialog has no working directory to start in")
	}
	for _, h := range []Helper{HelperPortal, HelperNone} {
		if _, ok := helperCommand(t.Context(), h); ok {
			t.Fatalf("helper %v built a process", h)
		}
	}
}

// TestPickRunsThePlatformOrderWithFakedProcesses pins a pick end to end
// against faked helpers: the first available helper runs; its failure falls
// through to the next; its exit 1 is a cancel the caller reads as a no-op;
// any other failure is an error.
func TestPickRunsThePlatformOrderWithFakedProcesses(t *testing.T) {
	t.Parallel()
	t.Run("portal-answers", func(t *testing.T) { t.Parallel(); pickPortalAnswers(t) })
	t.Run("portal-fails-then-zenity", func(t *testing.T) { t.Parallel(); pickPortalFallsToZenity(t) })
	t.Run("exit-one-is-cancelled", func(t *testing.T) { t.Parallel(); pickExitOneIsCancelled(t) })
	t.Run("other-exit-is-an-error", func(t *testing.T) { t.Parallel(); pickOtherExitIsAnError(t) })
	t.Run("no-helper-is-an-error", func(t *testing.T) { t.Parallel(); pickNoHelperIsAnError(t) })
}

// pickPortalAnswers pins the portal's converted answer.
func pickPortalAnswers(t *testing.T) {
	t.Helper()
	p := testPicker(t, testGoosLinux, Availability{Portal: true, Zenity: true}, fakePortal("/home/u/pick", false, nil), nil)
	p.Pick()
	res := <-p.Results()
	if res.Err != nil || res.Cancelled || res.Path != "/home/u/pick" {
		t.Fatalf("portal pick = %+v, want /home/u/pick", res)
	}
}

// pickPortalFallsToZenity pins the fall-through: a portal that cannot run
// hands the pick to the first CLI helper that can.
func pickPortalFallsToZenity(t *testing.T) {
	t.Helper()
	run := &fakeRunner{out: "/home/u/zenity\n"}
	p := testPicker(t, testGoosLinux, Availability{Portal: true, Zenity: true}, fakePortal("", false, errNoPortal), run)
	p.Pick()
	res := <-p.Results()
	if res.Err != nil || res.Path != "/home/u/zenity" {
		t.Fatalf("zenity fallback = %+v, want the zenity path", res)
	}
	if !slices.Equal(run.calls, []string{"zenity"}) {
		t.Fatalf("helper calls = %v, want one zenity run", run.calls)
	}
}

// pickExitOneIsCancelled pins that the CLI helpers' exit 1 — a closed
// chooser on every platform — is a cancel the caller reads as a no-op.
func pickExitOneIsCancelled(t *testing.T) {
	t.Helper()
	run := &fakeRunner{err: &fakeExit{code: 1}}
	p := testPicker(t, testGoosDarwin, Availability{OSAScript: true}, nil, run)
	p.Pick()
	res := <-p.Results()
	if !res.Cancelled || res.Err != nil || res.Path != "" {
		t.Fatalf("cancelled pick = %+v, want a bare cancel", res)
	}
}

// pickOtherExitIsAnError pins that any other helper exit is a failure.
func pickOtherExitIsAnError(t *testing.T) {
	t.Helper()
	run := &fakeRunner{err: &fakeExit{code: 126}}
	p := testPicker(t, testGoosDarwin, Availability{OSAScript: true}, nil, run)
	p.Pick()
	res := <-p.Results()
	if res.Err == nil || res.Cancelled {
		t.Fatalf("failed pick = %+v, want an error", res)
	}
}

// pickNoHelperIsAnError pins that a desktop the probe found nothing for
// refuses the pick.
func pickNoHelperIsAnError(t *testing.T) {
	t.Helper()
	p := testPicker(t, testGoosLinux, Availability{}, nil, nil)
	p.Pick()
	res := <-p.Results()
	if res.Err == nil {
		t.Fatal("a pick with no helper reported success")
	}
}

// TestPickerBusyRefusesASecondPick pins that a pick while one runs is
// refused at once and never queues a second chooser.
func TestPickerBusyRefusesASecondPick(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{out: "/first"}
	p := testPicker(t, testGoosDarwin, Availability{OSAScript: true}, nil, run)
	if !p.Pick() {
		t.Fatal("the first pick was refused")
	}
	if p.Pick() {
		t.Fatal("a second pick while one ran was accepted")
	}
	res := <-p.Results()
	if res.Err != nil || res.Path != "/first" {
		t.Fatalf("first pick = %+v, want /first", res)
	}
}

// TestPickerAvailableFollowsTheProbe pins that the availability the dialogs
// read comes from the probe the worker ran at start.
func TestPickerAvailableFollowsTheProbe(t *testing.T) {
	t.Parallel()
	p := testPicker(t, testGoosLinux, Availability{KDialog: true}, nil, nil)
	if av := p.Available(); !av.KDialog {
		t.Fatalf("available = %+v, want the probe's kdialog", av)
	}
	if p.First() != HelperKDialog {
		t.Fatalf("first helper = %v, want kdialog", p.First())
	}
}

// TestPickerStopEndsTheWorker pins the stop path: Stop ends the worker and
// is idempotent, like the workspace mutator's.
func TestPickerStopEndsTheWorker(t *testing.T) {
	t.Parallel()
	p := testPicker(t, testGoosDarwin, Availability{OSAScript: true}, nil, &fakeRunner{})
	p.Stop()
	p.Stop()
}

// testPicker builds a Picker whose probe, portal flow, runner and platform
// the test controls, started for the caller.
func testPicker(t *testing.T, goos string, av Availability, portal pickPortalFunc, run Runner) *Picker {
	t.Helper()
	p := NewPicker()
	p.probeOf = func(context.Context) Availability { return av }
	p.portal = portal
	if run != nil {
		p.runner = run
	}
	p.goos = goos
	if portal == nil {
		p.portal = func(context.Context) (string, bool, error) {
			return "", false, errNoPortalStub
		}
	}
	p.Start(context.Background())
	t.Cleanup(p.Stop)
	return p
}

// TestUriPathTurnsAPortalURIIntoAPath pins the portal answer's decode: a
// file URI becomes its absolute path, spaces and all, and anything else is
// refused.
func TestUriPathTurnsAPortalURIIntoAPath(t *testing.T) {
	t.Parallel()
	path, err := uriPath("file:///home/u/pick%20list")
	if err != nil || path != "/home/u/pick list" {
		t.Fatalf("uriPath = (%q, %v), want the decoded path", path, err)
	}
	if _, err := uriPath("https://example.invalid/folder"); err == nil {
		t.Fatal("a non-file URI decoded into a path")
	}
}
