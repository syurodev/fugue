//go:build linux || darwin

package hostconn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/syurodev/fugue/pkg/client"
)

type launchedChildren struct{ host, log <-chan error }

func launchDetached(ctx context.Context, appPath, dataDir string, env []string) (launchedChildren, error) {
	appPath, err := filepath.EvalSymlinks(appPath)
	if err != nil {
		return launchedChildren{}, fmt.Errorf("resolve app executable: %w", err)
	}
	hostPath := filepath.Join(filepath.Dir(appPath), "fugue")
	if err = validateExecutable(hostPath); err != nil {
		return launchedChildren{}, err
	}
	logFile, err := client.OpenHostLog(dataDir)
	if err != nil {
		return launchedChildren{}, fmt.Errorf("prepare host log: %w", err)
	}
	defer closeHandle(logFile)
	r, w, err := os.Pipe()
	if err != nil {
		return launchedChildren{}, fmt.Errorf("create host log pipe: %w", err)
	}
	defer closeHandle(r)
	defer closeHandle(w)
	if err = ctx.Err(); err != nil {
		return launchedChildren{}, fmt.Errorf("start host: %w", err)
	}
	sink, err := os.StartProcess(appPath, []string{appPath, "--host-log-sink"}, &os.ProcAttr{Env: []string{}, Files: []*os.File{r, nil, nil, logFile}, Sys: &syscall.SysProcAttr{Setsid: true}})
	if err != nil {
		return launchedChildren{}, fmt.Errorf("start host log sink: %w", err)
	}
	logDone := reap(sink)
	if err = ctx.Err(); err != nil {
		return launchedChildren{log: logDone}, fmt.Errorf("start host: %w", err)
	}
	host, err := os.StartProcess(hostPath, []string{hostPath, "serve", "--app-idle-grace"}, &os.ProcAttr{Env: hostEnvironment(env, dataDir), Files: []*os.File{nil, w, w}, Sys: &syscall.SysProcAttr{Setsid: true}})
	if err != nil {
		return launchedChildren{log: logDone}, fmt.Errorf("start host: %w", err)
	}
	return launchedChildren{host: reap(host), log: logDone}, nil
}

var errChildExited = errors.New("host child exited unsuccessfully")

func reap(process *os.Process) <-chan error {
	done := make(chan error, 1)
	go func() {
		state, err := process.Wait()
		if err == nil && !state.Success() {
			err = errChildExited
		}
		done <- err
		close(done)
	}()
	return done
}
