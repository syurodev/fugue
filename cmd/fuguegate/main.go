package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
)

// ErrUsage reports a malformed command line.
var ErrUsage = errors.New("usage: fuguegate coverage|fuzz|mutation|deadcode|proto|commitlint|commitmsg|gateguard [flags]")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

const (
	exitFailed      = 1
	exitOutputError = 2
)

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if err := dispatch(ctx, args, stdout); err != nil {
		if _, werr := fmt.Fprintln(stderr, "fuguegate:", err); werr != nil {
			return exitOutputError
		}
		return exitFailed
	}
	return 0
}

// say writes one formatted line of progress output.
func say(w io.Writer, format string, args ...any) error {
	if _, err := fmt.Fprintf(w, format+"\n", args...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func dispatch(ctx context.Context, args []string, stdout io.Writer) error {
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
