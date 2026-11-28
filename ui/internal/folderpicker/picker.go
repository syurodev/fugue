package folderpicker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// errNoPicker is the static cause behind a pick no helper could run; it is
// wrapped, never constructed, at the call site.
var errNoPicker = errors.New("no folder picker is available")

// Helper is one way a pick can run.
type Helper uint8

// The helpers, in no order; the platform's order lives in pickOrder.
const (
	HelperNone Helper = iota
	HelperPortal
	HelperOSAScript
	HelperZenity
	HelperKDialog
)

// Availability is what the current desktop answered the probe with.
type Availability struct {
	Portal    bool
	OSAScript bool
	Zenity    bool
	KDialog   bool
}

// pickOrder is the platform's fallback chain: macOS osascript; Linux the
// portal FileChooser, then zenity, then kdialog; nowhere else. Only the
// helpers the probe found ride the chain.
func pickOrder(av Availability, goos string) []Helper {
	var order []Helper
	switch goos {
	case "darwin":
		if av.OSAScript {
			order = append(order, HelperOSAScript)
		}
	case "linux":
		if av.Portal {
			order = append(order, HelperPortal)
		}
		if av.Zenity {
			order = append(order, HelperZenity)
		}
		if av.KDialog {
			order = append(order, HelperKDialog)
		}
	}
	return order
}

// selectHelper is the first helper a pick runs.
func selectHelper(av Availability, goos string) Helper {
	if order := pickOrder(av, goos); len(order) > 0 {
		return order[0]
	}
	return HelperNone
}

// commandOf builds one CLI helper's invocation: a fixed name and argv, the
// start directory as an argument, never a shell. ok is false for the helpers
// that do not run a process (the portal rides D-Bus; none runs nothing).
func commandOf(h Helper, startDir string) (name string, args []string, ok bool) {
	switch h {
	case HelperOSAScript:
		return "osascript", []string{"-e", `POSIX path of (choose folder with prompt "Choose a folder")`}, true
	case HelperZenity:
		return "zenity", []string{"--file-selection", "--directory"}, true
	case HelperKDialog:
		return "kdialog", []string{"--getexistingdirectory", startDir}, true
	default:
		return "", nil, false
	}
}

// exitCoder is the error a Runner answers when the helper process ran and
// exited nonzero; exitCodeOf reads the code. Exit 1 is how the CLI helpers
// answer a closed chooser, so a pick maps it to a cancel.
type exitCoder interface {
	error
	ExitCode() int
}

// exitCodeOf returns the helper's exit code, when its error carries one.
func exitCodeOf(err error) (int, bool) {
	var ec exitCoder
	if !errors.As(err, &ec) {
		return 0, false
	}
	return ec.ExitCode(), true
}

// exitError wraps one nonzero helper exit.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return fmt.Sprintf("%s (exit %d)", e.err, e.code) }
func (e *exitError) ExitCode() int { return e.code }
func (e *exitError) Unwrap() error { return e.err }

// Runner runs one helper process and answers its stdout; a nonzero exit is
// an error. The interface exists for the tests' recording fake; production
// runs exec directly.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, err error)
}

// execRunner runs helpers through exec, argv only.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	var eerr *exec.ExitError
	if errors.As(err, &eerr) {
		return "", &exitError{code: eerr.ExitCode(), err: fmt.Errorf("%s failed", name)}
	}
	return "", fmt.Errorf("run %s: %w", name, err)
}

// pickPortalFunc runs the portal FileChooser flow: the chosen folder's path,
// whether the user closed it, or why it could not run.
type pickPortalFunc func(ctx context.Context) (path string, cancelled bool, err error)

// Result is one finished pick, delivered to the window goroutine.
type Result struct {
	// Path is the chosen folder's absolute path, empty unless one was chosen.
	Path string
	// Cancelled marks a chooser the user closed without choosing: a no-op.
	Cancelled bool
	// Err is why no chooser ran or answered.
	Err error
}

// pickBufferSize bounds the request and result queues. A pick is one user
// action at a time; a second while one runs is refused at once.
const pickBufferSize = 1

// probeTimeout bounds the one availability probe: a session bus that accepts
// the connection but never answers the introspect cannot stall a dialog open
// for longer.
const probeTimeout = 2 * time.Second

