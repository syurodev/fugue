//go:build darwin

package process

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSignalTreeTreatsZombieOnlyGroupEPERMAsGone(t *testing.T) {
	const attempts = 200
	observedEPERM := 0
	for range attempts {
		cmd := exec.Command("/bin/sh", "-c", "exit 0")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, stdout); err != nil {
			_ = cmd.Wait()
			t.Fatal(err)
		}
		pgid := cmd.Process.Pid
		zombie, live := waitForZombieGroup(t, pgid)
		if zombie == 0 || live != 0 {
			_ = cmd.Wait()
			t.Fatalf("process group %d has %d zombies and %d live members", pgid, zombie, live)
		}
		err = syscall.Kill(-pgid, syscall.SIGKILL)
		if errors.Is(err, syscall.EPERM) {
			observedEPERM++
			if got := groupSignalError(pgid, err); got != nil {
				_ = cmd.Wait()
				t.Fatalf("zombie-only process group EPERM was not ignored: %v", got)
			}
		} else if got := groupSignalError(pgid, err); got != nil {
			_ = cmd.Wait()
			t.Fatalf("signal error for zombie-only process group: %v", got)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	if observedEPERM == 0 {
		t.Fatalf("EPERM was not observed in %d zombie-only process-group attempts", attempts)
	}
	fmt.Printf("observed EPERM for zombie-only process groups: %d/%d attempts\n", observedEPERM, attempts)
}

func TestSignalTreeKeepsEPERMWhenLiveMemberRemains(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}()

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
