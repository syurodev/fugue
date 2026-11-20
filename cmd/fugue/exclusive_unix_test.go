//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/testutil"
)

var errUnexpectedReadyLine = errors.New("unexpected ready line")

type runningExclusiveHelper struct {
	grandchildPID int
	codeCh        <-chan int
	errOut        *bytes.Buffer
	done          func()
}

func startReadyReader(t *testing.T, ctx context.Context, pr *io.PipeReader) (*supervise.Task, <-chan error) {
	t.Helper()
	readyCh := make(chan error, 1)
	readTask := supervise.Go(ctx, "test.exclusive-ready", func(context.Context) error {
		line, rerr := bufio.NewReader(pr).ReadString('\n')
		if rerr == nil && line != "ready\n" {
			rerr = fmt.Errorf("%w: %q", errUnexpectedReadyLine, line)
		}
		readyCh <- rerr
		return nil
	})
	return readTask, readyCh
}

func readPIDFromRoot(t *testing.T, dir, name string) int {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open pid dir: %v", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close pid dir: %v", cerr)
		}
	}()
	rawPID, err := root.ReadFile(name)
	if err != nil {
		t.Fatalf("read grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatalf("parse grandchild pid %q: %v", string(rawPID), err)
	}
	return pid
}

func startExclusiveGroupChild(t *testing.T, ctx context.Context, sockPath string) runningExclusiveHelper {
	t.Helper()
	pidDir := t.TempDir()
	pr, pw := io.Pipe()
	var errOut bytes.Buffer
	codeCh := make(chan int, 1)
	readTask, readyCh := startReadyReader(t, context.WithoutCancel(ctx), pr)

	cmdLine := fmt.Sprintf("sleep 60 & echo $! > %s/grandchild.pid; printf 'ready\\n'; wait", pidDir)
	runTask := supervise.Go(context.WithoutCancel(ctx), "test.exclusive-run", func(context.Context) error {
		code := RunExclusive(ctx, bytes.NewBuffer(nil), pw, &errOut, config.MapEnv{app.SessionTokenEnv: bridgeTestToken}, sockPath, []string{"sh", "-c", cmdLine}, testVersion)
		if cerr := pw.Close(); cerr != nil {
			t.Errorf("close pipe writer: %v", cerr)
		}
		codeCh <- code
		return nil
	})

	t.Cleanup(func() {
		if cerr := pr.Close(); cerr != nil && !errors.Is(cerr, io.ErrClosedPipe) {
			t.Errorf("close pipe reader: %v", cerr)
		}
		<-readTask.Done()
		<-runTask.Done()
	})

	if err := testutil.Await(t, readyCh); err != nil {
		t.Fatalf("wait for child ready: %v", err)
	}
	pid := readPIDFromRoot(t, pidDir, "grandchild.pid")
	t.Cleanup(func() {
		if kerr := syscall.Kill(pid, syscall.SIGKILL); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
			t.Errorf("cleanup grandchild %d: %v", pid, kerr)
		}
	})
	return runningExclusiveHelper{
		grandchildPID: pid,
		codeCh:        codeCh,
		errOut:        &errOut,
		done:          func() { <-runTask.Done() },
	}
}

func TestRunExclusiveKillsProcessGroupWhenServerClosesStream(t *testing.T) {
	t.Parallel()
	stub := &stubHost{dropStream: make(chan struct{})}
	sockPath := serveStub(t, stub)
	h := startExclusiveGroupChild(t, t.Context(), sockPath)

	close(stub.dropStream)
	code := testutil.Await(t, h.codeCh)
	h.done()

	wantCode := exitSignaled + int(syscall.SIGKILL)
	if code != wantCode {
		t.Fatalf("RunExclusive exit code = %d, want %d (stderr=%q)", code, wantCode, h.errOut.String())
	}
	if !strings.Contains(h.errOut.String(), "lease lost") {
		t.Fatalf("stderr = %q, want lease lost message", h.errOut.String())
	}
	testutil.Poll(t, "grandchild process exits after stream close", func() bool {
		return errors.Is(syscall.Kill(h.grandchildPID, 0), syscall.ESRCH)
	})
}