// Picker runs folder picks off the UI goroutine, in the shape of the
// workspace mutator: one worker goroutine takes requests, runs the
// platform's helpers in order, and delivers each outcome for the frame loop.
// Start probes once, so the dialogs read the availability without ever
// probing on the window goroutine.
type Picker struct {
	probeOf func(ctx context.Context) Availability
	runner  Runner
	portal  pickPortalFunc
	goos    string

	reqs     chan struct{}
	results  chan Result
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once

	// probeOnce memoizes the availability: the first dialog open pays the
	// probe (a PATH lookup, and one D-Bus introspect on Linux), and every
	// later read and pick reuses the answer. A pick itself always runs on
	// the worker goroutine.
	probeOnce sync.Once
	avail     Availability
}

// NewPicker builds a picker over the platform's probe and helpers.
func NewPicker() *Picker {
	return &Picker{
		probeOf: platformProbe,
		runner:  execRunner{},
		portal:  pickPortal,
		goos:    runtime.GOOS,
		reqs:    make(chan struct{}, pickBufferSize),
		results: make(chan Result, pickBufferSize),
		done:    make(chan struct{}),
	}
}

// Start runs the worker. It ends with Stop or the context.
func (p *Picker) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	go p.run(runCtx)
}

// Stop ends the worker and waits for it. It is idempotent.
func (p *Picker) Stop() {
	p.stopOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
	})
	<-p.done
}

// Pick requests one folder pick and reports whether it was accepted; a pick
// while one runs is refused at once instead of queueing a second chooser.
// It never blocks: the window goroutine calls it mid-frame, and the outcome
// comes back on Results.
func (p *Picker) Pick() bool {
	select {
	case p.reqs <- struct{}{}:
		return true
	default:
		return false
	}
}

// Results receives the finished picks. The window goroutine drains it every
// frame.
func (p *Picker) Results() <-chan Result { return p.results }

// Available is the desktop's probed answer, memoized on first read.
func (p *Picker) Available() Availability {
	p.probeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		defer cancel()
		p.avail = p.probeOf(ctx)
	})
	return p.avail
}

// First is the helper a pick would run, or HelperNone.
func (p *Picker) First() Helper { return selectHelper(p.Available(), p.goos) }

// run serves requests until the context ends. The availability is already
// memoized by then: a pick only follows a dialog open, which read First.
func (p *Picker) run(ctx context.Context) {
	defer close(p.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.reqs:
			res := p.pickAll(ctx)
			if ctx.Err() != nil {
				return
			}
			select {
			case p.results <- res:
			case <-ctx.Done():
				return
			}
		}
	}
}

// pickAll walks the platform's helpers in order and answers with the first
// outcome that is a choice, a cancel or a refusal. A helper that could not
// run at all falls through to the next one; once a chooser appeared, its own
// answer — including a cancel — is final, because opening a second chooser
// after a cancel would surprise the user.
func (p *Picker) pickAll(ctx context.Context) Result {
	order := pickOrder(p.Available(), p.goos)
	if len(order) == 0 {
		return Result{Err: errNoPicker}
	}
	var lastErr error
	for _, h := range order {
		res, ran := p.pickOne(ctx, h)
		if ran {
			return res
		}
		lastErr = res.Err
	}
	if lastErr == nil {
		lastErr = errNoPicker
	}
	return Result{Err: fmt.Errorf("the folder picker failed: %w", lastErr)}
}

// pickOne runs one helper. ran marks that a chooser actually appeared, so
// the answer is final either way.
func (p *Picker) pickOne(ctx context.Context, h Helper) (res Result, ran bool) {
	if ctx.Err() != nil {
		return Result{Err: ctx.Err()}, false
	}
	if h == HelperPortal {
		path, cancelled, err := p.portal(ctx)
		if err != nil {
			return Result{Err: err}, false
		}
		return Result{Path: path, Cancelled: cancelled}, true
	}
	startDir, err := userStartDir()
	if err != nil {
		return Result{Err: err}, false
	}
	name, args, ok := commandOf(h, startDir)
	if !ok {
		return Result{Err: errNoPicker}, false
	}
	out, err := p.runner.Run(ctx, name, args...)
	if err != nil {
		if code, isExit := exitCodeOf(err); isExit && code == 1 {
			return Result{Cancelled: true}, true
		}
		return Result{Err: fmt.Errorf("%s failed: %w", name, err)}, false
	}
	if out == "" {
		return Result{Cancelled: true}, true
	}
	return Result{Path: out}, true
}

// userStartDir is the directory kdialog's required start argument names.
func userStartDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find a start directory for the folder picker: %w", err)
	}
	return home, nil
}

// uriPath turns a portal's file URI into an absolute path.
func uriPath(uri string) (string, error) {
	u, err := url.ParseRequestURI(uri)
	if err != nil || u.Scheme != "file" {
		return "", fmt.Errorf("the folder picker answered %q, which is not a file path", uri)
	}
	return u.Path, nil
}
