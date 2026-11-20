package agy

import (
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
// that does not complete initialize + session/new (or session/load) within 30 s
// fails typed (vendor.ErrHandshakeTimeout).
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
		return errcode.New(errcode.InvalidArgument, "agy: invalid model parameter")
	}
	for i := range len(model) {
		if !isModelChar(i, model[i]) {
			return errcode.New(errcode.InvalidArgument, "agy: invalid model parameter")
		}
	}
	return nil
}

func validateEffort(effort string) error {
	if effort == "" {
		return nil
	}
	switch effort {
	case "low", "medium", "high":
		if IsDenylistedFlag(effort) {
			return errcode.New(errcode.InvalidArgument, "agy: invalid effort parameter")
		}
		return nil
	default:
		return errcode.New(errcode.InvalidArgument, "agy: invalid effort parameter")
	}
}

func validateResumeSessionID(id string) error {
	if id == "" {
		return nil
	}
	if !ids.IsUUID(id) || IsDenylistedFlag(id) {
		return errcode.New(errcode.InvalidArgument, "agy: invalid resume session id parameter")
	}
	return nil
}

// DenylistedFlags returns permission-widening, sandbox-bypass, and mode flags
// forbidden on spawned agy argv (VR-04, vendor-runtime §5.3, §8).
func DenylistedFlags() []string {
	return []string{
		"--dangerously-skip-permissions",
		"--sandbox",
		"--mode",
		"--add-dir",
		"--settings",
		"--ide-mcp-port",
		"--cloud_code_endpoint",
		"--use_stubby_auth",
	}
}

// IsDenylistedFlag reports whether arg matches or starts with any forbidden agy flag.
func IsDenylistedFlag(arg string) bool {
	for _, denied := range DenylistedFlags() {
		if arg == denied || strings.HasPrefix(arg, denied+"=") {
			return true
		}
	}
	return false
}

// ValidateArgv verifies that every non-binary entry in argv is either the `acp`
// subcommand or an allowed vendor-runtime §7 profile flag, rejecting unknown or
// denylisted flags.
func ValidateArgv(argv []string) error {
	if len(argv) < 2 || argv[0] == "" || argv[1] != "acp" {
		return errcode.New(errcode.InvalidArgument, "agy: argv must invoke acp subcommand")
	}
	for _, arg := range argv[2:] {
		if IsDenylistedFlag(arg) || !isAllowedProfileFlag(arg) {
			return errcode.New(errcode.InvalidArgument, "agy: disallowed or unknown profile flag")
		}
	}
	return nil
}

func isAllowedProfileFlag(arg string) bool {
	return arg == "--disable-slash-commands"
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

// BuildArgv constructs the vetted spawn argv for an Antigravity ACP session.
// It carries no permission-widening or sandbox flag (VR-04).
func BuildArgv(binary string, spec *vendor.StartSpec) ([]string, error) {
	if binary == "" {
		return nil, errcode.New(errcode.InvalidArgument, "agy binary path is empty")
	}
	if spec == nil {
		spec = &vendor.StartSpec{}
	}
	if err := validateProfile(spec); err != nil {
		return nil, err
	}
	argv := []string{binary, "acp"}
	if err := ValidateArgv(argv); err != nil {
		return nil, err
	}
	return argv, nil
}

// SessionEnv is the environment of an agy ACP session process: the §8 allowlist
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
			return "", errcode.Wrap(errcode.CodeOf(err), "check agy binary", err)
		}
		return path, nil
	}
	probe, err := a.Probe(ctx, home)
	if err != nil {
		return "", err
	}
	return probe.Path, nil
}

// Start launches an `agy acp` session process and performs the 30-second
// initialize + session/new (or session/load) handshake.
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
	supervise.Go(context.WithoutCancel(ctx), "agy.reader", func(taskCtx context.Context) error {
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
	clientVer := strings.TrimSpace(a.ClientVersion)
	if clientVer == "" {
		clientVer = DefaultClientVersion
	}
	codec := NewCodec()
	return &Session{
		sessionID:     spec.SessionID,
		model:         spec.Model,
		cwd:           spec.Cwd,
		clientVersion: clientVer,
		codec:         codec,
		normalizer:    NewNormalizerWithCodec(spec.SessionID, codec),
		clock:         clk,
		idGen:         idGen,
		obsCh:         make(chan Observation, observationBufferSize),
		closeNotify:   make(chan struct{}),
		readerDoneCh:  make(chan struct{}),
		writeSem:      make(chan struct{}, 1),
		state:         stateStarting,
		pendingRPCs:   make(map[string]chan Frame),
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
			return nil, errcode.Wrap(errcode.NotFound, "spawn agy process", err)
		}
		if errors.Is(err, process.ErrNotExecutable) {
			return nil, errcode.Wrap(errcode.PermissionDenied, "spawn agy process", err)
		}
		return nil, errcode.Wrap(errcode.Internal, "spawn agy process", err)
	}
	return proc, nil
}

func makeProtocolInterrupt(sess *Session) func(context.Context) error {
	return func(context.Context) error {
		if sess == nil || sess.proc == nil {
			return nil
		}
		sid := sess.acpID()
		if sid == "" {
			return nil
		}
		if err := sess.sendCancelNotification(sid); err != nil {
			if errors.Is(err, process.ErrStdinClosed) || errcode.CodeOf(err) == errcode.Canceled {
				return nil
			}
			return err
		}
		return nil
	}
}