func TestRunExclusiveContextCancelAndSignalExitCode(t *testing.T) {
	t.Parallel()
	sockPath := serveStub(t, &stubHost{})
	ctx, cancel := context.WithCancel(t.Context())
	h := startExclusiveGroupChild(t, ctx, sockPath)

	cancel()
	code := testutil.Await(t, h.codeCh)
	h.done()

	wantKill := exitSignaled + int(syscall.SIGKILL)
	if code != wantKill {
		t.Fatalf("cancelled RunExclusive exit code = %d, want %d", code, wantKill)
	}
	testutil.Poll(t, "grandchild process exits after cancel", func() bool {
		return errors.Is(syscall.Kill(h.grandchildPID, 0), syscall.ESRCH)
	})

	var out, errOut bytes.Buffer
	sigCode := RunExclusive(t.Context(), bytes.NewBuffer(nil), &out, &errOut, config.MapEnv{app.SessionTokenEnv: bridgeTestToken}, sockPath, []string{"sh", "-c", "kill -TERM $$"}, testVersion)
	wantTerm := exitSignaled + int(syscall.SIGTERM)
	if sigCode != wantTerm {
		t.Fatalf("signaled child exit code = %d, want %d (stderr=%q)", sigCode, wantTerm, errOut.String())
	}
}

func assertProcessSpec(t *testing.T, name string, spec exclusiveProcessSpec, wantFG bool, wantCtty, wantRestorePgrp int) {
	t.Helper()
	attr := spec.sysProcAttr
	if attr == nil || !attr.Setpgid || attr.Foreground != wantFG || attr.Ctty != wantCtty ||
		spec.hasForeground != wantFG || spec.restorePgrp != wantRestorePgrp {
		t.Fatalf("%s = spec%+v attr%+v, want Setpgid=true Foreground=%v Ctty=%d restorePgrp=%d",
			name, spec, attr, wantFG, wantCtty, wantRestorePgrp)
	}
}

func TestExclusiveProcAttrTTYSelection(t *testing.T) {
	t.Parallel()
	assertProcessSpec(t, "non-tty", selectExclusiveProcessSpec(exclusiveTTYProbe{isTTY: false}, 100), false, 0, 0)
	assertProcessSpec(t, "tty fg==pgrp", selectExclusiveProcessSpec(exclusiveTTYProbe{fd: 9, isTTY: true, fgPgrp: 100}, 100), true, 9, 100)
	assertProcessSpec(t, "tty fg!=pgrp", selectExclusiveProcessSpec(exclusiveTTYProbe{fd: 9, isTTY: true, fgPgrp: 200}, 100), false, 0, 0)
	assertProcessSpec(t, "bytes.Buffer", prepareExclusiveProcess(bytes.NewBuffer(nil)), false, 0, 0)

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := pr.Close(); cerr != nil {
			t.Errorf("close pipe reader: %v", cerr)
		}
		if cerr := pw.Close(); cerr != nil {
			t.Errorf("close pipe writer: %v", cerr)
		}
	})
	assertProcessSpec(t, "os.Pipe", prepareExclusiveProcess(pr), false, 0, 0)
}

func newFakeTTYRestoreHooks(t *testing.T, fakeFD, fgPgrp, selfPgrp int) (exclusiveHooks, *[][2]int) {
	t.Helper()
	var calls [][2]int
	selected := selectExclusiveProcessSpec(exclusiveTTYProbe{fd: fakeFD, isTTY: true, fgPgrp: fgPgrp}, selfPgrp)
	hooks := exclusiveHooks{
		prepareProcess: func(io.Reader) exclusiveProcessSpec {
			// Run the real child without a kernel-bound controlling TTY fd, while keeping selected's restore state.
			execSpec := selected
			execSpec.sysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			return execSpec
		},
		restoreForeground: func(spec exclusiveProcessSpec) {
			restoreForegroundPgrp(spec, func(fd, pgrp int) error {
				calls = append(calls, [2]int{fd, pgrp})
				return nil
			})
		},
	}
	return hooks, &calls
}

