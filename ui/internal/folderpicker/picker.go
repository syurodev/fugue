package folderpicker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// The static causes behind a refused pick; they are wrapped, never
// constructed, at the call site. The off-platform portal's cause lives in
// the platform files that use it.
var (
	errNoPicker      = errors.New("no folder picker is available")
	errNotAFilePath  = errors.New("the folder picker answered a path that is not a file")
	errForeignFolder = errors.New("the folder picker answered a folder on another host")
)

// Mode selects whether the native chooser picks a folder, opens a file, or saves a file.
type Mode uint8

const (
	ModeFolder Mode = iota
	ModeOpenFile
	ModeSaveFile
)

// Request configures one native chooser invocation.
type Request struct {
	Mode        Mode
	Tag         string
	FilterExt   string
	DefaultName string
}

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

// The static causes behind a helper run; wrapped, never constructed.
var (
	errHelperFailed = errors.New("the helper failed")
	errHelperRun    = errors.New("run the helper")
)

func (e *exitError) Error() string { return fmt.Sprintf("%s (exit %d)", e.err, e.code) }
func (e *exitError) ExitCode() int { return e.code }
func (e *exitError) Unwrap() error { return e.err }

// Runner runs one CLI helper and answers its stdout; a nonzero exit is an
// error. The interface exists for the tests' recording fake; production runs
// exec through helperCommand.
type Runner interface {
	Run(ctx context.Context, h Helper, req Request) (stdout string, err error)
}

const (
	osascriptChooseFolder = `POSIX path of (choose folder with prompt "Choose a folder")`
	osascriptChooseOpen   = "on run argv\nPOSIX path of (choose file of type {item 1 of argv} with prompt \"Import memory\")\nend run"
	osascriptChooseSave   = "on run argv\nPOSIX path of (choose file name with prompt \"Export memory\" default name (item 1 of argv))\nend run"
)

func sanitizeFilterExt(ext string) string {
	ext = strings.TrimPrefix(strings.TrimSpace(ext), ".")
	var b strings.Builder
	for _, r := range ext {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	if b.Len() == 0 {
		return "md"
	}
	return b.String()
}

func sanitizeSaveFileName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = filepath.Base(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			continue
		}
		b.WriteRune(r)
	}
	clean := strings.TrimSpace(b.String())
	if clean == "" || clean == "." || clean == ".." {
		return "memory.md"
	}
	return clean
}

// helperCommand builds one CLI helper's run with literal program names and
// sanitized argv (no shell interpolation, gosec G204 clean).
func helperCommand(ctx context.Context, h Helper, req Request) (*exec.Cmd, bool) {
	ext := sanitizeFilterExt(req.FilterExt)
	saveName := sanitizeSaveFileName(req.DefaultName)
	switch h {
	case HelperOSAScript:
		switch req.Mode {
		case ModeOpenFile:
			return exec.CommandContext(ctx, "osascript", "-e", osascriptChooseOpen, "--", ext), true
		case ModeSaveFile:
			return exec.CommandContext(ctx, "osascript", "-e", osascriptChooseSave, "--", saveName), true
		default:
			return exec.CommandContext(ctx, "osascript", "-e", osascriptChooseFolder), true
		}
	case HelperZenity:
		switch req.Mode {
		case ModeOpenFile:
			return exec.CommandContext(ctx, "zenity", "--file-selection", "--file-filter=Markdown files | *."+ext), true
		case ModeSaveFile:
			return exec.CommandContext(ctx, "zenity", "--file-selection", "--save", "--confirm-overwrite", "--filename="+saveName), true
		default:
			return exec.CommandContext(ctx, "zenity", "--file-selection", "--directory"), true
		}
	case HelperKDialog:
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, false
		}
		var cmd *exec.Cmd
		switch req.Mode {
		case ModeOpenFile:
			cmd = exec.CommandContext(ctx, "kdialog", "--getopenfilename", ".", "*."+ext)
		case ModeSaveFile:
			cmd = exec.CommandContext(ctx, "kdialog", "--getsavefilename", saveName, "*."+ext)
		default:
			cmd = exec.CommandContext(ctx, "kdialog", "--getexistingdirectory", ".")
		}
		cmd.Dir = home
		return cmd, true
	case HelperNone, HelperPortal:
		return nil, false
	}
	return nil, false
}

// execRunner runs helpers through exec.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, h Helper, req Request) (string, error) {
	cmd, ok := helperCommand(ctx, h, req)
	if !ok {
		return "", errNoPicker
	}
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	var eerr *exec.ExitError
	if errors.As(err, &eerr) {
		return "", &exitError{code: eerr.ExitCode(), err: errHelperFailed}
	}
	return "", fmt.Errorf("%w: %s", errHelperRun, helperName(h))
}

// helperName is the helper's program name, for messages.
func helperName(h Helper) string {
	switch h {
	case HelperOSAScript:
		return "osascript"
	case HelperZenity:
		return "zenity"
	case HelperKDialog:
		return "kdialog"
	case HelperNone, HelperPortal:
		return "helper"
	}
	return "helper"
}

// pickPortalFunc runs the portal FileChooser flow: the chosen path,
// whether the user closed it, or why it could not run.
type pickPortalFunc func(ctx context.Context, req Request) (path string, cancelled bool, err error)

// Result is one finished pick, delivered to the window goroutine.
type Result struct {
	// Mode and Tag echo the Request that initiated this pick.
	Mode Mode
	Tag  string
	// Path is the chosen folder or file's absolute path, empty unless chosen.
	Path string
	// Cancelled marks a chooser the user closed without choosing: a no-op.
	Cancelled bool
	// Err is why no chooser ran or answered.
	Err error
}

const (
	pickBufferSize = 1
	probeTimeout   = 2 * time.Second
)

