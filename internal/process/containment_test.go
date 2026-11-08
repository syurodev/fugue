//go:build unix

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func TestLinuxCgroupStopProofAndFallback(t *testing.T) {
	t.Parallel()

	t.Run("proved when cgroup becomes empty", func(t *testing.T) {
		t.Parallel()
		testCgroupProvedWhenEmpty(t)
	})

	t.Run("unknown plus scope_unverified on mismatched proc cgroup path", func(t *testing.T) {
		t.Parallel()
		testCgroupScopeUnverified(t)
	})

	t.Run("fallback to group path plus tracker when probe fails", func(t *testing.T) {
		t.Parallel()
		testCgroupFallbackWhenProbeFails(t)
	})

	t.Run("cgroup.kill preferred over per-pid SIGKILL", func(t *testing.T) {
		t.Parallel()
		testCgroupKillPreferredOverPerPidSigkill(t)
	})
}

func setupTempCgroupAndProc(t *testing.T) (string, string) {
	t.Helper()
	cgRoot := t.TempDir()
	procRoot := t.TempDir()
	if err := writeRootFile(cgRoot, "cgroup.controllers", []byte("cpu memory pids\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(procRoot, "sys/kernel/random"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeRootFile(procRoot, "sys/kernel/random/boot_id", []byte("boot-uuid-1234\n")); err != nil {
		t.Fatal(err)
	}
	return cgRoot, procRoot
}

func markFakeScopeEmpty(t *testing.T, unitDir string) {
	t.Helper()
	if err := writeRootFile(unitDir, "cgroup.events", []byte("populated 0\nfrozen 0\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeRootFile(unitDir, "cgroup.procs", nil); err != nil {
		t.Fatal(err)
	}
}

func spawnReadyHelper(ctx context.Context, t *testing.T, exe string) *Process {
	t.Helper()
	helper, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, fakeVendorModeIgnoreSigterm},
		Clock: clock.NewFake(testEpoch()),
	})
	if err != nil {
		t.Fatalf("Spawn helper: %v", err)
	}
	t.Cleanup(func() {
		if kerr := syscall.Kill(helper.PID(), syscall.SIGKILL); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
			t.Errorf("kill helper %d: %v", helper.PID(), kerr)
		}
		if werr := helper.Wait(); werr != nil {
			return
		}
	})
	waitReady(t, helper.Stdout())
	drainStdout(ctx, helper.Stdout())
	return helper
}

func testCgroupProvedWhenEmpty(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	cgRoot, procRoot := setupTempCgroupAndProc(t)

	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, fakeVendorModeNormal},
		Clock: clk,
		Env: []string{
			"FUGUE_TEST_CGROUP_ROOT=" + cgRoot,
			"FUGUE_TEST_PROC_ROOT=" + procRoot,
		},
		SessionID:      "sess-proved",
		MemoryMax:      "2G",
		CPUWeight:      100,
		CgroupRoot:     cgRoot,
		ProcRoot:       procRoot,
		SystemdRunPath: exe,
		Probe:          NewSystemdProbe(),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitReady(t, proc.Stdout())
	drainStdout(ctx, proc.Stdout())

	wantID := Identity{
		PID:        proc.PID(),
		StartToken: formatLinuxStartToken("boot-uuid-1234", 98765),
		ScopeUnit:  "fugue-vendor-sess-proved.scope",
	}
	if diff := cmp.Diff(wantID, proc.Identity()); diff != "" {
		t.Fatalf("Identity mismatch (-want +got):\n%s", diff)
	}

	unitDir := filepath.Join(cgRoot, "user.slice", wantID.ScopeUnit)
	stopDone := make(chan struct{})
	var proof StopProof
	var stopErr error
	_ = supervise.Go(ctx, "test.cgroup_proved", func(ctx context.Context) error {
		proof, stopErr = proc.Stop(ctx)
		close(stopDone)
		return nil
	})

	<-proc.exitCh
	markFakeScopeEmpty(t, unitDir)
	driveClockUntil(t, clk, stopDone)

	if stopErr != nil {
		t.Fatalf("Stop: %v", stopErr)
	}
	wantProof := StopProof{Status: StopProofProved, Reason: ReasonCgroupEmpty}
	if diff := cmp.Diff(wantProof, proof); diff != "" {
		t.Fatalf("StopProof mismatch (-want +got):\n%s", diff)
	}
}

