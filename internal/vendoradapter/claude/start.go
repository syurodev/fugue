package claude

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

// DenylistedFlags returns the flags forbidden by vendor runtime §8.
func DenylistedFlags() []string {
	return []string{
		"--dangerously-skip-permissions",
		"--allow-dangerously-skip-permissions",
		"--permission-mode",
		"--allowedTools",
		"--allowed-tools",
		"--disallowedTools",
		"--add-dir",
		"--permission-prompt-tool",
		"--mcp-config",
	}
}

// IsDenylistedFlag reports whether an argument starts with or matches any
// forbidden security-sensitive flag.
func IsDenylistedFlag(arg string) bool {
	for _, denied := range DenylistedFlags() {
		if arg == denied || strings.HasPrefix(arg, denied+"=") {
			return true
		}
	}
	return false
}

// BuildArgv constructs the vetted spawn argv for a Claude Code session.
func BuildArgv(binary string, spec *vendor.StartSpec) ([]string, error) {
	if binary == "" {
		return nil, errcode.New(errcode.InvalidArgument, "claude binary path is empty")
	}
	if spec == nil {
		spec = &vendor.StartSpec{}
	}
	argv := []string{
		binary,
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}

	if spec.Model != "" {
		if IsDenylistedFlag(spec.Model) {
			return nil, errcode.New(errcode.InvalidArgument, "denylisted argument: "+spec.Model)
		}
		argv = append(argv, "--model", spec.Model)
	}
	if spec.Effort != "" {
		if IsDenylistedFlag(spec.Effort) {
			return nil, errcode.New(errcode.InvalidArgument, "denylisted argument: "+spec.Effort)
		}
		argv = append(argv, "--effort", spec.Effort)
	}
	if spec.ResumeSessionID != "" {
		if IsDenylistedFlag(spec.ResumeSessionID) {
			return nil, errcode.New(errcode.InvalidArgument, "denylisted argument: "+spec.ResumeSessionID)
		}
		argv = append(argv, "--resume", spec.ResumeSessionID)
	}

	for _, arg := range argv[1:] {
		if IsDenylistedFlag(arg) {
			return nil, errcode.New(errcode.InvalidArgument, "denylisted argument: "+arg)
		}
	}
	return argv, nil
}

func (a *Adapter) resolveStartBinary(ctx context.Context, spec *vendor.StartSpec) (string, error) {
	if spec.BinaryPath != "" {
		if !filepath.IsAbs(spec.BinaryPath) {
			return "", errcode.New(errcode.InvalidArgument, "spec binary path must be an absolute path")
		}
		return spec.BinaryPath, nil
	}
	probe, err := a.Probe(ctx, spec.Home)
	if err != nil {
		return "", err
	}
	return probe.Path, nil
}

// Start launches a new Claude Code session process and conducts the 30-second
// initial handshake awaiting system/init.
func (a *Adapter) Start(ctx context.Context, spec *vendor.StartSpec) (vendor.Session, error) {
	if spec == nil {
		return nil, errcode.New(errcode.InvalidArgument, "spec must not be nil")
	}

	binaryPath, err := a.resolveStartBinary(ctx, spec)
	if err != nil {
		return nil, err
	}

	argv, err := BuildArgv(binaryPath, spec)
	if err != nil {
		return nil, err
	}

	clk := spec.Clock
	if clk == nil {
		clk = clock.Real{}
	}

	var idGen idGenerator = &defaultSequenceIDGen{}
	if spec.IDs != nil {
		idGen = spec.IDs
	}

	sess := &Session{
		clock:        clk,
		idGen:        idGen,
		obsCh:        make(chan Observation, observationBufferSize),
		closeNotify:  make(chan struct{}),
		readerDoneCh: make(chan struct{}),
		state:        stateStarting,
	}

	proc, err := a.spawnSessionProcess(ctx, spec, argv, clk, sess)
	if err != nil {
		return nil, err
	}
	sess.proc = proc
	sess.normalizer = NewNormalizer(spec.SessionID)

	initCh := make(chan *Init, 1)
	readerErrCh := make(chan error, 1)

	_ = supervise.Go(ctx, "claude.reader", func(taskCtx context.Context) error {
		sess.runReader(taskCtx, initCh, readerErrCh)
		return nil
	})

	if err := sess.waitForHandshake(ctx, initCh, readerErrCh); err != nil {
		return nil, err
	}
	return sess, nil
}

func (a *Adapter) spawnSessionProcess(
	ctx context.Context,
	spec *vendor.StartSpec,
	argv []string,
	clk clock.Clock,
	sess *Session,
) (*process.Process, error) {
	probe := spec.Probe
	if probe == nil {
		probe = process.NewSystemdProbe()
	}

	opts := &process.Options{
		Argv:          argv,
		Cwd:           spec.Cwd,
		Env:           ProbeEnv(spec.HostEnv, spec.Home),
		Clock:         clk,
		Interrupt:     makeProtocolInterrupt(sess),
		SessionID:     spec.SessionID,
		XDGRuntimeDir: spec.XDGRuntimeDir,
		Probe:         probe,
		OnEscalation:  spec.OnEscalation,
	}

	proc, err := process.Spawn(ctx, opts)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "spawn claude process", err)
	}
	return proc, nil
}

func makeProtocolInterrupt(sess *Session) func(context.Context) error {
	return func(_ context.Context) error {
		if sess == nil || sess.proc == nil {
			return nil
		}
		ctrl := wireControlRequest{
			Type:      "control_request",
			RequestID: "req-stop-interrupt",
			Request: wireControlSubtype{
				Subtype: controlInterrupt,
			},
		}
		raw, err := json.Marshal(ctrl)
		if err != nil {
			return errcode.Wrap(errcode.Internal, "encode stop interrupt", err)
		}
		if writeErr := sess.proc.WriteLine(raw); writeErr != nil {
			if errors.Is(writeErr, process.ErrStdinClosed) {
				return nil
			}
			return errcode.Wrap(errcode.Internal, "write stop interrupt", writeErr)
		}
		return nil
	}
}

func (s *Session) waitForHandshake(ctx context.Context, initCh <-chan *Init, readerErrCh <-chan error) error {
	timer := s.clock.NewTimer(defaultHandshakeTimeout)
	defer timer.Stop()

	select {
	case <-initCh:
		s.mu.Lock()
		s.state = stateReady
		s.mu.Unlock()
		return nil

	case <-timer.C():
		s.cleanupStartup(ctx)
		return vendor.ErrHandshakeTimeout

	case <-ctx.Done():
		s.cleanupStartup(ctx)
		return errcode.Wrap(errcode.Canceled, "session start canceled", ctx.Err())

	case rErr := <-readerErrCh:
		s.cleanupStartup(ctx)
		if errors.Is(rErr, process.ErrFrameTooLarge) {
			return rErr
		}
		return errcode.Wrap(errcode.Internal, "claude session failed before handshake", rErr)
	}
}

func (s *Session) cleanupStartup(ctx context.Context) {
	if err := s.proc.CloseStdin(); err != nil {
		_ = err
	}
	if _, err := s.proc.Stop(ctx); err != nil {
		_ = err
	}
	close(s.closeNotify)
	<-s.readerDoneCh
}
