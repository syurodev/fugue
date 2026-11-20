package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

// DefaultHandshakeTimeout is the vendor-runtime §13 handshake budget: a session
// that does not complete initialize + thread/start within 30 s fails typed
// (vendor.ErrHandshakeTimeout).
const DefaultHandshakeTimeout = 30 * time.Second

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
		return errcode.New(errcode.InvalidArgument, "codex: invalid model parameter")
	}
	for i := range len(model) {
		if !isModelChar(i, model[i]) {
			return errcode.New(errcode.InvalidArgument, "codex: invalid model parameter")
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
			return errcode.New(errcode.InvalidArgument, "codex: invalid effort parameter")
		}
		return nil
	default:
		return errcode.New(errcode.InvalidArgument, "codex: invalid effort parameter")
	}
}

func validateResumeSessionID(id string) error {
	if id == "" {
		return nil
	}
	if !ids.IsUUID(id) || IsDenylistedFlag(id) {
		return errcode.New(errcode.InvalidArgument, "codex: invalid resume session id parameter")
	}
	return nil
}

// DenylistedFlags returns the permission-widening and sandbox-bypass flags
// forbidden by vendor-runtime §5.2 and §8 (VR-04).
func DenylistedFlags() []string {
	return []string{
		"--dangerously-bypass-approvals-and-sandbox",
		"--full-auto",
		"--ask-for-approval",
		"-a",
		"--sandbox",
		"-s",
		"--yolo",
		"--cd",
		"-C",
	}
}

// IsDenylistedFlag reports whether arg matches or starts with any forbidden
// security-sensitive Codex flag.
func IsDenylistedFlag(arg string) bool {
	for _, denied := range DenylistedFlags() {
		if arg == denied || strings.HasPrefix(arg, denied+"=") {
			return true
		}
	}
	return false
}

func validateProfile(spec *vendor.StartSpec) error {
	if err := validateModel(spec.Model); err != nil {
		return err
	}
	if err := validateEffort(spec.Effort); err != nil {
		return err
	}
	return validateResumeSessionID(spec.ResumeSessionID)
}

// BuildArgv constructs the vetted spawn argv for a Codex app-server session.
// It carries no permission-widening flag, and Codex's own approval policy stays
// at its default (VR-04, vendor-runtime §5.2). The Fugue MCP overrides are
// appended after the denylist check: they are Fugue's own, and no profile can
// set them.
func BuildArgv(binary string, spec *vendor.StartSpec) ([]string, error) {
	if binary == "" {
		return nil, errcode.New(errcode.InvalidArgument, "codex binary path is empty")
	}
	if spec == nil {
		spec = &vendor.StartSpec{}
	}
	if err := validateProfile(spec); err != nil {
		return nil, err
	}
	argv := []string{binary, "app-server"}
	for _, arg := range argv[1:] {
		if IsDenylistedFlag(arg) {
			return nil, errcode.New(errcode.InvalidArgument, "codex: invalid argument")
		}
	}
	if spec.Bridge.Binary == "" {
		return argv, nil
	}
	overrides, err := bridgeConfigOverrides(spec.Bridge)
	if err != nil {
		return nil, err
	}
	grown := make([]string, len(argv), len(argv)+len(overrides))
	copy(grown, argv)
	return append(grown, overrides...), nil
}

// SessionEnv is the environment of a Codex session process: the §8 allowlist
// of ProbeEnv plus, when the host minted one, the session token as the single
// added variable (VR-E09).
func SessionEnv(spec *vendor.StartSpec) []string {
	if spec.SessionToken.IsZero() {
		return ProbeEnv(spec.HostEnv, spec.Home)
	}
	return filteredEnv(spec.HostEnv, spec.Home, []string{vendor.SessionTokenEnv + "=" + spec.SessionToken.Reveal()})
}

func (a *Adapter) resolveStartBinary(ctx context.Context, home vendor.AccountHome) (string, error) {
	if a.Binary != "" && filepath.IsAbs(a.Binary) {
		path, err := vendor.CheckAbsoluteBinary(DefaultBinary, a.Binary)
		if err != nil {
			return "", errcode.Wrap(errcode.CodeOf(err), "check codex binary", err)
		}
		return path, nil
	}
	probe, err := a.Probe(ctx, home)
	if err != nil {
		return "", err
	}
	return probe.Path, nil
}

