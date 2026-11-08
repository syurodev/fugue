package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// ErrUsage reports a malformed command line.
var ErrUsage = errors.New("usage: fuguegate coverage|fuzz|mutation|deadcode|proto|commitlint|commitmsg|gateguard|changes|exclusive [flags]")

func main() {
	ctx, stop := signalContext()
	code := run(ctx, clock.Real{}, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

const (
	exitFailed      = 1
	exitOutputError = 2
)

func run(ctx context.Context, clk clock.Clock, args []string, stdout, stderr io.Writer) int {
	err := dispatch(ctx, clk, args, stdout)
	var exit *ExitError
	if errors.As(err, &exit) {
		// A command that ran through exclusive already reported itself; only
		// its exit code crosses this boundary.
		return exit.Code
	}
	if err != nil {
		if _, werr := fmt.Fprintln(stderr, "fuguegate:", err); werr != nil {
			return exitOutputError
		}
		return exitFailed
	}
	return 0
}

// signalContext is the process context of fuguegate and of the re-exec helper
// entry in tests, which cannot reach main; the context constructors stay in
// this file.
func signalContext() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// say writes one formatted line of progress output.
func say(w io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(w, format+"\n", args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func dispatch(ctx context.Context, clk clock.Clock, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return ErrUsage
	}
	rest := args[1:]
	switch args[0] {
	case "coverage":
		return runCoverage(rest, stdout)
	case exclusiveCommand:
		return runExclusive(ctx, rest, stdout, clk)
	}
	if cmd, ok := commands()[args[0]]; ok {
		return cmd(ctx, rest, stdout)
	}
	return fmt.Errorf("%w: unknown command %q", ErrUsage, args[0])
}

// commands are the subcommands that take a context, their arguments and the
// output writer.
func commands() map[string]func(context.Context, []string, io.Writer) error {
	return map[string]func(context.Context, []string, io.Writer) error{
		"fuzz":       runFuzz,
		"mutation":   runMutation,
		"deadcode":   runDeadcode,
		"proto":      runProto,
		"commitlint": runCommitLint,
		"commitmsg":  runCommitMsg,
		"gateguard":  runGateGuard,
		"changes":    runChanges,
	}
}

// newFlags returns a flag set that reports problems through the returned
// error instead of printing usage.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
