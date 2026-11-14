package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

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
	if socket, ok := bridgeArguments(args); ok {
		// The bridge's stdout is the MCP channel; its log goes to stderr,
		// which the vendor CLI owns.
		slog.SetDefault(slog.New(redact.NewJSONHandler(os.Stderr, slog.LevelWarn)))
		return RunBridge(ctx, os.Stdin, out, config.ProcessEnv{}, socket, version)
	}
	grace, valid := serveArguments(args)
	if !valid {
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
	return RunServe(ctx, out, cfg, version, grace)
}

func serveArguments(args []string) (time.Duration, bool) {
	if len(args) == 1 && args[0] == "serve" {
		return 0, true
	}
	if len(args) == 2 && args[0] == "serve" && args[1] == "--app-idle-grace" {
		return 10 * time.Second, true
	}
	return 0, false
}

// bridgeArguments recognizes `mcp-bridge --socket <path>`, the command line
// the host writes into a vendor session's MCP configuration.
func bridgeArguments(args []string) (string, bool) {
	if len(args) == 3 && args[0] == bridgeCommand && args[1] == bridgeSocketFlag && args[2] != "" {
		return args[2], true
	}
	return "", false
}

// usage prints the command surface of this binary.
func usage(out io.Writer) error {
	if err := say(out, "usage: fugue serve"); err != nil {
		return err
	}
	if err := say(out, "  runs the Fugue host in the foreground; configuration comes from the environment"); err != nil {
		return err
	}
	if err := say(out, "usage: fugue mcp-bridge --socket <path>"); err != nil {
		return err
	}
	return say(out, "  serves Fugue's MCP tools to a vendor session; started by the host, not by hand")
}

// say writes one formatted line of user output.
func say(w io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(w, format+"\n", args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}