func assertLeaseLostRestoresForeground(t *testing.T, fakeFD, fakeFG int) {
	t.Helper()
	stub := &stubHost{dropStream: make(chan struct{})}
	sockPath := serveStub(t, stub)
	slot, err := acquireExclusiveSlot(t.Context(), config.MapEnv{app.SessionTokenEnv: bridgeTestToken}, sockPath, testVersion)
	if err != nil {
		t.Fatalf("acquireExclusiveSlot: %v", err)
	}
	defer slot.release()

	hooksLost, callsLost := newFakeTTYRestoreHooks(t, fakeFD, fakeFG, fakeFG)
	pr, pw := io.Pipe()
	readTask, readyCh := startReadyReader(t, t.Context(), pr)
	codeCh := make(chan int, 1)
	var errLost bytes.Buffer
	runTask := supervise.Go(t.Context(), "test.exclusive-tty-lost", func(ctx context.Context) error {
		c := runExclusiveCommandWithHooks(ctx, bytes.NewBuffer(nil), pw, &errLost, []string{"sh", "-c", "printf 'ready\\n'; sleep 60"}, slot, hooksLost)
		if cerr := pw.Close(); cerr != nil {
			t.Errorf("close pipe writer: %v", cerr)
		}
		codeCh <- c
		return nil
	})
	t.Cleanup(func() {
		if cerr := pr.Close(); cerr != nil && !errors.Is(cerr, io.ErrClosedPipe) {
			t.Errorf("close pipe reader: %v", cerr)
		}
		<-readTask.Done()
		<-runTask.Done()
	})

	if rerr := testutil.Await(t, readyCh); rerr != nil {
		t.Fatalf("wait ready: %v", rerr)
	}
	close(stub.dropStream)
	lostCode := testutil.Await(t, codeCh)
	<-runTask.Done()

	if lostCode != exitSignaled+int(syscall.SIGKILL) {
		t.Fatalf("lease lost exit = %d, want %d", lostCode, exitSignaled+int(syscall.SIGKILL))
	}
	if len(*callsLost) != 1 || (*callsLost)[0] != [2]int{fakeFD, fakeFG} {
		t.Fatalf("lease lost tcsetpgrp calls = %v, want [[%d %d]]", *callsLost, fakeFD, fakeFG)
	}
}

func TestRunExclusiveRestoresForegroundOnSuccessAndLeaseLost(t *testing.T) {
	t.Parallel()
	const fakeFD, fakeFG = 77, 4242

	hooksSuccess, callsSuccess := newFakeTTYRestoreHooks(t, fakeFD, fakeFG, fakeFG)
	var out, errOut bytes.Buffer
	code := runExclusiveCommandWithHooks(t.Context(), bytes.NewBuffer(nil), &out, &errOut, []string{"sh", "-c", "true"}, nil, hooksSuccess)
	if code != exitOK {
		t.Fatalf("success path exit = %d (stderr=%q), want 0", code, errOut.String())
	}
	if len(*callsSuccess) != 1 || (*callsSuccess)[0] != [2]int{fakeFD, fakeFG} {
		t.Fatalf("success path tcsetpgrp calls = %v, want [[%d %d]]", *callsSuccess, fakeFD, fakeFG)
	}

	hooksBG, callsBG := newFakeTTYRestoreHooks(t, fakeFD, fakeFG, fakeFG+1)
	if bgCode := runExclusiveCommandWithHooks(t.Context(), bytes.NewBuffer(nil), &out, &errOut, []string{"sh", "-c", "true"}, nil, hooksBG); bgCode != exitOK {
		t.Fatalf("background tty exit = %d, want 0", bgCode)
	}
	if len(*callsBG) != 0 {
		t.Fatalf("background tty should not restore foreground, got calls %v", *callsBG)
	}

	assertLeaseLostRestoresForeground(t, fakeFD, fakeFG)
}
