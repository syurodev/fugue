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
	"fmt"
	"os"

	"gioui.org/app"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/appwin"
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
	// The same rule the host applies (internal/config): the
	// SYNX_FUGUE_DATA_DIR override when absolute, else the user config dir;
	// the lookup and the resolver are wired here, not read implicitly.
	dataDir, err := client.DataDir(os.LookupEnv, os.UserConfigDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fugue-app:", err)
		return exitFailure
	}
	err = appwin.Run(ctx, appwin.Options{
		DataDir: dataDir,
		Params: client.Params{
			Kind:        client.KindApp,
			Name:        "fugue-app",
			Version:     version,
			APIVersions: []string{"v1"},
		},
		Topics: []string{"host"},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "fugue-app:", err)
		return exitFailure
	}
	return exitOK
}
