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
var ErrUsage = errors.New("usage: fuguegate coverage|fuzz|mutation|deadcode|proto|commitlint|commitmsg|gateguard|exclusive [flags]")

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
	case "fuzz":
		return runFuzz(ctx, rest, stdout)
	case "mutation":
		return runMutation(ctx, rest, stdout)
	case "deadcode":
		return runDeadcode(ctx, rest, stdout)
	case "proto":
		return runProto(ctx, rest, stdout)
	case "commitlint":
		return runCommitLint(ctx, rest, stdout)
	case "commitmsg":
		return runCommitMsg(ctx, rest, stdout)
	case "gateguard":
		return runGateGuard(ctx, rest, stdout)
	case exclusiveCommand:
		return runExclusive(ctx, rest, stdout, clk)
	default:
		return fmt.Errorf("%w: unknown command %q", ErrUsage, args[0])
	}
}

// newFlags returns a flag set that reports problems through the returned
// error instead of printing usage.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}
