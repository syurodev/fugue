package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/pkg/client"
)

const testServe = "serve"

// testIdleGrace is the fake-clock idle grace the lifecycle tests serve with.
const testIdleGrace = 100 * time.Millisecond

// expireGrace waits for the host to arm its idle grace on the fake clock and
// then advances the clock past it.
func expireGrace(t *testing.T, ctx context.Context, clk *clock.Fake, result <-chan serveResult) {
	t.Helper()
	if err := clk.BlockUntilPending(ctx, 1); err != nil {
		select {
		case res := <-result:
			t.Fatalf("serve ended before it armed the idle grace: exit %d, error: %v", res.code, res.err)
		default:
			t.Fatalf("the idle grace was never armed: %v", err)
		}
	}
	clk.Advance(testIdleGrace)
}

func TestServeArgumentsSelectOnlyAppIdleGrace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		args  []string
		grace time.Duration
		valid bool
	}{
		{[]string{testServe}, 0, true},
		{[]string{testServe, "--app-idle-grace"}, 10 * time.Second, true},
		{[]string{testServe, "--app-idle-grace", "extra"}, 0, false},
		{[]string{testServe, "--unknown"}, 0, false},
	}
	for _, tc := range cases {
		got, ok := serveArguments(tc.args)
		if got != tc.grace || ok != tc.valid {
			t.Fatalf("args %v: %v %v", tc.args, got, ok)
		}
	}
}

func TestServeExitsAfterLastLeaseIdleGrace(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cfg := config.Config{DataDir: serveDir(t)}
	ready := make(chan struct{})
	clk := clock.NewFake(time.Unix(0, 0))
	result := startServe(t, ctx, cfg, serveOpts{ready: ready, sigs: make(chan os.Signal), idleGrace: testIdleGrace, clock: clk})
	awaitReady(t, ctx, ready, result)
	c, err := client.Dial(ctx, socketPath(cfg.DataDir), client.Params{Kind: client.KindApp, APIVersions: []string{"v1"}})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	// The last lease's release arms the grace; the fake clock fires it only
	// once the test says so, however slow the runner is.
	expireGrace(t, ctx, clk, result)
	select {
	case got := <-result:
		if got.code != exitOK || got.err != nil {
			t.Fatalf("serve exit: %+v", got)
		}
	case <-ctx.Done():
		<-result
		t.Fatal("host did not exit after grace")
	}
}

func TestServeWithoutInitialLeaseExitsAfterGrace(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cfg := config.Config{DataDir: serveDir(t)}
	ready := make(chan struct{})
	clk := clock.NewFake(time.Unix(0, 0))
	result := startServe(t, ctx, cfg, serveOpts{ready: ready, sigs: make(chan os.Signal), idleGrace: testIdleGrace, clock: clk})
	awaitReady(t, ctx, ready, result)
	expireGrace(t, ctx, clk, result)
	select {
	case got := <-result:
		if got.code != exitOK || got.err != nil {
			t.Fatalf("serve exit: %+v", got)
		}
	case <-ctx.Done():
		<-result
		t.Fatal("host with no clients did not exit")
	}
}

type failingDrainTransport struct{}

func (failingDrainTransport) Drain(context.Context, bool) error {
	return errcode.New(errcode.Internal, "drain failed")
}

func TestRunUntilStoppedCloseErrorExitsFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"drain", "signal", "context"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			clk := clock.NewFake(time.Unix(0, 0))
			hp, err := app.StartHost(t.Context(), &app.HostDeps{DataDir: t.TempDir(), Config: app.HostConfig{HostVersion: "test", APIVersions: []string{"v1"}}, Clock: clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("test")), Transport: failingDrainTransport{}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sigs := make(chan os.Signal, 1)
			switch mode {
			case "drain":
				hp.Stop("test")
				<-hp.DrainDone()
			case "signal":
				sigs <- syscall.SIGTERM
			case "context":
				cancel()
			}
			if code := runUntilStopped(ctx, hp, sigs); code != exitFailure {
				t.Fatalf("exit = %d, want %d", code, exitFailure)
			}
		})
	}
}
