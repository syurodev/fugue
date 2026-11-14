//go:build unix

package process

// The systemd containment wiring (F9.9e evidence), exercised through the fake
// vendor and its fake systemd-run launcher: the launcher's probe answer is
// cached per owner, so a second session of the same owner never probes again,
// and a respawn that targets a scope unit which still has processes fails
// closed. The fake clock drives every containment wait, so the suite is
// deterministic on macOS and Linux CI.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func containedSessionOpts(exe string, env []string, cgRoot, procRoot, sessionID string, probe *SystemdProbe, clk *clock.Fake) *Options {
	return &Options{
		Argv:           []string{exe, fakeVendorFlag, fakeVendorModeNormal},
		Clock:          clk,
		Env:            env,
		SessionID:      sessionID,
		XDGRuntimeDir:  fakeXDGRuntimeDir,
		CgroupRoot:     cgRoot,
		ProcRoot:       procRoot,
		SystemdRunPath: exe,
		Probe:          probe,
	}
}

// resetFakeStage removes the coordination files the previous spawn left in
// the fake proc root, so the next spawn's real-world waits (the child booting
// and staging its own stat) are fresh instead of satisfied by stale files.
func resetFakeStage(t *testing.T, procRoot string) {
	t.Helper()
	for _, rel := range []string{"stage-parent-ready", "stage-scope-moved", "allow-scope-move", "stat-ready"} {
		if err := os.Remove(filepath.Join(procRoot, rel)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
}

func startContainedSession(ctx context.Context, t *testing.T, clk *clock.Fake, procRoot string, opts *Options) *Process {
	t.Helper()
	resetFakeStage(t, procRoot)
	proc, err := spawnFakeContained(ctx, t, clk, procRoot, opts)
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, proc.Stdout())
	drainStdout(ctx, proc.Stdout())
	return proc
}

func stopContainedSession(ctx context.Context, t *testing.T, clk *clock.Fake, cgRoot string, proc *Process) {
	t.Helper()
	unitDir := filepath.Join(cgRoot, filepath.FromSlash(fakeUserManagerRel()), "app.slice", proc.Identity().ScopeUnit)
	markFakeScopeEmpty(t, unitDir)
	proof := stopAndDrive(ctx, t, clk, proc, nil)
	if proof.Status != StopProofProved {
		t.Fatalf("stop proof = %+v, want proved", proof)
	}
}

// The vendor runtime owns one SystemdProbe: the answer the launcher's probe
// produced for the first spawn is still in force for the second, even though
// the probed capability has disappeared in the meantime.
func TestOneSystemdProbePerOwner(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	cgRoot, procRoot := setupTempCgroupAndProc(t)
	probe := NewSystemdProbe()
	env := []string{
		"FUGUE_TEST_CGROUP_ROOT=" + cgRoot,
		"FUGUE_TEST_PROC_ROOT=" + procRoot,
	}

	first := startContainedSession(ctx, t, clk, procRoot, containedSessionOpts(exe, env, cgRoot, procRoot, "owner-a", probe, clk))
	// The probe's answer now comes from the cache: remove the capability the
	// probe checks, so a re-probe would fall back to the uncontained path and
	// leave the second session without a scope unit.
	if err := os.Remove(filepath.Join(cgRoot, "cgroup.controllers")); err != nil {
		t.Fatal(err)
	}
	second := startContainedSession(ctx, t, clk, procRoot, containedSessionOpts(exe, env, cgRoot, procRoot, "owner-b", probe, clk))

	for _, tc := range []struct {
		proc      *Process
		sessionID string
	}{
		{first, "owner-a"},
		{second, "owner-b"},
	} {
		want := Identity{
			PID:        tc.proc.PID(),
			StartToken: formatLinuxStartToken("boot-uuid-1234", 98765),
			ScopeUnit:  scopeUnitPrefix + tc.sessionID + scopeUnitSuffix,
		}
		if diff := cmp.Diff(want, tc.proc.Identity()); diff != "" {
			t.Fatalf("identity of %s mismatch (-want +got):\n%s", tc.sessionID, diff)
		}
	}
	stopContainedSession(ctx, t, clk, cgRoot, first)
	stopContainedSession(ctx, t, clk, cgRoot, second)
}

// A respawn of the same session targets the same scope unit. While the old
// scope still has processes the launcher refuses the unit, so the respawn
// fails typed and the live process is untouched: the unit still holds exactly
// the first process, and the refused spawn never probed or rewrote it.
func TestRespawnFailsClosedWhileOldScopeHasProcesses(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	cgRoot, procRoot := setupTempCgroupAndProc(t)
	probe := NewSystemdProbe()
	env := []string{
		"FUGUE_TEST_CGROUP_ROOT=" + cgRoot,
		"FUGUE_TEST_PROC_ROOT=" + procRoot,
		"FUGUE_TEST_REFUSE_EXISTING_UNIT=1",
	}
	opts := containedSessionOpts(exe, env, cgRoot, procRoot, "respawn-same", probe, clk)

	first := startContainedSession(ctx, t, clk, procRoot, opts)

	done := make(chan struct{})
	var secondErr error
	_ = supervise.Go(ctx, "test.spawn_refused", func(ctx context.Context) error {
		_, secondErr = Spawn(ctx, opts)
		close(done)
		return nil
	})
	driveClockUntil(t, clk, done)
	var coded *errcode.Error
	if !errors.As(secondErr, &coded) {
		t.Fatalf("respawn refusal is not typed: %v", secondErr)
	}

	// The old scope still holds exactly the first process.
	unitDir := filepath.Join(cgRoot, filepath.FromSlash(fakeUserManagerRel()), "app.slice",
		scopeUnitPrefix+"respawn-same"+scopeUnitSuffix)
	procs, err := readRootFile(unitDir, "cgroup.procs")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(procs), strconv.Itoa(first.PID())+"\n"; got != want {
		t.Fatalf("unit cgroup.procs = %q, want only the first process %q", got, want)
	}
	select {
	case <-first.exitCh:
		t.Fatal("the refused respawn disturbed the live process")
	default:
	}
	stopContainedSession(ctx, t, clk, cgRoot, first)
}