func testCgroupScopeUnverified(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	cgRoot, procRoot := setupTempCgroupAndProc(t)

	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, fakeVendorModeNormal},
		Clock: clk,
		Env: []string{
			"FUGUE_TEST_CGROUP_ROOT=" + cgRoot,
			"FUGUE_TEST_PROC_ROOT=" + procRoot,
			"FUGUE_TEST_PROC_CGROUP_OVERRIDE=0::/user.slice/wrong-unit.scope",
		},
		SessionID:      "sess-unverified",
		CgroupRoot:     cgRoot,
		ProcRoot:       procRoot,
		SystemdRunPath: exe,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitReady(t, proc.Stdout())
	drainStdout(ctx, proc.Stdout())

	if proc.Identity().ScopeUnit != "" {
		t.Fatalf("expected empty ScopeUnit on unverified cgroup, got %q", proc.Identity().ScopeUnit)
	}

	stopDone := make(chan struct{})
	var proof StopProof
	var stopErr error
	_ = supervise.Go(ctx, "test.cgroup_unverified", func(ctx context.Context) error {
		proof, stopErr = proc.Stop(ctx)
		close(stopDone)
		return nil
	})

	<-proc.exitCh
	driveClockUntil(t, clk, stopDone)

	if stopErr != nil {
		t.Fatalf("Stop: %v", stopErr)
	}
	wantProof := StopProof{Status: StopProofUnknown, Reason: ReasonScopeUnverified}
	if diff := cmp.Diff(wantProof, proof); diff != "" {
		t.Fatalf("StopProof mismatch (-want +got):\n%s", diff)
	}
}

