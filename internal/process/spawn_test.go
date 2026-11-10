package process

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const fakeVendorModeNormal = "normal"

func TestProcessSpawnDistinguishesFailures(t *testing.T) {
	t.Parallel()

	ctx := t.Context()

	t.Run("binary not found", func(t *testing.T) {
		t.Parallel()
		_, err := Spawn(ctx, &Options{
			Argv: []string{"/nonexistent/binary/path/surely_does_not_exist"},
		})
		if !errors.Is(err, ErrBinaryNotFound) {
			t.Fatalf("expected ErrBinaryNotFound, got %v", err)
		}
	})

	t.Run("not executable", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		nonExecPath := filepath.Join(tmpDir, "non_exec.bin")
		if err := os.WriteFile(nonExecPath, []byte("#!/bin/sh\necho hi\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := Spawn(ctx, &Options{
			Argv: []string{nonExecPath},
		})
		if !errors.Is(err, ErrNotExecutable) {
			t.Fatalf("expected ErrNotExecutable, got %v", err)
		}
	})

	t.Run("start failed on invalid directory", func(t *testing.T) {
		t.Parallel()
		exe := fakeVendorExe(t)
		_, err := Spawn(ctx, &Options{
			Argv:           []string{exe, fakeVendorFlag, fakeVendorModeNormal},
			Cwd:            "/nonexistent/directory/cannot/chdir",
			SystemdRunPath: nonexistentSystemdRun,
			Probe:          NewSystemdProbe(),
		})
		if !errors.Is(err, ErrStartFailed) {
			t.Fatalf("expected ErrStartFailed, got %v", err)
		}
	})
}

func TestProcessExitsOnStdinEOF(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeVendorExe(t)

	proc, err := Spawn(ctx, &Options{
		Argv:           []string{exe, fakeVendorFlag, "exit-on-eof"},
		SystemdRunPath: nonexistentSystemdRun,
		Probe:          NewSystemdProbe(),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	waitReady(t, proc.Stdout())

	if err := proc.CloseStdin(); err != nil {
		t.Fatalf("CloseStdin: %v", err)
	}

	if err := proc.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestProcessStderrFloodCapturedInRing(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeVendorExe(t)

	proc, err := Spawn(ctx, &Options{
		Argv:           []string{exe, fakeVendorFlag, "flood-stderr"},
		SystemdRunPath: nonexistentSystemdRun,
		Probe:          NewSystemdProbe(),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	line, err := proc.Stdout().ReadLine()
	if err != nil || string(line) != "FLOOD_DONE" {
		t.Fatalf("expected FLOOD_DONE, got line=%q err=%v", string(line), err)
	}

	if proc.StderrRing().Len() != DefaultStderrRingCapacity {
		t.Fatalf("StderrRing().Len() = %d, want %d", proc.StderrRing().Len(), DefaultStderrRingCapacity)
	}

	if _, err := proc.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestFinalLinesSurviveImmediateExit(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	exe := fakeVendorExe(t)
	probe := NewSystemdProbe()

	for iter := range 20 {
		proc, err := Spawn(ctx, &Options{
			Argv:           []string{exe, fakeVendorFlag, "burst-and-exit"},
			SystemdRunPath: nonexistentSystemdRun,
			Probe:          probe,
		})
		if err != nil {
			t.Fatalf("iter %d: Spawn: %v", iter, err)
		}

		linesRead := drainBurstLines(t, proc.Stdout())
		if linesRead != 500 {
			t.Fatalf("iter %d: expected 500 lines, got %d", iter, linesRead)
		}

		if err := proc.Wait(); err != nil {
			t.Fatalf("iter %d: Wait: %v", iter, err)
		}
	}
}

func drainBurstLines(t *testing.T, lr *LineReader) int {
	t.Helper()
	linesRead := 0
	for {
		line, rerr := lr.ReadLine()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return linesRead
			}
			t.Fatalf("ReadLine[%d]: %v", linesRead, rerr)
		}
		linesRead++
		if linesRead == 500 && !bytes.HasPrefix(line, []byte("line 499 ")) {
			t.Fatalf("unexpected final line prefix: %s", line[:20])
		}
	}
}

func TestProcessWriteLine(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	proc, err := Spawn(ctx, &Options{
		Argv:           []string{fakeVendorExe(t), fakeVendorFlag, "echo-stdin"},
		SystemdRunPath: nonexistentSystemdRun,
		Probe:          NewSystemdProbe(),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	waitReady(t, proc.Stdout())

	testWriteLineEchoAndLimit(t, proc)
	testWriteLineClosedAndStopped(ctx, t, proc)
}

func testWriteLineEchoAndLimit(t *testing.T, proc *Process) {
	t.Helper()
	if err := proc.WriteLine([]byte(`{"hello":"world"}`)); err != nil {
		t.Fatalf("WriteLine: %v", err)
	}
	line, err := proc.Stdout().ReadLine()
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if string(line) != `{"hello":"world"}` {
		t.Fatalf("expected echo, got %q", string(line))
	}

	if werr := proc.WriteLine([]byte("second line\n")); werr != nil {
		t.Fatalf("WriteLine with newline: %v", werr)
	}
	line, err = proc.Stdout().ReadLine()
	if err != nil {
		t.Fatalf("ReadLine: %v", err)
	}
	if string(line) != "second line" {
		t.Fatalf("expected second line, got %q", string(line))
	}

	oversized := bytes.Repeat([]byte("a"), MaxFrameSize+1)
	if err := proc.WriteLine(oversized); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func testWriteLineClosedAndStopped(ctx context.Context, t *testing.T, proc *Process) {
	t.Helper()
	if err := proc.CloseStdin(); err != nil {
		t.Fatalf("CloseStdin: %v", err)
	}
	if err := proc.WriteLine([]byte("after close")); !errors.Is(err, ErrStdinClosed) {
		t.Fatalf("expected ErrStdinClosed after CloseStdin, got %v", err)
	}
	if err := proc.CloseStdin(); err != nil {
		t.Fatalf("CloseStdin idempotent: %v", err)
	}

	if _, err := proc.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := proc.WriteLine([]byte("after stop")); !errors.Is(err, ErrStdinClosed) {
		t.Fatalf("expected ErrStdinClosed after Stop, got %v", err)
	}
}
