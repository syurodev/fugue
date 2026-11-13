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
