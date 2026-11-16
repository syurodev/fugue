//go:build darwin

package process

import (
	"errors"
	"io"
	"os/exec"
	"runtime"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSignalTreeTreatsZombieOnlyGroupEPERMAsGone(t *testing.T) {
	t.Parallel()
	const attempts = 200
	observedEPERM := 0
	for range attempts {
		if runZombieGroupAttempt(t) {
			observedEPERM++
		}
	}
	if observedEPERM == 0 {
		t.Fatalf("EPERM was not observed in %d zombie-only process-group attempts", attempts)
	}
	t.Logf("observed EPERM for zombie-only process groups: %d/%d attempts", observedEPERM, attempts)
}

func runZombieGroupAttempt(t *testing.T) bool {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	if startErr := cmd.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	if _, copyErr := io.Copy(io.Discard, stdout); copyErr != nil {
		reapCommand(t, cmd)
		t.Fatal(copyErr)
	}
	pgid := cmd.Process.Pid
	zombie, live := waitForZombieGroup(t, pgid)
	if zombie == 0 || live != 0 {
		reapCommand(t, cmd)
		t.Fatalf("process group %d has %d zombies and %d live members", pgid, zombie, live)
	}
	killErr := syscall.Kill(-pgid, syscall.SIGKILL)
	if got := groupSignalError(pgid, killErr); got != nil {
		reapCommand(t, cmd)
		t.Fatalf("signal error for zombie-only process group: %v", got)
	}
	if waitErr := cmd.Wait(); waitErr != nil {
		t.Fatal(waitErr)
	}
	return errors.Is(killErr, syscall.EPERM)
}

func reapCommand(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if waitErr := cmd.Wait(); waitErr != nil {
		t.Logf("wait after failure: %v", waitErr)
	}
}

func TestSignalTreeKeepsEPERMWhenLiveMemberRemains(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(t.Context(), "/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			t.Logf("cleanup kill: %v", killErr)
		}
		if waitErr := cmd.Wait(); waitErr != nil {
			t.Logf("cleanup wait: %v", waitErr)
		}
	})

	live, err := sysctlGroupHasLiveMembers(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !live {
		t.Fatalf("expected live process group member for %d", cmd.Process.Pid)
	}
	// This test owns the group, so macOS permits the real signal. Supplying the
	// EPERM result exercises the classifier while sysctl confirms a live member.
	if err := groupSignalError(cmd.Process.Pid, syscall.EPERM); !errors.Is(err, syscall.EPERM) {
		t.Fatalf("live process-group EPERM = %v, want EPERM", err)
	}
}

func waitForZombieGroup(t *testing.T, pgid int) (zombies, live int) {
	t.Helper()
	for range 10000 {
		kprocs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
		if err != nil {
			t.Fatal(err)
		}
		zombies, live = 0, 0
		for i := range kprocs {
			proc := &kprocs[i]
			if int(proc.Eproc.Pgid) != pgid {
				continue
			}
			if proc.Proc.P_stat == sZomb {
				zombies++
			} else if proc.Proc.P_stat != 0 {
				live++
			}
		}
		if zombies > 0 {
			return zombies, live
		}
		runtime.Gosched()
	}
	t.Fatalf("process group %d did not reach zombie state", pgid)
	return 0, 0
}

// TestSignalErrorsForAMemberMidExitAreGone reproduces the window after
// SIGKILL in which XNU has set P_WEXIT but the process is still SRUN: every
// signal to it fails with EPERM although the process table lists it as not
// yet a zombie. The window is real but short and depends on runner speed, so
// this real-kernel check asserts only when it sees the window; the
// deterministic guarantee is TestKinfoCanBeSignaled.
func TestSignalErrorsForAMemberMidExitAreGone(t *testing.T) {
	t.Parallel()
	const attempts = 300
	observed := 0
	for range attempts {
		observed += midExitAttempt(t)
	}
	t.Logf("mid-exit EPERM observations: %d in %d attempts", observed, attempts)
}

func TestKinfoCanBeSignaled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		stat  int8
		flag  int32
		alive bool
	}{
		{"running", 2, 0x4004, true},
		{"running mid-exit", 2, 0x4004 | pWExit, false},
		{"zombie", sZomb, 0x4004, false},
		{"zombie mid-exit", sZomb, 0x4004 | pWExit, false},
		{"no state", 0, 0, false},
	}
	for _, tc := range cases {
		var kp unix.KinfoProc
		kp.Proc.P_stat = tc.stat
		kp.Proc.P_flag = tc.flag
		if got := kinfoCanBeSignaled(&kp); got != tc.alive {
			t.Errorf("%s: kinfoCanBeSignaled = %v, want %v", tc.name, got, tc.alive)
		}
	}
}

// midExitAttempt kills a one-process group and, each time a signal fails
// with EPERM while the process is mid-exit, checks that both stop-path
// filters call it gone. It returns the number of such observations.
func midExitAttempt(t *testing.T) int {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		reapKilled(t, cmd)
		t.Fatalf("first kill: %v", err)
	}
	observed := 0
	for range 20000 {
		kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err != nil || kp.Proc.P_stat == sZomb {
			break
		}
		groupErr := syscall.Kill(-pid, syscall.SIGKILL)
		if !errors.Is(groupErr, syscall.EPERM) || kp.Proc.P_flag&pWExit == 0 {
			continue
		}
		observed++
		if got := groupSignalError(pid, groupErr); got != nil {
			reapKilled(t, cmd)
			t.Fatalf("group EPERM for a mid-exit member = %v, want nil", got)
		}
		leaderErr := syscall.Kill(pid, syscall.SIGKILL)
		if got := platformLeaderSignalError(pid, leaderErr); got != nil {
			reapKilled(t, cmd)
			t.Fatalf("leader signal %v for a mid-exit leader = %v, want nil", leaderErr, got)
		}
	}
	reapKilled(t, cmd)
	return observed
}

// reapKilled waits for a command the test killed; the kill is the expected
// exit, any other wait error is real.
func reapKilled(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	var exitErr *exec.ExitError
	if err := cmd.Wait(); err != nil && !errors.As(err, &exitErr) {
		t.Errorf("wait: %v", err)
	}
}

func TestLeaderSignalEPERMIsKeptForALiveLeader(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(t.Context(), "/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if killErr := cmd.Process.Kill(); killErr != nil {
			t.Logf("cleanup kill: %v", killErr)
		}
		reapCommand(t, cmd)
	})
	if got := platformLeaderSignalError(cmd.Process.Pid, syscall.EPERM); !errors.Is(got, syscall.EPERM) {
		t.Fatalf("live leader EPERM = %v, want EPERM kept", got)
	}
}
