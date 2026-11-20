package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/hostapi"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
)

type fixedExclusiveResources struct {
	ramBytes uint64
	cores    int
}

func (f fixedExclusiveResources) TotalMemoryBytes() (uint64, error) { return f.ramBytes, nil }
func (f fixedExclusiveResources) CPUCores() int                     { return f.cores }

func TestParseExclusiveArgs(t *testing.T) {
	t.Parallel()
	validCases := []struct {
		args     []string
		wantSock string
		wantCmd  []string
	}{
		{[]string{exclusiveCommand, exclusiveSocketFlag, "/tmp/a.sock", "--", "echo", "ok"}, "/tmp/a.sock", []string{"echo", "ok"}},
		{[]string{exclusiveCommand, "--", "pwd"}, "", []string{"pwd"}},
	}
	for _, tc := range validCases {
		sock, cmd, matched, valid := parseExclusiveArgs(tc.args)
		if !matched || !valid || sock != tc.wantSock || !slices.Equal(cmd, tc.wantCmd) {
			t.Fatalf("parseExclusiveArgs(%v) = (%q, %v, %v, %v)", tc.args, sock, cmd, matched, valid)
		}
	}
	for _, bad := range [][]string{{exclusiveCommand}, {exclusiveCommand, "--"}, {exclusiveCommand, exclusiveSocketFlag}, {exclusiveCommand, "date"}} {
		_, _, m, v := parseExclusiveArgs(bad)
		if !m || v {
			t.Fatalf("parseExclusiveArgs(%v) = (%v, %v), want matched=true valid=false", bad, m, v)
		}
	}
	if code := runMain(t.Context(), []string{exclusiveCommand}, &bytes.Buffer{}, &bytes.Buffer{}); code != exitUsage {
		t.Fatalf("runMain invalid exclusive exit = %d, want %d", code, exitUsage)
	}
}

func startExclusiveTestHost(t *testing.T, cores int) (string, *app.HostProcess) {
	t.Helper()
	dir := serveDir(t)
	cheapvault.SeedDataDir(t, dir)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := testutil.OSClock()
	sock, err := hostapi.ListenSocket(t.Context(), hostapi.SocketOptions{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := hostapi.NewServer(hostapi.ServerConfig{
		Socket: sock, DataDir: dir, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("excl-conn")), HostUID: int64(os.Getuid()),
	})
	deps := &app.HostDeps{
		DataDir:       dir,
		WorkspaceEnv:  config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config:        app.HostConfig{HostVersion: testVersion, APIVersions: []string{"v1"}},
		Clock:         clk,
		IDs:           ids.NewUUIDv7(clk, ids.NewDeterministic("excl-host")),
		Random:        ids.NewDeterministic("excl-tokens"),
		HostResources: fixedExclusiveResources{ramBytes: 16 << 30, cores: cores},
		Transport:     srv,
		VendorBinary:  exe,
	}
	hp, err := app.StartHost(t.Context(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(t.Context(), hp.Runtime()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Drain(context.WithoutCancel(t.Context()), true); err != nil {
			t.Errorf("drain: %v", err)
		}
		if err := hp.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	cheapvault.Unlock(t, hp.Vault())
	return sock.Path, hp
}

func TestRunExclusiveSharesSemaphoreAndForwardsExitCode(t *testing.T) {
	t.Parallel()
	sockPath, _ := startExclusiveTestHost(t, 4) // 4 cores -> heavy cap 1
	var out, errOut bytes.Buffer
	code := RunExclusive(t.Context(), bytes.NewBuffer(nil), &out, &errOut, config.MapEnv{}, sockPath, []string{"sh", "-c", "printf hello-excl; exit 7"}, testVersion)
	if code != 7 {
		t.Fatalf("RunExclusive exit code = %d (stderr=%q), want 7", code, errOut.String())
	}
	if out.String() != "hello-excl" {
		t.Fatalf("stdout = %q, want hello-excl", out.String())
	}
	var wg sync.WaitGroup
	var active atomic.Int32
	for range 2 {
		wg.Add(1)
		supervise.Go(t.Context(), "test.exclusive-waiter", func(ctx context.Context) error {
			defer wg.Done()
			slot, slotErr := acquireExclusiveSlot(ctx, config.MapEnv{}, sockPath, testVersion)
			if slotErr != nil {
				t.Errorf("acquireExclusiveSlot: %v", slotErr)
				return slotErr
			}
			if cur := active.Add(1); cur > 1 {
				t.Errorf("concurrent overlap = %d, want <= 1", cur)
			}
			runtime.Gosched()
			active.Add(-1)
			slot.release()
			return nil
		})
	}
	wg.Wait()
}

func TestRunExclusiveSessionPrincipalUsesSessionService(t *testing.T) {
	t.Parallel()
	sockPath := serveStub(t, &stubHost{})
	var out, errOut bytes.Buffer
	code := RunExclusive(t.Context(), bytes.NewBuffer(nil), &out, &errOut, config.MapEnv{app.SessionTokenEnv: bridgeTestToken}, sockPath, []string{"sh", "-c", "printf session-ok"}, testVersion)
	if code != exitOK || out.String() != "session-ok" {
		t.Fatalf("session RunExclusive = (%d, %q, stderr=%q)", code, out.String(), errOut.String())
	}
}
