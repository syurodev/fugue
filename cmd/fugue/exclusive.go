package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/pkg/client"
)

const (
	exclusiveCommand    = "exclusive"
	exclusiveSocketFlag = "--socket"
	exitSignaled        = 128
)

var errHeavyLeaseLost = errors.New("heavy command slot lease lost; child command terminated")

// parseExclusiveArgs parses `exclusive [--socket <path>] -- <cmd...>`.
func parseExclusiveArgs(args []string) (socket string, cmdArgs []string, matched, valid bool) {
	if len(args) == 0 || args[0] != exclusiveCommand {
		return "", nil, false, false
	}
	rest := args[1:]
	for len(rest) > 0 {
		arg := rest[0]
		if arg == "--" {
			return socket, rest[1:], true, len(rest) > 1
		}
		if arg == exclusiveSocketFlag {
			if len(rest) < 2 || rest[1] == "" {
				return "", nil, true, false
			}
			socket = rest[1]
			rest = rest[2:]
			continue
		}
		if val, ok := strings.CutPrefix(arg, exclusiveSocketFlag+"="); ok && val != "" {
			socket = val
			rest = rest[1:]
			continue
		}
		return "", nil, true, false
	}
	return "", nil, true, false
}

// RunExclusive acquires a §7 heavy-command semaphore slot from the running host
// and runs cmdArgs with that slot held, terminating the child process group if
// the host closes or drops the lease stream.
func RunExclusive(ctx context.Context, in io.Reader, out, errOut io.Writer, env config.Env, socket string, cmdArgs []string, ver string) int {
	if len(cmdArgs) == 0 {
		return exitUsage
	}
	resolvedSocket, err := resolveExclusiveSocket(env, socket)
	if err != nil {
		writeCommandErr(errOut, err)
		return exitFailure
	}
	slot, err := acquireExclusiveSlot(ctx, env, resolvedSocket, ver)
	if err != nil {
		writeCommandErr(errOut, err)
		return exitFailure
	}
	defer slot.release()
	return runExclusiveCommand(ctx, in, out, errOut, cmdArgs, slot)
}

func resolveExclusiveSocket(env config.Env, socket string) (string, error) {
	if socket != "" {
		return socket, nil
	}
	dataDir, err := client.DataDir(env.Lookup, os.UserConfigDir)
	if err != nil {
		return "", fmt.Errorf("resolve fugue data dir: %w", err)
	}
	hf, err := client.ReadHostFile(dataDir)
	if err != nil {
		return "", fmt.Errorf("read host discovery: %w", err)
	}
	return hf.SocketPath, nil
}

type exclusiveSlot struct {
	lease     *client.HeavyLease
	lost      chan struct{}
	mu        sync.Mutex
	err       error
	watchTask *supervise.Task
	closeConn func()
}

func startExclusiveSlot(ctx context.Context, lease *client.HeavyLease, closeConn func()) *exclusiveSlot {
	slot := &exclusiveSlot{
		lease:     lease,
		lost:      make(chan struct{}),
		closeConn: closeConn,
	}
	slot.watchTask = supervise.Go(context.WithoutCancel(ctx), "exclusive.lease-watch", func(context.Context) error {
		err := lease.Wait()
		slot.mu.Lock()
		slot.err = err
		slot.mu.Unlock()
		close(slot.lost)
		return nil
	})
	return slot
}

func (s *exclusiveSlot) release() {
	if s == nil {
		return
	}
	s.lease.Release()
	if s.watchTask != nil {
		<-s.watchTask.Done()
	}
	if s.closeConn != nil {
		s.closeConn()
	}
}

func (s *exclusiveSlot) lossError() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func acquireExclusiveSlot(ctx context.Context, env config.Env, socket, ver string) (*exclusiveSlot, error) {
	if token, err := sessionToken(env); err == nil {
		sess, dialErr := client.DialSession(ctx, socket, token)
		if dialErr != nil {
			return nil, fmt.Errorf("attach session client: %w", dialErr)
		}
		lease, slotErr := sess.AcquireHeavySlot(ctx)
		if slotErr != nil {
			sess.Close()
			return nil, fmt.Errorf("acquire session heavy slot: %w", slotErr)
		}
		return startExclusiveSlot(ctx, lease, sess.Close), nil
	}
	cli, dialErr := client.Dial(ctx, socket, client.Params{
		Kind:        client.KindCLI,
		Name:        "fugue",
		Version:     ver,
		APIVersions: []string{"v1"},
	})
	if dialErr != nil {
		return nil, fmt.Errorf("dial fugue host: %w", dialErr)
	}
	lease, slotErr := cli.AcquireHeavySlot(ctx)
	if slotErr != nil {
		cli.Close()
		return nil, fmt.Errorf("acquire cli heavy slot: %w", slotErr)
	}
	return startExclusiveSlot(ctx, lease, cli.Close), nil
}

