package process

import (
	"errors"
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
			Argv: []string{exe, fakeVendorFlag, fakeVendorModeNormal},
			Cwd:  "/nonexistent/directory/cannot/chdir",
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
		Argv: []string{exe, fakeVendorFlag, "exit-on-eof"},
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
		Argv: []string{exe, fakeVendorFlag, "flood-stderr"},
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
