package folderpicker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// fakeRunner records the helper invocations the pick reached and answers
// with a scripted stdout and error, so the tests never run a real process.
type fakeRunner struct {
	calls []string
	out   string
	err   error
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
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
		{"linux-portal-first", "linux", Availability{Portal: true, Zenity: true}, HelperPortal},
		{"linux-zenity-second", "linux", Availability{Zenity: true, KDialog: true}, HelperZenity},
		{"linux-kdialog-third", "linux", Availability{KDialog: true}, HelperKDialog},
		{"linux-none", "linux", Availability{}, HelperNone},
		{"other-none", "windows", Availability{OSAScript: true, Portal: true}, HelperNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
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

// TestCommandOfBuildsArgvOnly pins that the CLI helpers run as fixed argv:
// the name and arguments are exact, and a path never rides a shell string.
func TestCommandOfBuildsArgvOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		h      Helper
		start  string
		name   string
		args   []string
		wantOK bool
	}{
		{HelperOSAScript, "", "osascript", []string{"-e", `POSIX path of (choose folder with prompt "Choose a folder")`}, true},
		{HelperZenity, "", "zenity", []string{"--file-selection", "--directory"}, true},
		{HelperKDialog, "/home/u", "kdialog", []string{"--getexistingdirectory", "/home/u"}, true},
		{HelperPortal, "", "", nil, false},
		{HelperNone, "", "", nil, false},
	} {
		name, args, ok := commandOf(tc.h, tc.start)
		if ok != tc.wantOK || name != tc.name || !slices.Equal(args, tc.args) {
			t.Fatalf("commandOf(%v, %q) = (%q, %v, %v), want (%q, %v, %v)", tc.h, tc.start, name, args, ok, tc.name, tc.args, tc.wantOK)
		}
	}
}

// TestPickRunsThePlatformOrderWithFakedProcesses pins a pick end to end
// against faked helpers: the first available helper runs; its failure falls
// through to the next; its exit 1 is a cancel the caller reads as a no-op;
// any other failure is an error.
func TestPickRunsThePlatformOrderWithFakedProcesses(t *testing.T) {
	t.Parallel()
	t.Run("portal-answers", func(t *testing.T) {
		p := testPicker(t, "linux", Availability{Portal: true, Zenity: true}, fakePortal("/home/u/pick", false, nil), nil)
		p.Pick()
		res := <-p.Results()
		if res.Err != nil || res.Cancelled || res.Path != "/home/u/pick" {
			t.Fatalf("portal pick = %+v, want /home/u/pick", res)
		}
	})
	t.Run("portal-fails-then-zenity", func(t *testing.T) {
		run := &fakeRunner{out: "/home/u/zenity\n"}
		p := testPicker(t, "linux", Availability{Portal: true, Zenity: true}, fakePortal("", false, errors.New("no portal")), run)
		p.Pick()
		res := <-p.Results()
		if res.Err != nil || res.Path != "/home/u/zenity" {
			t.Fatalf("zenity fallback = %+v, want the zenity path", res)
		}
		if len(run.calls) != 1 || !strings.HasPrefix(run.calls[0], "zenity --file-selection --directory") {
			t.Fatalf("helper calls = %v, want one zenity run", run.calls)
		}
	})
	t.Run("exit-one-is-cancelled", func(t *testing.T) {
		run := &fakeRunner{err: &fakeExit{code: 1}}
		p := testPicker(t, "darwin", Availability{OSAScript: true}, nil, run)
		p.Pick()
		res := <-p.Results()
		if !res.Cancelled || res.Err != nil || res.Path != "" {
			t.Fatalf("cancelled pick = %+v, want a bare cancel", res)
		}
	})
	t.Run("other-exit-is-an-error", func(t *testing.T) {
		run := &fakeRunner{err: &fakeExit{code: 126}}
		p := testPicker(t, "darwin", Availability{OSAScript: true}, nil, run)
		p.Pick()
		res := <-p.Results()
		if res.Err == nil || res.Cancelled {
			t.Fatalf("failed pick = %+v, want an error", res)
		}
	})
	t.Run("no-helper-is-an-error", func(t *testing.T) {
		p := testPicker(t, "linux", Availability{}, nil, nil)
		p.Pick()
		res := <-p.Results()
		if res.Err == nil {
			t.Fatal("a pick with no helper reported success")
		}
	})
}

// TestPickerBusyRefusesASecondPick pins that a pick while one runs is
// refused at once and never queues a second chooser.
func TestPickerBusyRefusesASecondPick(t *testing.T) {
	t.Parallel()
	run := &fakeRunner{out: "/first"}
	p := testPicker(t, "darwin", Availability{OSAScript: true}, nil, run)
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
	p := testPicker(t, "linux", Availability{KDialog: true}, nil, nil)
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
	p := testPicker(t, "darwin", Availability{OSAScript: true}, nil, &fakeRunner{})
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
			return "", false, errors.New("portal not under test")
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