func testCgroupFallbackWhenProbeFails(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	cgRoot := t.TempDir() // no cgroup.controllers -> probe fails
	procRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(procRoot, "sys/kernel/random"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeRootFile(procRoot, "sys/kernel/random/boot_id", []byte("boot-uuid-fallback\n")); err != nil {
		t.Fatal(err)
	}

	proc, err := Spawn(ctx, &Options{
		Argv:           []string{exe, fakeVendorFlag, fakeVendorModeNormal},
		Clock:          clk,
		SessionID:      "sess-fallback",
		CgroupRoot:     cgRoot,
		ProcRoot:       procRoot,
		SystemdRunPath: nonexistentSystemdRun,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitReady(t, proc.Stdout())
	drainStdout(ctx, proc.Stdout())

	if proc.tracker == nil {
		t.Fatal("expected descendantTracker to be active when probe fails")
	}
	if proc.Identity().ScopeUnit != "" {
		t.Fatalf("expected empty ScopeUnit when probe fails, got %q", proc.Identity().ScopeUnit)
	}

	stopDone := make(chan struct{})
	var proof StopProof
	var stopErr error
	_ = supervise.Go(ctx, "test.cgroup_fallback", func(ctx context.Context) error {
		proof, stopErr = proc.Stop(ctx)
		close(stopDone)
		return nil
	})

	<-proc.exitCh
	driveClockUntil(t, clk, stopDone)

	if stopErr != nil {
		t.Fatalf("Stop: %v", stopErr)
	}
	wantProof := StopProof{Status: StopProofUnknown, Reason: ReasonScopeUnavailable}
	if diff := cmp.Diff(wantProof, proof); diff != "" {
		t.Fatalf("StopProof mismatch (-want +got):\n%s", diff)
	}
}

func testCgroupKillPreferredOverPerPidSigkill(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	cgRoot, procRoot := setupTempCgroupAndProc(t)
	stepCh := make(chan string, 10)

	helper := spawnReadyHelper(ctx, t, exe)
	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, fakeVendorModeIgnoreSigterm},
		Clock: clk,
		Env: []string{
			"FUGUE_TEST_CGROUP_ROOT=" + cgRoot,
			"FUGUE_TEST_PROC_ROOT=" + procRoot,
		},
		SessionID:      "sess-cgkill",
		CgroupRoot:     cgRoot,
		ProcRoot:       procRoot,
		SystemdRunPath: exe,
		OnEscalation: func(step string) {
			stepCh <- step
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitReady(t, proc.Stdout())
	drainStdout(ctx, proc.Stdout())

	unitDir := filepath.Join(cgRoot, "user.slice", "fugue-vendor-sess-cgkill.scope")
	prepareCgroupKillFixtures(t, unitDir, proc.PID(), helper.PID())

	stopDone := make(chan struct{})
	var proof StopProof
	var stopErr error
	_ = supervise.Go(ctx, "test.cgroup_kill_pref", func(ctx context.Context) error {
		proof, stopErr = proc.Stop(ctx)
		close(stopDone)
		return nil
	})

	expectEscalationStep(t, stepCh, escalationSigint)
	advancePending(clk, 5*time.Second)
	expectEscalationStep(t, stepCh, escalationSigterm)
	advancePending(clk, 5*time.Second)
	expectEscalationStep(t, stepCh, escalationSigkill)
	<-proc.exitCh
	markFakeScopeEmpty(t, unitDir)
	driveClockUntil(t, clk, stopDone)

	if stopErr != nil {
		t.Fatalf("Stop: %v", stopErr)
	}
	if proof.Status != StopProofProved {
		t.Fatalf("expected StopProofProved, got %+v", proof)
	}
	assertCgroupKillPreferred(t, unitDir, helper.PID())
}

func prepareCgroupKillFixtures(t *testing.T, unitDir string, leaderPID, helperPID int) {
	t.Helper()
	if err := writeRootFile(unitDir, "cgroup.kill", []byte("0")); err != nil {
		t.Fatal(err)
	}
	procsContent := fmt.Sprintf("%d\n%d\n", leaderPID, helperPID)
	if err := writeRootFile(unitDir, "cgroup.procs", []byte(procsContent)); err != nil {
		t.Fatal(err)
	}
}

func assertCgroupKillPreferred(t *testing.T, unitDir string, helperPID int) {
	t.Helper()
	killBytes, err := readRootFile(unitDir, "cgroup.kill")
	if err != nil {
		t.Fatalf("read cgroup.kill: %v", err)
	}
	if strings.TrimSpace(string(killBytes)) != "1" {
		t.Fatalf("expected cgroup.kill to contain 1, got %q", string(killBytes))
	}
	if err := syscall.Kill(helperPID, 0); err != nil {
		t.Fatalf("expected helper %d to survive because cgroup.kill was preferred over per-pid SIGKILL: %v", helperPID, err)
	}
}

func TestStopCleansAllDescendantsIncludingSetsidGrandchild(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	stepCh := make(chan string, 10)

	// Force the uncontained descendant tracker path on both Linux (/proc) and
	// macOS (sysctl kern.proc.all) by disabling systemd-run lookup.
	proc, err := Spawn(ctx, &Options{
		Argv:           []string{exe, fakeVendorFlag, fakeVendorModeSetsidGrandchild},
		Clock:          clk,
		SystemdRunPath: nonexistentSystemdRun,
		OnEscalation: func(step string) {
			stepCh <- step
		},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	grandchild := readPidLine(t, proc.Stdout(), "GRANDCHILD ")
	t.Cleanup(func() {
		if kerr := syscall.Kill(grandchild, syscall.SIGKILL); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
			t.Errorf("kill grandchild %d: %v", grandchild, kerr)
		}
	})
	waitReady(t, proc.Stdout())
	drainStdout(ctx, proc.Stdout())

	// Advance the clock by one trackerInterval so the periodic snapshot samples
	// the setsid grandchild while the leader is still alive.
	advancePending(clk, trackerInterval)
	for clk.Pending() == 0 {
		runtime.Gosched()
	}

	stopDone := make(chan struct{})
	var proof StopProof
	var stopErr error
	_ = supervise.Go(ctx, "test.stop_setsid_grandchild", func(ctx context.Context) error {
		proof, stopErr = proc.Stop(ctx)
		close(stopDone)
		return nil
	})

	expectEscalationStep(t, stepCh, escalationSigint)
	waitPidGone(t, grandchild)
	<-proc.exitCh
	driveClockUntil(t, clk, stopDone)

	if stopErr != nil {
		t.Fatalf("Stop: %v", stopErr)
	}
	if proof.Status != StopProofUnknown {
		t.Fatalf("expected StopProofUnknown on tracker path, got %+v", proof)
	}
	if kerr := syscall.Kill(grandchild, 0); !errors.Is(kerr, syscall.ESRCH) {
		t.Fatalf("expected setsid grandchild %d to be gone (ESRCH), got %v", grandchild, kerr)
	}
}

func TestReapRefusesMismatchedStartTime(t *testing.T) {
	t.Parallel()

	t.Run("refuses mismatched start_token without signaling", func(t *testing.T) {
		t.Parallel()
		testReapRefusesMismatch(t)
	})

	t.Run("reaps live process when start_token matches", func(t *testing.T) {
		t.Parallel()
		testReapMatchingStartToken(t)
	})

	t.Run("reaps scope when leader is gone and grandchild remains", func(t *testing.T) {
		t.Parallel()
		testReapScopeLeaderGoneGrandchildRemains(t)
	})
}

func testReapRefusesMismatch(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)

	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, fakeVendorModeNormal},
		Clock: clk,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() {
		if _, serr := proc.Stop(ctx); serr != nil {
			t.Errorf("cleanup Stop: %v", serr)
		}
	})
	waitReady(t, proc.Stdout())
	drainStdout(ctx, proc.Stdout())

	badID := Identity{
		PID:        proc.PID(),
		StartToken: proc.Identity().StartToken + "-stale",
		ScopeUnit:  "",
	}
	proof, reapErr := Reap(ctx, badID, &ReapOptions{Clock: clk})
	if !errors.Is(reapErr, ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch, got %v", reapErr)
	}
	if proof.Status != StopProofUnknown || proof.Reason != ReasonIdentityMismatch {
		t.Fatalf("unexpected proof: %+v", proof)
	}
	if kerr := syscall.Kill(proc.PID(), 0); kerr != nil {
		t.Fatalf("process %d should not have been signaled on mismatch: %v", proc.PID(), kerr)
	}
}

func testReapMatchingStartToken(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)

	proc, err := Spawn(ctx, &Options{
		Argv:  []string{exe, fakeVendorFlag, fakeVendorModeIgnoreSigterm},
		Clock: clk,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitReady(t, proc.Stdout())
	drainStdout(ctx, proc.Stdout())

	id := proc.Identity()
	id.ScopeUnit = "" // exercise the uncontained start_token verification path
	if id.StartToken == "" {
		t.Fatal("expected non-empty StartToken from running process")
	}

	reapDone := make(chan struct{})
	var proof StopProof
	var reapErr error
	_ = supervise.Go(ctx, "test.reap_matching", func(ctx context.Context) error {
		proof, reapErr = Reap(ctx, id, &ReapOptions{Clock: clk})
		close(reapDone)
		return nil
	})

	<-proc.exitCh
	driveClockUntil(t, clk, reapDone)

	if reapErr != nil {
		t.Fatalf("Reap: %v", reapErr)
	}
	wantProof := StopProof{Status: StopProofUnknown, Reason: ReasonGroupEmpty}
	if diff := cmp.Diff(wantProof, proof); diff != "" {
		t.Fatalf("Reap proof mismatch (-want +got):\n%s", diff)
	}
}

func testReapScopeLeaderGoneGrandchildRemains(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(testEpoch())
	exe := fakeVendorExe(t)
	cgRoot, procRoot := setupTempCgroupAndProc(t)

	grandchild := spawnReadyHelper(ctx, t, exe)
	unitName := "fugue-vendor-crashed-host.scope"
	unitDir := filepath.Join(cgRoot, "user.slice", unitName)
	setupOrphanedScopeFixtures(t, unitDir, grandchild.PID())

	reapDone := make(chan struct{})
	var proof StopProof
	var reapErr error
	_ = supervise.Go(ctx, "test.reap_scope", func(ctx context.Context) error {
		proof, reapErr = Reap(ctx, Identity{
			PID:        999999,
			StartToken: formatLinuxStartToken("boot-old", 111),
			ScopeUnit:  unitName,
		}, &ReapOptions{
			Clock:      clk,
			CgroupRoot: cgRoot,
			ProcRoot:   procRoot,
		})
		close(reapDone)
		return nil
	})

	<-grandchild.exitCh
	markFakeScopeEmpty(t, unitDir)
	driveClockUntil(t, clk, reapDone)

	if reapErr != nil {
		t.Fatalf("Reap scope: %v", reapErr)
	}
	wantProof := StopProof{Status: StopProofProved, Reason: ReasonCgroupEmpty}
	if diff := cmp.Diff(wantProof, proof); diff != "" {
		t.Fatalf("Reap scope proof mismatch (-want +got):\n%s", diff)
	}
}

func setupOrphanedScopeFixtures(t *testing.T, unitDir string, grandchildPID int) {
	t.Helper()
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeRootFile(unitDir, "cgroup.procs", []byte(strconv.Itoa(grandchildPID)+"\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeRootFile(unitDir, "cgroup.events", []byte("populated 1\n")); err != nil {
		t.Fatal(err)
	}
}
