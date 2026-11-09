package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/pkg/client"
)

const testServe = "serve"

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
	result := startServe(t, ctx, cfg, serveOpts{ready: ready, sigs: make(chan os.Signal), idleGrace: 100 * time.Millisecond})
	<-ready
	c, err := client.Dial(ctx, socketPath(cfg.DataDir), client.Params{Kind: client.KindApp, APIVersions: []string{"v1"}})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
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
	result := startServe(t, ctx, cfg, serveOpts{ready: ready, sigs: make(chan os.Signal), idleGrace: 100 * time.Millisecond})
	<-ready
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
