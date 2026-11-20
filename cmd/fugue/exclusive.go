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

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/pkg/client"
)

const (
	exclusiveCommand    = "exclusive"
	exclusiveSocketFlag = "--socket"
)

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
// and runs cmdArgs with that slot held.
func RunExclusive(ctx context.Context, in io.Reader, out, errOut io.Writer, env config.Env, socket string, cmdArgs []string, ver string) int {
	if len(cmdArgs) == 0 {
		return exitUsage
	}
	resolvedSocket, err := resolveExclusiveSocket(env, socket)
	if err != nil {
		writeCommandErr(errOut, err)
		return exitFailure
	}
	release, err := acquireExclusiveSlot(ctx, env, resolvedSocket, ver)
	if err != nil {
		writeCommandErr(errOut, err)
		return exitFailure
	}
	defer release()
	return runExclusiveCommand(ctx, in, out, errOut, cmdArgs)
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

func acquireExclusiveSlot(ctx context.Context, env config.Env, socket, ver string) (func(), error) {
	if token, err := sessionToken(env); err == nil {
		sess, dialErr := client.DialSession(ctx, socket, token)
		if dialErr != nil {
			return nil, fmt.Errorf("attach session client: %w", dialErr)
		}
		release, slotErr := sess.AcquireHeavySlot(ctx)
		if slotErr != nil {
			sess.Close()
			return nil, fmt.Errorf("acquire session heavy slot: %w", slotErr)
		}
		return func() {
			release()
			sess.Close()
		}, nil
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
	release, slotErr := cli.AcquireHeavySlot(ctx)
	if slotErr != nil {
		cli.Close()
		return nil, fmt.Errorf("acquire cli heavy slot: %w", slotErr)
	}
	return func() {
		release()
		cli.Close()
	}, nil
}

func runExclusiveCommand(ctx context.Context, in io.Reader, out, errOut io.Writer, cmdArgs []string) int {
	bin, err := exec.LookPath(cmdArgs[0])
	if err != nil {
		writeCommandErr(errOut, fmt.Errorf("locate %s: %w", cmdArgs[0], err))
		return exitFailure
	}
	cmd := &exec.Cmd{
		Path:   bin,
		Args:   cmdArgs,
		Stdin:  in,
		Stdout: out,
		Stderr: errOut,
	}
	if err = cmd.Start(); err != nil {
		writeCommandErr(errOut, fmt.Errorf("start %s: %w", cmdArgs[0], err))
		return exitFailure
	}
	stop := context.AfterFunc(ctx, func() { killExclusiveProcess(cmd.Process) })
	err = cmd.Wait()
	stop()
	if err == nil {
		return exitOK
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	writeCommandErr(errOut, fmt.Errorf("run %s: %w", cmdArgs[0], err))
	return exitFailure
}

func killExclusiveProcess(proc *os.Process) {
	if proc == nil {
		return
	}
	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		slog.Error("kill exclusive child process", "err", err)
	}
}

func writeCommandErr(errOut io.Writer, err error) {
	if err == nil || errOut == nil {
		return
	}
	if serr := say(errOut, "fugue: %v", err); serr != nil {
		slog.Error("write command error", "err", serr)
	}
}
