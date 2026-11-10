package claude

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

const maxModelLength = 128

func isModelChar(i int, c byte) bool {
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return true
	}
	return i > 0 && strings.IndexByte("._:[]-", c) != -1
}

func validateModel(model string) error {
	if model == "" {
		return nil
	}
	if strings.TrimSpace(model) == "" || len(model) > maxModelLength || IsDenylistedFlag(model) {
		return errcode.New(errcode.InvalidArgument, "claude: invalid model parameter")
	}
	for i := range len(model) {
		if !isModelChar(i, model[i]) {
			return errcode.New(errcode.InvalidArgument, "claude: invalid model parameter")
		}
	}
	return nil
}

func validateEffort(effort string) error {
	if effort == "" {
		return nil
	}
	switch effort {
	case "low", "medium", "high", "xhigh", "max":
		if IsDenylistedFlag(effort) {
			return errcode.New(errcode.InvalidArgument, "claude: invalid effort parameter")
		}
		return nil
	default:
		return errcode.New(errcode.InvalidArgument, "claude: invalid effort parameter")
	}
}

func validateResumeSessionID(id string) error {
	if id == "" {
		return nil
	}
	if !ids.IsUUID(id) || IsDenylistedFlag(id) {
		return errcode.New(errcode.InvalidArgument, "claude: invalid resume session id parameter")
	}
	return nil
}

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
	if err := validateModel(spec.Model); err != nil {
		return nil, err
	}
	if err := validateEffort(spec.Effort); err != nil {
		return nil, err
	}
	if err := validateResumeSessionID(spec.ResumeSessionID); err != nil {
		return nil, err
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
		argv = append(argv, "--model", spec.Model)
	}
	if spec.Effort != "" {
		argv = append(argv, "--effort", spec.Effort)
	}
	if spec.ResumeSessionID != "" {
		argv = append(argv, "--resume", spec.ResumeSessionID)
	}

	for _, arg := range argv[1:] {
		if IsDenylistedFlag(arg) {
			return nil, errcode.New(errcode.InvalidArgument, "claude: invalid argument")
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

	clk := a.Clock
	if clk == nil {
		clk = clock.Real{}
	}

	idGen := a.IDs
	if idGen == nil {
		idGen = ids.NewUUIDv7(clk, ids.CryptoRandom{})
	}

	sess := &Session{
		sessionID:    spec.SessionID,
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

	supervise.Go(ctx, "claude.reader", func(taskCtx context.Context) error {
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
	probe := a.SystemdProbe
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
		XDGRuntimeDir: a.XDGRuntimeDir,
		Probe:         probe,
		OnEscalation:  a.OnEscalation,
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
		sess.writeMu.Lock()
		defer sess.writeMu.Unlock()
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
		slog.Warn("claude startup cleanup: close stdin error", "session_id", s.sessionID, "error", err)
	}
	if _, err := s.proc.Stop(ctx); err != nil {
		slog.Warn("claude startup cleanup: stop error", "session_id", s.sessionID, "error", err)
	}
	close(s.closeNotify)
	<-s.readerDoneCh
}
