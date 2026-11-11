// Command mdpreview is a development viewer for the markdown renderer of
// F10.3b: it renders the markdown it reads on standard input in a scrollable
// window with a light/dark toggle, so the renderer can be judged by eye. It
// ships only for development: it is not packaged and fugue-app never imports
// it. Run it from the ui directory:
//
//	go run ./cmd/mdpreview < internal/markdown/testdata/preview.md
//
// The popover's open button hands the URL to the system opener, only for
// http, https and mailto, as a single argument.
//
// Owner: F10.3b. Allowed callers: none, it is an entry point.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gioui.org/app"

	"github.com/syurodev/fugue/ui/internal/markdown"
)

// maxInput bounds what is read from standard input; Parse caps the source
// again.
const maxInput = 2 << 20

// errNoInput reports a terminal on standard input, where reading would wait
// for typing.
var errNoInput = errors.New("no markdown on standard input: pass a file, e.g. < internal/markdown/testdata/preview.md")

// wallClock is the preview's time source for the highlight budget; the
// renderer itself reads no clock.
type wallClock struct{}

// Now implements markdown.Clock.
func (wallClock) Now() time.Time {
	return time.Now() //nolint:forbidigo // the dev-only preview owns the wall clock the highlight budget needs
}

func main() {
	src, err := readInput(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mdpreview:", err)
		os.Exit(1)
	}
	doc := markdown.Parse(src, markdown.WithClock(wallClock{}))
	ctx, cancel := context.WithCancel(context.Background()) //nolint:forbidigo // main roots the context of the opener processes
	defer cancel()
	go func() {
		// app.Main owns the main thread; the window runs beside it and ends
		// the process with its own outcome.
		code := 0
		if err := run(ctx, doc); err != nil {
			fmt.Fprintln(os.Stderr, "mdpreview:", err)
			code = 1
		}
		os.Exit(code)
	}()
	app.Main()
}

// readInput reads the markdown from f, refusing an interactive terminal.
func readInput(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat standard input: %w", err)
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", errNoInput
	}
	b, err := io.ReadAll(io.LimitReader(f, maxInput))
	if err != nil {
		return "", fmt.Errorf("read standard input: %w", err)
	}
	return string(b), nil
}
