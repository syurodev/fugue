package vendor

import (
	"context"
	"strings"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/process"
)

// LoginState is what a probe can say about the vendor login without reading
// vendor credential files (VR-03): a CLI that reports nothing says unknown.
type LoginState string

// Login states. A probe never guesses: logged in or out only when the CLI
// itself reports it.
const (
	LoginUnknown LoginState = "unknown"
	LoginActive  LoginState = "logged_in"
	LoginMissing LoginState = "logged_out"
)

// Capability is the adapter's declared contract (vendor-runtime §6). UI and
// policy may rely only on what is declared here; the declaration is checked by
// conformance against the pinned version (VR-07).
type Capability struct {
	// PerRequestApproval: the adapter relays each vendor permission request.
	// Claude gains it with the MCP prompt tool in F9.6.
	PerRequestApproval bool
	// BlockAllToolCalls: the strict profile can block every tool call (stage B).
	BlockAllToolCalls bool
	// Interrupt stops the running turn.
	Interrupt bool
	// Steer sends a message while the turn runs.
	Steer bool
	// Resume continues a recorded session.
	Resume bool
	// PartialDeltas streams content block deltas.
	PartialDeltas bool
	// VendorSlashCommands lists the vendor's own slash commands.
	VendorSlashCommands bool
	// DirectQuota: the vendor reports quota windows directly.
	DirectQuota bool
	// ModelList: the vendor lists selectable models.
	ModelList bool
}

// Probe reports what a probe learned: the CLI version, whether it lies inside
// the adapter's tested range (VR-08), the login state and the capability. Path
// is the absolute path of the binary the probe ran, so the session spawn runs
// the very file that was probed instead of resolving the name again.
type Probe struct {
	Path           string
	Version        string
	VersionTested  bool
	VersionWarning string
	Login          LoginState
	Capability     Capability
}

// AccountHome names the vendor account to run: the vendor's default home when
// ConfigDir is empty, or a separate config directory (CLAUDE_CONFIG_DIR,
// CODEX_HOME) for an additional account (vendor-runtime §8).
type AccountHome struct {
	ConfigDir string
}

// StartSpec carries what Start needs to spawn one session process. F9.5b adds
// the process-side fields (resume handle, profile options) when it implements
// Session on internal/process.
type StartSpec struct {
	SessionID string
	Cwd       string
	Home      AccountHome
	// HostEnv is the caller's environment snapshot; Start filters it through
	// the §8 allowlist, so no Fugue secret reaches the vendor process.
	HostEnv         []string
	Model           string
	Effort          string
	ResumeSessionID string
	BinaryPath      string
	XDGRuntimeDir   string
	Probe           *process.SystemdProbe
	Clock           clock.Clock
	IDs             ids.Generator
	OnEscalation    func(step string)
}

// Input is one user input: a message text, or a slash command as the whole
// prompt when it starts with "/" (vendor-runtime §9).
type Input struct {
	Text string
}

// TurnRef identifies a running turn of one session.
type TurnRef struct {
	ID string
}

// ApprovalRef identifies one relayed vendor approval request (F9.6).
type ApprovalRef struct {
	ID string
}

// Decision is the user's answer to a relayed approval request: allow or deny,
// with an optional message the vendor receives verbatim (VR-05).
type Decision struct {
	Allow   bool
	Message string
}

// CloseReason says why a session is being closed.
type CloseReason string

// Close reasons.
const (
	CloseUserRequest CloseReason = "user_request"
	CloseHostStop    CloseReason = "host_stop"
)

// Adapter is the port every vendor CLI implements (vendor-runtime §6). The
// adapter never writes storage; it reports to the session actor, the only
// writer.
type Adapter interface {
	// Probe runs the vendor's own version probe: version, tested range verdict,
	// login state and capability. It never reads vendor credential files.
	Probe(ctx context.Context, home AccountHome) (Probe, error)
	// Start spawns one session process and returns its Session.
	Start(ctx context.Context, spec *StartSpec) (Session, error)
}

// Session is one running vendor session process (vendor-runtime §6).
type Session interface {
	// Send delivers one user message or slash command and returns the turn.
	Send(ctx context.Context, in Input) (TurnRef, error)
	// Interrupt stops the given turn.
	Interrupt(ctx context.Context, turn TurnRef) error
	// Steer sends a message while the turn runs; adapters without the
	// capability return ErrUnsupported.
	Steer(ctx context.Context, turn TurnRef, in Input) error
	// Resolve answers a relayed approval request (F9.6); before the approval
	// bridge exists it returns ErrUnsupported and the session fails closed on
	// the vendor's own default.
	Resolve(ctx context.Context, req ApprovalRef, d Decision) error
	// Close stops the session process tree and returns its stop proof.
	Close(ctx context.Context, reason CloseReason) (process.StopProof, error)
}

// ErrUnsupported reports an operation the adapter's capability contract does
// not offer (for example Resolve before F9.6).
var ErrUnsupported = errcode.New(errcode.FailedPrecondition, "the vendor adapter does not support this operation")

// ErrFugueCommand reports an input that belongs to Fugue's own /fugue:
// command namespace: those commands run on the host and never reach a vendor
// (vendor-runtime §9).
var ErrFugueCommand = errcode.New(errcode.InvalidArgument, "the /fugue: command namespace never reaches a vendor")

// ErrBusy reports that a turn is already running in this session: concurrent
// turns are rejected and never queued implicitly (vendor-runtime §6).
var ErrBusy = errcode.New(errcode.FailedPrecondition, "vendor session has an active turn in progress")

// ErrSessionClosed reports that the vendor session process has been closed.
var ErrSessionClosed = errcode.New(errcode.FailedPrecondition, "vendor session is closed")

// ErrTurnNotRunning reports an operation on a turn that is not currently running.
var ErrTurnNotRunning = errcode.New(errcode.FailedPrecondition, "turn is not currently active")

// ErrHandshakeTimeout reports that the vendor session failed to receive system/init within 30 s.
var ErrHandshakeTimeout = errcode.New(errcode.DeadlineExceeded, "vendor session handshake timed out waiting for system/init")

// ValidateInput enforces the input rules shared by every adapter: the text is
// non-empty, and a prompt beginning with /fugue: is rejected with a typed
// error because Fugue commands run on the host (vendor-runtime §9). Every
// Session.Send and Steer calls it before anything reaches a vendor process.
func ValidateInput(in Input) error {
	if strings.HasPrefix(in.Text, "/fugue:") {
		return ErrFugueCommand
	}
	if strings.TrimSpace(in.Text) == "" {
		return errcode.New(errcode.InvalidArgument, "vendor input text is empty")
	}
	return nil
}
