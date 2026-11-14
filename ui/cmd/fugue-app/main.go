// Command fugue-app is the Fugue desktop app (desktop-app DA-01): a Gio
// binary in its own module that talks to the host only through the Host API.
// The skeleton opens one window, shows the connection state, and retries
// while no host answers; the host auto-start is F10.2b.
//
// Owner: desktop app (ui module). Allowed callers: none, it is an entry
// point.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"gioui.org/app"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/appwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
)

// version is the app's own version, injected at release time with -ldflags;
// gochecknoglobals allows the name for this purpose.
var version = "dev"

// exit codes of the app process.
const (
	exitOK      = 0
	exitFailure = 1
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--background-off" {
		dir, err := client.DataDir(os.LookupEnv, os.UserConfigDir)
		if err == nil {
			err = client.SetBackgroundEnabled(dir, false)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "fugue-app:", err)
			os.Exit(exitFailure)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--host-log-sink" {
		if err := runHostLogSink(); err != nil {
			os.Exit(exitFailure)
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// app.Main must own the main thread; the window runs beside it and
		// ends the process with its own outcome.
		os.Exit(run(ctx))
	}()
	app.Main()
}

// run resolves the data directory, opens the window, and returns the exit
// code.
func run(ctx context.Context) int {
	if err := runApp(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "fugue-app:", err)
		return exitFailure
	}
	return exitOK
}

func runApp(ctx context.Context) error {
	dataDir, err := client.DataDir(os.LookupEnv, os.UserConfigDir)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	if len(os.Args) == 2 && os.Args[1] == "--background-on" {
		if err = client.SetBackgroundEnabled(dataDir, true); err != nil {
			return fmt.Errorf("enable background mode: %w", err)
		}
	}
	params := client.Params{Kind: client.KindApp, Name: "fugue-app", Version: version, APIVersions: []string{"v1"}}
	return runWindow(ctx, dataDir, params)
}

func runWindow(ctx context.Context, dir string, params client.Params) error {
	bg, err := hostconn.StartBackground(ctx, dir, params, "")
	if err != nil && !backgroundUnavailable(err) {
		return fmt.Errorf("start background mode: %w", err)
	}
	if err = appwin.Run(ctx, appwin.Options{DataDir: dir, Params: params, Topics: []string{"host", "vendor-sessions"}}); err != nil {
		if bg != nil {
			bg.Close()
		}
		return fmt.Errorf("run window: %w", err)
	}
	return waitBackground(ctx, dir, params, bg)
}

func waitBackground(ctx context.Context, dir string, params client.Params, bg *hostconn.Background) error {
	if bg != nil {
		select {
		case <-bg.Done():
			bg.Close()
			bg = nil
		default:
		}
	}
	if bg == nil {
		var err error
		bg, err = hostconn.StartBackground(ctx, dir, params, "")
		if err != nil && !errors.Is(err, context.Canceled) && !backgroundUnavailable(err) {
			return fmt.Errorf("start background mode: %w", err)
		}
	}
	if bg != nil {
		<-bg.Done()
		bg.Close()
	}
	return nil
}

func backgroundUnavailable(err error) bool {
	return errors.Is(err, client.ErrBackgroundDisabled) || errors.Is(err, client.ErrBackgroundOwned)
}

// runHostLogSink drains the inherited host output into its inherited log file.
func runHostLogSink() error {
	f := os.NewFile(3, "host log")
	if f == nil {
		return client.ErrUntrustedHost
	}
	defer func() {
		if err := f.Close(); err != nil {
			return
		}
	}()
	if err := client.WriteBoundedLog(f, os.Stdin, 1<<20); err != nil {
		return fmt.Errorf("drain host output: %w", err)
	}
	return nil
}
