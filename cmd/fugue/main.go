package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/redact"
)

// version is the running host's version, injected at release time with
// -ldflags (cli §2.3); gochecknoglobals allows the name for this purpose.
var version = "dev"

func main() {
	os.Exit(runMain(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// runMain dispatches a subcommand. A usage error exits 2 and never touches
// the environment; serve loads the configuration from the process and exits
// through RunServe's code (cli §5.2). Usage goes to out, real errors to
// errOut.
func runMain(ctx context.Context, args []string, out, errOut io.Writer) int {
	if len(args) != 1 || args[0] != "serve" {
		if uerr := usage(out); uerr != nil {
			slog.Error("write usage", "err", uerr)
		}
		return exitUsage
	}
	cfg, err := config.FromProcess()
	if err != nil {
		if serr := say(errOut, "fugue: %v", err); serr != nil {
			slog.Error("write the startup error", "err", serr)
		}
		return exitFailure
	}
	slog.SetDefault(slog.New(redact.NewJSONHandler(os.Stderr, cfg.LogLevel)))
	return RunServe(ctx, out, cfg, version)
}

// usage prints the one-line command surface of this binary.
func usage(out io.Writer) error {
	if err := say(out, "usage: fugue serve"); err != nil {
		return err
	}
	return say(out, "  runs the Fugue host in the foreground; configuration comes from the environment")
}

// say writes one formatted line of user output.
func say(w io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(w, format+"\n", args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
