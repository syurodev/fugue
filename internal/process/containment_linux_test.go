//go:build linux

package process

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func TestLinuxRealSystemdScopeOrFallback(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeVendorExe(t)
	probe := NewSystemdProbe()

	hostEnv := os.Environ()
	xdgRuntimeDir := lookupEnvFromSlice(hostEnv, "XDG_RUNTIME_DIR")
	filteredEnv := FilterEnv(hostEnv, DefaultAllowlist(), nil, nil)
	launcherEnv := withXDGRuntimeDir(filteredEnv, xdgRuntimeDir)
	contained := xdgRuntimeDir != "" && probe.check(ctx, defaultSystemdRun, defaultCgroupRoot, launcherEnv)

	sessionID := fmt.Sprintf("real-systemd-%d", os.Getpid())
	if contained {
		t.Logf("real-systemd test taking contained branch")
		runRealSystemdContainedBranch(ctx, t, exe, filteredEnv, xdgRuntimeDir, sessionID, probe)
		return
	}

	t.Logf("real-systemd test taking uncontained fallback branch (scope_unavailable)")
	runRealSystemdFallbackBranch(ctx, t, exe, filteredEnv, xdgRuntimeDir, sessionID, probe)
}

func lookupEnvFromSlice(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if val, ok := strings.CutPrefix(entry, prefix); ok {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func runRealSystemdContainedBranch(
	ctx context.Context,
	t *testing.T,
	exe string,
	filteredEnv []string,
	xdgRuntimeDir string,
	sessionID string,
	probe *SystemdProbe,
) {
	t.Helper()
	clk := clock.Real{}
	unitName := scopeUnitPrefix + sessionID + scopeUnitSuffix

	proc, err := Spawn(ctx, &Options{
		Argv:          []string{exe, fakeVendorFlag, fakeVendorModeSetsidGrandchild},
		Clock:         clk,
		Env:           filteredEnv,
		SessionID:     sessionID,
		XDGRuntimeDir: xdgRuntimeDir,
		Probe:         probe,
	})
	if err != nil {
		t.Fatalf("Spawn contained: %v", err)
	}

	grandchild := readPidLine(t, proc.Stdout(), "GRANDCHILD ")
	t.Cleanup(func() {
		if kerr := syscall.Kill(grandchild, syscall.SIGKILL); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
			t.Errorf("kill grandchild %d: %v", grandchild, kerr)
		}
	})
	waitReady(t, proc.Stdout())
	drainStdout(ctx, proc.Stdout())

	if proc.Identity().ScopeUnit != unitName {
		t.Fatalf("expected ScopeUnit %q, got %q (uncontainedReason=%q)", unitName, proc.Identity().ScopeUnit, proc.uncontainedReason)
	}

	proof, stopErr := proc.Stop(ctx)
	if stopErr != nil {
		t.Fatalf("Stop contained: %v", stopErr)
	}
	wantProof := StopProof{Status: StopProofProved, Reason: ReasonCgroupEmpty}
	if diff := cmp.Diff(wantProof, proof); diff != "" {
		t.Fatalf("StopProof mismatch (-want +got):\n%s", diff)
	}

	if !waitPollCondition(ctx, clk, scopeVerifyTimeout, func() bool {
		_, findErr := findScopeDir(defaultCgroupRoot, defaultProcRoot, unitName)
		return findErr != nil
	}) {
		t.Fatalf("timed out waiting for transient scope %s to be removed", unitName)
	}
	if !waitPollCondition(ctx, clk, scopeVerifyTimeout, func() bool {
		return errors.Is(syscall.Kill(grandchild, 0), syscall.ESRCH)
	}) {
		t.Fatalf("expected setsid grandchild %d to be gone (ESRCH)", grandchild)
	}
}

func runRealSystemdFallbackBranch(
	ctx context.Context,
	t *testing.T,
	exe string,
	filteredEnv []string,
	xdgRuntimeDir string,
	sessionID string,
	probe *SystemdProbe,
) {
	t.Helper()
	clk := clock.NewFake(testEpoch())
	proc, err := Spawn(ctx, &Options{
		Argv:          []string{exe, fakeVendorFlag, fakeVendorModeSetsidGrandchild},
		Clock:         clk,
		Env:           filteredEnv,
		SessionID:     sessionID,
		XDGRuntimeDir: xdgRuntimeDir,
		Probe:         probe,
	})
	if err != nil {
		t.Fatalf("Spawn fallback: %v", err)
	}
	grandchild := readPidLine(t, proc.Stdout(), "GRANDCHILD ")
	t.Cleanup(func() {
		if kerr := syscall.Kill(grandchild, syscall.SIGKILL); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
			t.Errorf("kill grandchild %d: %v", grandchild, kerr)
		}
	})
	waitReady(t, proc.Stdout())
	drainStdout(ctx, proc.Stdout())
	if proc.Identity().ScopeUnit != "" {
		t.Fatalf("expected empty ScopeUnit on fallback, got %q", proc.Identity().ScopeUnit)
	}

	advancePending(ctx, t, clk, trackerInterval)
	awaitPending(ctx, t, clk, 1)
	stopDone := make(chan struct{})
	var proof StopProof
	var stopErr error
	_ = supervise.Go(ctx, "test.real_systemd_fallback_stop", func(ctx context.Context) error {
		proof, stopErr = proc.Stop(ctx)
		close(stopDone)
		return nil
	})
	waitPidGone(t, grandchild)
	<-proc.exitCh
	<-proc.stderrTask.Done()
	<-proc.stdoutPipe.ended
	driveClockUntil(t, clk, stopDone)
	if stopErr != nil {
		t.Fatalf("Stop fallback: %v", stopErr)
	}
	wantProof := StopProof{Status: StopProofUnknown, Reason: ReasonScopeUnavailable}
	if diff := cmp.Diff(wantProof, proof); diff != "" {
		t.Fatalf("StopProof mismatch (-want +got):\n%s", diff)
	}
	if kerr := syscall.Kill(grandchild, 0); !errors.Is(kerr, syscall.ESRCH) {
		t.Fatalf("expected setsid grandchild %d to be gone (ESRCH), got %v", grandchild, kerr)
	}
}