type exclusiveHooks struct {
	prepareProcess    func(io.Reader) *syscall.SysProcAttr
	restoreForeground func(*syscall.SysProcAttr)
}

func runExclusiveCommand(ctx context.Context, in io.Reader, out, errOut io.Writer, cmdArgs []string, slot *exclusiveSlot) int {
	return runExclusiveCommandWithHooks(ctx, in, out, errOut, cmdArgs, slot, defaultExclusiveHooks())
}

func runExclusiveCommandWithHooks(ctx context.Context, in io.Reader, out, errOut io.Writer, cmdArgs []string, slot *exclusiveSlot, hooks exclusiveHooks) int {
	if isLeaseLost(slot) {
		writeCommandErr(errOut, formatLeaseLostErr(slot.lossError()))
		return exitFailure
	}
	bin, err := exec.LookPath(cmdArgs[0])
	if err != nil {
		writeCommandErr(errOut, fmt.Errorf("locate %s: %w", cmdArgs[0], err))
		return exitFailure
	}
	attr := exclusiveProcAttr(in)
	if hooks.prepareProcess != nil {
		attr = hooks.prepareProcess(in)
	}
	cmd := &exec.Cmd{
		Path:        bin,
		Args:        cmdArgs,
		Stdin:       in,
		Stdout:      out,
		Stderr:      errOut,
		SysProcAttr: attr,
	}
	if hooks.restoreForeground != nil {
		defer hooks.restoreForeground(attr)
	}
	if err = cmd.Start(); err != nil {
		writeCommandErr(errOut, fmt.Errorf("start %s: %w", cmdArgs[0], err))
		return exitFailure
	}
	err, leaseLost := waitExclusiveCommand(ctx, cmd, slot)
	if leaseLost {
		writeCommandErr(errOut, formatLeaseLostErr(slot.lossError()))
		return exclusiveExitCode(err, true)
	}
	if err == nil {
		return exitOK
	}
	if code, ok := exclusiveProcessExitCode(err); ok {
		return code
	}
	writeCommandErr(errOut, fmt.Errorf("run %s: %w", cmdArgs[0], err))
	return exitFailure
}

func waitExclusiveCommand(ctx context.Context, cmd *exec.Cmd, slot *exclusiveSlot) (error, bool) {
	var lostCh <-chan struct{}
	if slot != nil {
		lostCh = slot.lost
	}
	var leaseLost atomic.Bool
	watchCtx, cancelWatch := context.WithCancel(ctx)
	killWatch := supervise.Go(watchCtx, "exclusive.kill-watch", func(wCtx context.Context) error {
		select {
		case <-wCtx.Done():
			if ctx.Err() != nil {
				killExclusiveProcessGroup(cmd.Process)
			}
		case <-lostCh:
			leaseLost.Store(true)
			killExclusiveProcessGroup(cmd.Process)
		}
		return nil
	})
	waitErr := cmd.Wait()
	cancelWatch()
	<-killWatch.Done()
	if waitErr != nil {
		return fmt.Errorf("wait %s: %w", cmd.Path, waitErr), leaseLost.Load()
	}
	return nil, leaseLost.Load()
}

func isLeaseLost(slot *exclusiveSlot) bool {
	if slot == nil {
		return false
	}
	select {
	case <-slot.lost:
		return true
	default:
		return false
	}
}

func formatLeaseLostErr(cause error) error {
	if cause == nil {
		return errHeavyLeaseLost
	}
	return fmt.Errorf("%w: %w", errHeavyLeaseLost, cause)
}

func exclusiveProcessExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 0, false
	}
	if code := exitErr.ExitCode(); code >= 0 {
		return code, true
	}
	if code, ok := signalExitCode(exitErr); ok {
		return code, true
	}
	return exitFailure, true
}

func exclusiveExitCode(err error, forceNonZero bool) int {
	if err == nil {
		if forceNonZero {
			return exitFailure
		}
		return exitOK
	}
	if code, ok := exclusiveProcessExitCode(err); ok {
		return code
	}
	return exitFailure
}

func writeCommandErr(errOut io.Writer, err error) {
	if err == nil || errOut == nil {
		return
	}
	if serr := say(errOut, "fugue: %v", err); serr != nil {
		slog.Error("write command error", "err", serr)
	}
}