// Picker runs folder and file picks off the UI goroutine.
type Picker struct {
	probeOf  func(ctx context.Context) Availability
	runner   Runner
	portal   pickPortalFunc
	onResult func()
	goos     string

	reqs     chan Request
	results  chan Result
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once

	probeOnce sync.Once
	probed    atomic.Bool
	avail     atomic.Pointer[Availability]
}

// NewPicker builds a picker over the platform's probe and helpers.
func NewPicker(onResult func()) *Picker {
	seq := &atomic.Uint64{}
	return &Picker{
		probeOf: platformProbe,
		runner:  execRunner{},
		portal: func(ctx context.Context, req Request) (string, bool, error) {
			return pickPortal(ctx, seq, req)
		},
		onResult: onResult,
		goos:     runtime.GOOS,
		reqs:     make(chan Request, pickBufferSize),
		results:  make(chan Result, pickBufferSize),
		done:     make(chan struct{}),
	}
}

// Start launches the worker goroutine, which runs the one availability probe
// and then serves picks until Stop or the context ends.
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

// Pick requests one folder pick and reports whether it was accepted.
func (p *Picker) Pick() bool {
	return p.PickWith(Request{Mode: ModeFolder})
}

// PickOpenFile requests an open-file chooser filtered to filterExt (e.g. ".md").
func (p *Picker) PickOpenFile(tag, filterExt string) bool {
	return p.PickWith(Request{Mode: ModeOpenFile, Tag: tag, FilterExt: filterExt})
}

// PickSaveFile requests a save-file chooser seeded with defaultName.
func (p *Picker) PickSaveFile(tag, defaultName string) bool {
	return p.PickWith(Request{Mode: ModeSaveFile, Tag: tag, DefaultName: defaultName, FilterExt: "md"})
}

// PickWith requests a chooser pick described by req and reports whether it was accepted.
func (p *Picker) PickWith(req Request) bool {
	select {
	case p.reqs <- req:
		return true
	default:
		return false
	}
}

// Results receives the finished picks. The window goroutine drains it every frame.
func (p *Picker) Results() <-chan Result { return p.results }

// Available is the desktop's probed answer, or zero until the probe finishes.
func (p *Picker) Available() Availability {
	if av := p.avail.Load(); av != nil {
		return *av
	}
	return Availability{}
}

// Probed reports whether the availability probe has finished.
func (p *Picker) Probed() bool { return p.probed.Load() }

// First is the helper a pick would run, or HelperNone.
func (p *Picker) First() Helper { return selectHelper(p.Available(), p.goos) }

func (p *Picker) ensureProbed(ctx context.Context) {
	p.probeOnce.Do(func() {
		probeCtx, cancelProbe := context.WithTimeout(ctx, probeTimeout)
		av := p.probeOf(probeCtx)
		cancelProbe()
		p.avail.Store(&av)
		p.probed.Store(true)
	})
}

func (p *Picker) run(ctx context.Context) {
	defer close(p.done)
	p.ensureProbed(ctx)
	if ctx.Err() != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-p.reqs:
			res := p.pickAll(ctx, req)
			if ctx.Err() != nil {
				return
			}
			select {
			case p.results <- res:
				if p.onResult != nil {
					p.onResult()
				}
			case <-ctx.Done():
				return
			}
		}
	}
}

func (p *Picker) pickAll(ctx context.Context, req Request) Result {
	order := pickOrder(p.Available(), p.goos)
	if len(order) == 0 {
		return Result{Mode: req.Mode, Tag: req.Tag, Err: errNoPicker}
	}
	var lastErr error
	for _, h := range order {
		res, ran := p.pickOne(ctx, h, req)
		if ran {
			res.Mode = req.Mode
			res.Tag = req.Tag
			return res
		}
		lastErr = res.Err
	}
	if lastErr == nil {
		lastErr = errNoPicker
	}
	return Result{Mode: req.Mode, Tag: req.Tag, Err: fmt.Errorf("the folder picker failed: %w", lastErr)}
}

func (p *Picker) pickOne(ctx context.Context, h Helper, req Request) (res Result, ran bool) {
	if ctx.Err() != nil {
		return Result{Err: ctx.Err()}, false
	}
	if h == HelperPortal {
		path, cancelled, err := p.portal(ctx, req)
		if err != nil {
			return Result{Err: err}, false
		}
		return Result{Path: path, Cancelled: cancelled}, true
	}
	out, err := p.runner.Run(ctx, h, req)
	if err != nil {
		if code, isExit := exitCodeOf(err); isExit && code == 1 {
			return Result{Cancelled: true}, true
		}
		return Result{Err: fmt.Errorf("%w: %s", err, helperName(h))}, false
	}
	if out == "" {
		return Result{Cancelled: true}, true
	}
	return Result{Path: out}, true
}

func uriPath(uri string) (string, error) {
	const scheme = "file://"
	if !strings.HasPrefix(uri, scheme) {
		return "", fmt.Errorf("%w: %q", errNotAFilePath, uri)
	}
	if !strings.HasPrefix(uri[len(scheme):], "/") {
		return "", fmt.Errorf("%w: %q", errForeignFolder, uri)
	}
	return unescape(uri[len(scheme):]), nil
}

func unescape(path string) string {
	if !strings.Contains(path, "%") {
		return path
	}
	var b strings.Builder
	b.Grow(len(path))
	for i := 0; i < len(path); i++ {
		if c := path[i]; c == '%' && i+2 < len(path) {
			hi, ok := hexVal(path[i+1])
			if ok {
				lo, ok := hexVal(path[i+2])
				if ok {
					b.WriteByte(hi<<4 | lo)
					i += 2
					continue
				}
			}
		}
		b.WriteByte(path[i])
	}
	return b.String()
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