func (s *Session) handshake(ctx context.Context, spec *vendor.StartSpec, readerErrCh <-chan error) error {
	timer := s.clock.NewTimer(DefaultHandshakeTimeout)
	defer timer.Stop()

	caps, err := s.runInitializeHandshake(ctx, timer, readerErrCh)
	if err != nil {
		return err
	}

	sessFrame, err := s.openACPSession(ctx, timer, readerErrCh, spec)
	if err != nil {
		return err
	}
	var sessResp SessionStateResult
	if json.Unmarshal(sessFrame.Result, &sessResp) != nil || sessResp.SessionID == "" {
		s.cleanupStartup(ctx)
		return errcode.New(errcode.Internal, "agy handshake did not return a session id")
	}

	s.recordSessionReady(&sessResp, caps)
	return s.applyProfileConfigOptions(ctx, timer, readerErrCh, sessResp.SessionID, spec)
}

func (s *Session) runInitializeHandshake(
	ctx context.Context,
	timer clock.Timer,
	readerErrCh <-chan error,
) (HandshakeCapabilities, error) {
	initParams := InitializeParams{
		ProtocolVersion:    ProtocolVersion,
		ClientCapabilities: map[string]any{},
		ClientInfo:         ClientInfo{Name: "fugue", Version: s.clientVersion},
	}
	initFrame, err := s.callHandshakeRPC(ctx, timer, readerErrCh, MethodInitialize, initParams)
	if err != nil {
		return HandshakeCapabilities{}, err
	}
	var initResp InitializeResult
	if json.Unmarshal(initFrame.Result, &initResp) != nil || initResp.ProtocolVersion != ProtocolVersion {
		s.cleanupStartup(ctx)
		return HandshakeCapabilities{}, errcode.New(errcode.Internal, "agy handshake returned unsupported protocol version")
	}
	if initResp.AgentInfo != nil && initResp.AgentInfo.Version != "" {
		_, tested, warning := versionVerdict(initResp.AgentInfo.Version)
		if !tested && warning != "" {
			slog.WarnContext(ctx, warning, "session_id", s.sessionID, "version", initResp.AgentInfo.Version)
		}
	}
	return initResp.CapabilitiesSummary(), nil
}

func (s *Session) recordSessionReady(sessResp *SessionStateResult, caps HandshakeCapabilities) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acpSessionID = sessResp.SessionID
	s.handshakeCaps = caps
	s.models = sessResp.Models
	s.modes = sessResp.Modes
	s.configOptions = sessResp.ConfigOptions
	if s.model == "" && sessResp.Models != nil {
		s.model = sessResp.Models.CurrentModelID
	}
	s.state = stateReady
}

func (s *Session) openACPSession(
	ctx context.Context,
	timer clock.Timer,
	readerErrCh <-chan error,
	spec *vendor.StartSpec,
) (Frame, error) {
	servers, err := bridgeServers(spec.Bridge)
	if err != nil {
		return Frame{}, err
	}
	if spec.ResumeSessionID != "" {
		return s.callHandshakeRPC(ctx, timer, readerErrCh, MethodSessionLoad, SessionLoadParams{
			SessionID:  spec.ResumeSessionID,
			Cwd:        spec.Cwd,
			McpServers: servers,
		})
	}
	return s.callHandshakeRPC(ctx, timer, readerErrCh, MethodSessionNew, SessionNewParams{
		Cwd:        spec.Cwd,
		McpServers: servers,
	})
}

func (s *Session) applyProfileConfigOptions(
	ctx context.Context,
	timer clock.Timer,
	readerErrCh <-chan error,
	acpSessionID string,
	spec *vendor.StartSpec,
) error {
	if spec.Model != "" {
		if _, err := s.callHandshakeRPC(ctx, timer, readerErrCh, MethodSessionSetConfigOption, SessionSetConfigOptionParams{
			SessionID: acpSessionID,
			ConfigID:  "model",
			Value:     spec.Model,
		}); err != nil {
			return err
		}
	}
	if spec.Effort != "" {
		if _, err := s.callHandshakeRPC(ctx, timer, readerErrCh, MethodSessionSetConfigOption, SessionSetConfigOptionParams{
			SessionID: acpSessionID,
			ConfigID:  "thought_level",
			Value:     spec.Effort,
		}); err != nil {
			return err
		}
	}
	return nil
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
		if fErr := s.fatalFrameError(readerErrCh); fErr != nil {
			return Frame{}, fErr
		}
		return Frame{}, err
	}

	select {
	case frame := <-ch:
		if frame.Error != nil {
			s.cleanupStartup(ctx)
			return Frame{}, errcode.Wrap(errcode.Internal, "agy handshake rpc failed", frame.Error)
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
		return Frame{}, errcode.Wrap(errcode.Internal, "agy session failed before handshake", rErr)
	}
}

func (s *Session) cleanupStartup(ctx context.Context) {
	if err := s.proc.CloseStdin(); err != nil {
		slog.WarnContext(ctx, "agy startup cleanup: close stdin error", "session_id", s.sessionID, "error", err)
	}
	if _, err := s.proc.Stop(context.WithoutCancel(ctx)); err != nil {
		slog.WarnContext(ctx, "agy startup cleanup: stop error", "session_id", s.sessionID, "error", err)
	}
	s.closeNotifyOnce.Do(func() { close(s.closeNotify) })
	<-s.readerDoneCh
}

func (s *Session) fatalFrameError(readerErrCh <-chan error) error {
	if errors.Is(s.closedErr(), process.ErrFrameTooLarge) {
		return process.ErrFrameTooLarge
	}
	select {
	case rErr := <-readerErrCh:
		if errors.Is(rErr, process.ErrFrameTooLarge) || errcode.CodeOf(rErr) == errcode.FrameTooLarge {
			return rErr
		}
	default:
	}
	return nil
}