// Start launches a new `codex app-server` session process and conducts the
// 30-second initial handshake (`initialize`, `initialized`,
// `account/rateLimits/read`, and `thread/start` or `thread/resume`).
func (a *Adapter) Start(ctx context.Context, spec *vendor.StartSpec) (vendor.Session, error) {
	if spec == nil {
		return nil, errcode.New(errcode.InvalidArgument, "spec must not be nil")
	}
	if strings.TrimSpace(spec.SessionID) == "" {
		return nil, errcode.New(errcode.InvalidArgument, "session id is required")
	}
	binaryPath, err := a.resolveStartBinary(ctx, spec.Home)
	if err != nil {
		return nil, err
	}
	argv, err := BuildArgv(binaryPath, spec)
	if err != nil {
		return nil, err
	}
	sess := a.newSessionState(spec)
	proc, err := a.spawnSessionProcess(ctx, spec, argv, sess.clock, sess)
	if err != nil {
		return nil, err
	}
	sess.proc = proc
	if spec.OnSpawned != nil {
		spec.OnSpawned(sess)
	}
	readerErrCh := make(chan error, 1)
	supervise.Go(context.WithoutCancel(ctx), "codex.reader", func(taskCtx context.Context) error {
		sess.runReader(taskCtx, readerErrCh)
		return nil
	})
	if err := sess.handshake(ctx, spec, readerErrCh); err != nil {
		return nil, err
	}
	return sess, nil
}

func (a *Adapter) newSessionState(spec *vendor.StartSpec) *Session {
	clk := a.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	idGen := a.IDs
	if idGen == nil {
		idGen = ids.NewUUIDv7(clk, ids.CryptoRandom{})
	}
	codec := NewCodec()
	return &Session{
		sessionID:         spec.SessionID,
		model:             spec.Model,
		cwd:               spec.Cwd,
		codec:             codec,
		normalizer:        NewNormalizerWithCodec(spec.SessionID, codec),
		clock:             clk,
		idGen:             idGen,
		obsCh:             make(chan Observation, observationBufferSize),
		closeNotify:       make(chan struct{}),
		readerDoneCh:      make(chan struct{}),
		writeSem:          make(chan struct{}, 1),
		state:             stateStarting,
		pendingRPCs:       make(map[string]chan Frame),
		seenApprovals:     make(map[string]bool),
		pendingApprovals:  make(map[string]pendingServerRequest),
		resolvedApprovals: make(map[string]bool),
	}
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
		Env:           SessionEnv(spec),
		Clock:         clk,
		Interrupt:     makeProtocolInterrupt(sess),
		SessionID:     spec.SessionID,
		XDGRuntimeDir: a.XDGRuntimeDir,
		MemoryMax:     spec.MemoryMax,
		CPUWeight:     spec.CPUWeight,
		Probe:         probe,
		OnEscalation:  a.OnEscalation,
	}
	proc, err := process.Spawn(ctx, opts)
	if err != nil {
		if errors.Is(err, process.ErrBinaryNotFound) {
			return nil, errcode.Wrap(errcode.NotFound, "spawn codex process", err)
		}
		if errors.Is(err, process.ErrNotExecutable) {
			return nil, errcode.Wrap(errcode.PermissionDenied, "spawn codex process", err)
		}
		return nil, errcode.Wrap(errcode.Internal, "spawn codex process", err)
	}
	return proc, nil
}

func makeProtocolInterrupt(sess *Session) func(context.Context) error {
	return func(ctx context.Context) error {
		if sess == nil || sess.proc == nil {
			return nil
		}
		threadID, turnID := sess.interruptTarget()
		if threadID == "" || turnID == "" {
			return nil
		}
		_, raw, err := sess.codec.NextRequest(MethodTurnInterrupt, TurnInterruptParams{
			ThreadID: threadID,
			TurnID:   turnID,
		})
		if err != nil {
			return err
		}
		select {
		case sess.writeSem <- struct{}{}:
			defer func() { <-sess.writeSem }()
		default:
			return nil
		}
		if writeErr := sess.proc.WriteLine(bytes.TrimRight(raw, "\r\n")); writeErr != nil {
			if errors.Is(writeErr, process.ErrStdinClosed) {
				return nil
			}
			return errcode.Wrap(errcode.Internal, "write stop interrupt", writeErr)
		}
		return nil
	}
}

