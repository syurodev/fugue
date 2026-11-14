package claude

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
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
// without system/init within 30 s fails typed (vendor.ErrHandshakeTimeout).
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

func validateProfile(spec *vendor.StartSpec) error {
	if err := validateModel(spec.Model); err != nil {
		return err
	}
	if err := validateEffort(spec.Effort); err != nil {
		return err
	}
	return validateResumeSessionID(spec.ResumeSessionID)
}

// BuildArgv constructs the vetted spawn argv for a Claude Code session.
func BuildArgv(binary string, spec *vendor.StartSpec) ([]string, error) {
	if binary == "" {
		return nil, errcode.New(errcode.InvalidArgument, "claude binary path is empty")
	}
	if spec == nil {
		spec = &vendor.StartSpec{}
	}
	if err := validateProfile(spec); err != nil {
		return nil, err
	}

	argv := []string{
		binary,
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--replay-user-messages",
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
	// The denylist above covers every profile-derived argument. The MCP
	// flags below are Fugue's own and are appended after it: a profile can
	// never set them, and --strict-mcp-config keeps the user's and the
	// project's MCP servers out of the session.
	if spec.Bridge.Binary == "" {
		return argv, nil
	}
	config, err := bridgeConfig(spec.Bridge)
	if err != nil {
		return nil, err
	}
	return append(argv, "--mcp-config", config, "--strict-mcp-config"), nil
}

// BridgeServerName is the MCP server name Claude sees; its tools are named
// mcp__fugue__<tool>.
const BridgeServerName = "fugue"

type mcpServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// bridgeConfig renders the inline MCP configuration that makes Claude spawn
// `fugue mcp-bridge`. It names only the binary and the socket: the session
// token travels as an expanded environment entry, never in argv.
func bridgeConfig(bridge vendor.Bridge) (string, error) {
	if !filepath.IsAbs(bridge.Binary) || !filepath.IsAbs(bridge.Socket) {
		return "", errcode.New(errcode.InvalidArgument, "claude: the mcp bridge needs absolute binary and socket paths")
	}
	raw, err := json.Marshal(map[string]map[string]mcpServer{"mcpServers": {BridgeServerName: {
		Type: "stdio", Command: bridge.Binary, Args: []string{"mcp-bridge", "--socket", bridge.Socket},
		// Claude Code expands the placeholder from its own environment, where
		// the host put the token. The config carries only the placeholder, so
		// the token is in neither argv nor any file, and the bridge does not
		// depend on the CLI passing its whole environment on.
		Env: map[string]string{vendor.SessionTokenEnv: "${" + vendor.SessionTokenEnv + "}"},
	}}})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "claude: encode the mcp bridge configuration", err)
	}
	return string(raw), nil
}

// SessionEnv is the environment of a session process: the §8 allowlist of
// ProbeEnv plus, when the host minted one, the session token as the single
// added variable (VR-E09).
func SessionEnv(spec *vendor.StartSpec) []string {
	if spec.SessionToken.IsZero() {
		return ProbeEnv(spec.HostEnv, spec.Home)
	}
	return filteredEnv(spec.HostEnv, spec.Home, []string{vendor.SessionTokenEnv + "=" + spec.SessionToken.Reveal()})
}

func checkAbsoluteBinary(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errcode.Wrap(errcode.NotFound, "claude binary not found", err)
		}
		if errors.Is(err, os.ErrPermission) {
			return "", errcode.Wrap(errcode.PermissionDenied, "claude binary permission denied", err)
		}
		return "", errcode.Wrap(errcode.Internal, "stat claude binary", err)
	}
	return path, nil
}

func (a *Adapter) resolveStartBinary(ctx context.Context, home vendor.AccountHome) (string, error) {
	if a.Binary != "" && filepath.IsAbs(a.Binary) {
		return checkAbsoluteBinary(a.Binary)
	}
	probe, err := a.Probe(ctx, home)
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

	binaryPath, err := a.resolveStartBinary(ctx, spec.Home)
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
		writeSem:     make(chan struct{}, 1),
		state:        stateStarting,
	}

	proc, err := a.spawnSessionProcess(ctx, spec, argv, clk, sess)
	if err != nil {
		return nil, err
	}
	sess.proc = proc
	sess.normalizer = NewNormalizer(spec.SessionID)
	if spec.OnSpawned != nil {
		spec.OnSpawned(sess)
	}

	initCh := make(chan *Init, 1)
	readerErrCh := make(chan error, 1)

	supervise.Go(context.WithoutCancel(ctx), "claude.reader", func(taskCtx context.Context) error {
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
		Env:           SessionEnv(spec),
		Clock:         clk,
		Interrupt:     makeProtocolInterrupt(sess),
		SessionID:     spec.SessionID,
		XDGRuntimeDir: a.XDGRuntimeDir,
		Probe:         probe,
		OnEscalation:  a.OnEscalation,
	}

	proc, err := process.Spawn(ctx, opts)
	if err != nil {
		if errors.Is(err, process.ErrBinaryNotFound) {
			return nil, errcode.Wrap(errcode.NotFound, "spawn claude process", err)
		}
		if errors.Is(err, process.ErrNotExecutable) {
			return nil, errcode.Wrap(errcode.PermissionDenied, "spawn claude process", err)
		}
		return nil, errcode.Wrap(errcode.Internal, "spawn claude process", err)
	}
	return proc, nil
}

func makeProtocolInterrupt(sess *Session) func(context.Context) error {
	return func(ctx context.Context) error {
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
		select {
		case sess.writeSem <- struct{}{}:
			defer func() { <-sess.writeSem }()
		default:
			// Write is busy with Send/Steer; skip protocol interrupt to allow stop order to proceed.
			return nil
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
	timer := s.clock.NewTimer(DefaultHandshakeTimeout)
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
		slog.WarnContext(ctx, "claude startup cleanup: close stdin error", "session_id", s.sessionID, "error", err)
	}
	if _, err := s.proc.Stop(context.WithoutCancel(ctx)); err != nil {
		slog.WarnContext(ctx, "claude startup cleanup: stop error", "session_id", s.sessionID, "error", err)
	}
	close(s.closeNotify)
	<-s.readerDoneCh
}
