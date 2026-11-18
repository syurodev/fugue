package vendor_test

// The shared login runner (F9.10b, VR-03) over fake CLIs written with
// testutil.WriteExecutable. Nothing here completes or awaits a real vendor
// login (the Q-112 measurement rule).

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
)

func writeLoginCLI(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-cli")
	testutil.WriteExecutable(t, path, []byte("#!/bin/sh\n"+body))
	return path
}

func drainLoginLines(t *testing.T, run vendor.LoginRun) string {
	t.Helper()
	var out strings.Builder
	for line := range run.Lines() {
		out.WriteString(line)
		out.WriteByte('\n')
	}
	<-run.Done()
	return out.String()
}

func startFakeLogin(t *testing.T, path string) *vendor.LoginProcess {
	t.Helper()
	run, err := vendor.StartLoginProcess(t.Context(), &vendor.LoginOptions{
		Name: "fake", Path: path,
		Env:   []string{"HOME=/home/REDACTED", "TERM=dumb"},
		Clock: clock.Real{},
		Probe: process.NewSystemdProbe(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestStartLoginProcessRelaysLinesAndExits(t *testing.T) {
	t.Parallel()
	// The CLI lingers briefly: the supervisor needs the process alive to read
	// its start token, so a CLI that exits at once is not a valid spawn.
	path := writeLoginCLI(t, "/bin/sleep 0.5\nprintf '%s\\n' 'line one'\nprintf '%s\\n' 'line two'\nexit 3\n")
	run := startFakeLogin(t, path)
	out := drainLoginLines(t, run)
	if out != "line one\nline two\n" {
		t.Fatalf("relayed lines = %q, want the verbatim two lines", out)
	}
	end := run.End()
	if !end.Ran || end.ExitCode != 3 {
		t.Fatalf("login end = %+v, want ran with exit 3", end)
	}
}

func TestStartLoginProcessSpawnFailuresAreTyped(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "not-there")
	if _, err := vendor.StartLoginProcess(t.Context(), &vendor.LoginOptions{Name: "fake", Path: missing}); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("missing binary error = %v, want not found", err)
	}
	if _, err := vendor.StartLoginProcess(t.Context(), &vendor.LoginOptions{Path: missing}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("empty name error = %v, want invalid argument", err)
	}
	if _, err := vendor.StartLoginProcess(t.Context(), &vendor.LoginOptions{Name: "fake"}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("empty path error = %v, want invalid argument", err)
	}
}

func TestStartLoginProcessStopEndsTheProcessGroup(t *testing.T) {
	t.Parallel()
	path := writeLoginCLI(t, "printf '%s\\n' 'waiting'\nexec /bin/sleep 300\n")
	run := startFakeLogin(t, path)
	if line := <-run.Lines(); line != "waiting" {
		t.Fatalf("first line = %q, want the prompt", line)
	}
	// The §8 stop order signals the process group: the CLI ends without a
	// readable exit code, whatever it was doing.
	if _, err := run.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	drainLoginLines(t, run)
	if end := run.End(); end.Ran {
		t.Fatalf("login end = %+v, want a signal-ended process without an exit code", end)
	}
}

func TestStartLoginProcessStopsACLIThatIgnoresSIGINT(t *testing.T) {
	t.Parallel()
	// A CLI that traps SIGINT must still end: the stop order escalates to
	// SIGTERM and SIGKILL, and Stop returns once the group is empty.
	path := writeLoginCLI(t, "printf '%s\\n' 'ready'\ntrap '' INT\nexec /bin/sleep 300\n")
	run := startFakeLogin(t, path)
	if line := <-run.Lines(); line != "ready" {
		t.Fatalf("first line = %q, want ready", line)
	}
	stopped := make(chan struct{})
	task := supervise.Go(t.Context(), "test.login-stop", func(context.Context) error {
		_, err := run.Stop(t.Context())
		close(stopped)
		return err
	})
	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("Stop did not return within the stop-order budget")
	}
	drainLoginLines(t, run)
	if end := run.End(); end.Ran {
		t.Fatalf("login end = %+v, want a signal-ended process without an exit code", end)
	}
	if err := task.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestStartLoginProcessFrameTooLargeFailsTheRun(t *testing.T) {
	t.Parallel()
	// One 9 MiB line: past the 8 MiB frame cap the stream is unreadable and
	// the host must not treat whatever follows as relayed output.
	path := writeLoginCLI(t, "dd if=/dev/zero bs=1000000 count=9 2>/dev/null | tr '\\0' 'a'\nexit 0\n")
	run := startFakeLogin(t, path)
	drainLoginLines(t, run)
	if end := run.End(); end.Ran {
		t.Fatalf("login end = %+v, want an unreadable run without an exit code", end)
	}
}

func TestStartLoginProcessEnvIsPassedUnchanged(t *testing.T) {
	t.Parallel()
	path := writeLoginCLI(t, "/bin/sleep 0.5\nexec /usr/bin/env\n")
	run := startFakeLogin(t, path)
	env := drainLoginLines(t, run)
	for _, want := range []string{"HOME=/home/REDACTED\n", "TERM=dumb\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("login environment lost %s: %q", want, env)
		}
	}
	if strings.Contains(env, "FUGUE") {
		t.Errorf("login environment carries a Fugue variable: %q", env)
	}
}