func (s *Session) handshake(ctx context.Context, spec *vendor.StartSpec, readerErrCh <-chan error) error {
	timer := s.clock.NewTimer(DefaultHandshakeTimeout)
	defer timer.Stop()

	initParams := InitializeParams{
		ClientInfo:   ClientInfo{Name: "fugue", Version: "0.1.0"},
		Capabilities: &InitializeCapabilities{ExperimentalAPI: true},
	}
	if _, err := s.callHandshakeRPC(ctx, timer, readerErrCh, MethodInitialize, initParams); err != nil {
		return err
	}
	if err := s.writeNotification(ctx, MethodInitialized, nil); err != nil {
		s.cleanupStartup(ctx)
		return err
	}
	// Read rate limits once after initialize (vendor-runtime §6); best-effort if the
	// account does not expose rate limits.
	if _, err := s.callHandshakeRPC(ctx, timer, readerErrCh, MethodRateLimitsRead, nil); err != nil {
		slog.DebugContext(ctx, "codex rate limit read during handshake failed", "session_id", s.sessionID, "error", err)
	}

	threadFrame, err := s.startOrResumeThread(ctx, timer, readerErrCh, spec)
	if err != nil {
		return err
	}
	var threadResp ThreadStartResponse
	if json.Unmarshal(threadFrame.Result, &threadResp) != nil || threadResp.Thread.ID == "" {
		s.cleanupStartup(ctx)
		return errcode.New(errcode.Internal, "codex handshake did not return a thread id")
	}

	s.mu.Lock()
	s.threadID = threadResp.Thread.ID
	if threadResp.Model != "" {
		s.model = threadResp.Model
	}
	s.state = stateReady
	s.mu.Unlock()
	return nil
}

func (s *Session) startOrResumeThread(
	ctx context.Context,
	timer clock.Timer,
	readerErrCh <-chan error,
	spec *vendor.StartSpec,
) (Frame, error) {
	if spec.ResumeSessionID != "" {
		return s.callHandshakeRPC(ctx, timer, readerErrCh, MethodThreadResume, ThreadResumeParams{
			ThreadID: spec.ResumeSessionID,
			Cwd:      spec.Cwd,
			Model:    spec.Model,
		})
	}
	return s.callHandshakeRPC(ctx, timer, readerErrCh, MethodThreadStart, ThreadStartParams{
		Cwd:   spec.Cwd,
		Model: spec.Model,
	})
}

func (s *Session) callHandshakeRPC(
	ctx context.Context,
	timer clock.Timer,
	readerErrCh <-chan error,
	method string,
	params any,
) (Frame, error) {
	ch, cid, raw, err := s.prepareRPC(method, params)
	if err != nil {
		s.cleanupStartup(ctx)
		return Frame{}, err
	}
	defer s.removePendingRPC(cid)

	if err := s.writeRawLine(ctx, raw); err != nil {
		s.cleanupStartup(ctx)
		return Frame{}, err
	}

	select {
	case frame := <-ch:
		if frame.Error != nil {
			s.cleanupStartup(ctx)
			return Frame{}, errcode.Wrap(errcode.Internal, "codex handshake rpc failed", frame.Error)
		}
		return frame, nil
	case <-timer.C():
		s.cleanupStartup(ctx)
		return Frame{}, vendor.ErrHandshakeTimeout
	case <-ctx.Done():
		s.cleanupStartup(ctx)
		return Frame{}, errcode.Wrap(errcode.Canceled, "session start canceled", ctx.Err())
	case rErr := <-readerErrCh:
		s.cleanupStartup(ctx)
		if errors.Is(rErr, process.ErrFrameTooLarge) {
			return Frame{}, rErr
		}
		return Frame{}, errcode.Wrap(errcode.Internal, "codex session failed before handshake", rErr)
	}
}

func (s *Session) cleanupStartup(ctx context.Context) {
	if err := s.proc.CloseStdin(); err != nil {
		slog.WarnContext(ctx, "codex startup cleanup: close stdin error", "session_id", s.sessionID, "error", err)
	}
	if _, err := s.proc.Stop(context.WithoutCancel(ctx)); err != nil {
		slog.WarnContext(ctx, "codex startup cleanup: stop error", "session_id", s.sessionID, "error", err)
	}
	s.closeNotifyOnce.Do(func() { close(s.closeNotify) })
	<-s.readerDoneCh
}
