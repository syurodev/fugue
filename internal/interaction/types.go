package interaction

import (
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Kind distinguishes an approval confirmation from an option selection.
type Kind string

// The supported interaction kinds.
const (
	KindConfirm Kind = "confirm"
	KindSelect  Kind = "select"
)

// Outcome is the terminal state of an interaction request.
type Outcome string

// The six request outcomes defined by tool-system §5.1.
const (
	OutcomeAllowedOnce    Outcome = "allowed_once"
	OutcomeAllowedSession Outcome = "allowed_session"
	OutcomeDenied         Outcome = "denied"
	OutcomeCancelled      Outcome = "cancelled"
	OutcomeUnavailable    Outcome = "unavailable"
	OutcomeExpired        Outcome = "expired"
)

// Option is one selectable choice offered by a KindSelect request.
type Option struct {
	ID    string
	Label string
}

// Binding ties a request and its resulting receipt to the exact execution
// context and revisions observed during Prepare. Any change in these fields
// invalidates the receipt at ClaimReceipt or ClaimSelection.
type Binding struct {
	SessionID       string
	WorkspaceID     string
	InvocationID    string
	AttemptID       string
	CheckGeneration uint64
	ToolName        string
	ArgsDigest      string
	TargetRevision  string
	EnvRevision     string
	EnvTag          string
}

// Spec describes an interaction to ensure for an operation ID.
type Spec struct {
	OperationID string
	Kind        Kind
	Summary     string
	Important   bool
	Options     []Option
	ExpiresAt   time.Time
	Binding     Binding
}

// Ticket is the immutable snapshot of an interaction request returned to
// callers and delivered to the Responder. It never carries raw tool arguments
// or secrets.
type Ticket struct {
	RequestID           string
	OperationID         string
	Incarnation         string
	Kind                Kind
	Summary             string
	AllowSessionOffered bool
	Options             []Option
	Binding             Binding
	CreatedAt           time.Time
	ExpiresAt           time.Time
	Resolved            bool
	ResolvedAt          time.Time
	Outcome             Outcome
	SelectedOptionID    string
	ReceiptID           string
}

// Decision is a user's answer to a pending interaction request.
type Decision struct {
	Incarnation string
	RequestID   string
	Outcome     Outcome
	OptionID    string
}

// Receipt is a one-use capability issued when a request is approved or when a
// selection is completed. A confirmation receipt authorizes one execution
// attempt matching its Binding; a selection receipt records the chosen option
// for Prepare and never authorizes execution.
type Receipt struct {
	ReceiptID        string
	RequestID        string
	OperationID      string
	Incarnation      string
	Kind             Kind
	SelectedOptionID string
	Binding          Binding
	IssuedAt         time.Time
	ExpiresAt        time.Time
	Claimed          bool
	ClaimedAt        time.Time
}

// AuditEvent is a metadata-only record emitted for every request lifecycle
// transition and receipt claim. It never contains tool arguments or secrets.
type AuditEvent struct {
	Action      string
	Incarnation string
	OperationID string
	RequestID   string
	ReceiptID   string
	SessionID   string
	ToolName    string
	Kind        Kind
	Outcome     Outcome
	OccurredAt  time.Time
}

// Responder notifies a trusted human interface that a pending Ticket awaits a
// decision. A nil Responder means the host has no trusted UI responder and must
// fail closed with OutcomeUnavailable.
type Responder interface {
	Prompt(ticket *Ticket) error
}

// NopResponder is a Responder that accepts prompt notifications without
// side effects, for tests and hosts that drive answers through Pending and Answer.
type NopResponder struct{}

// Prompt implements Responder.
func (NopResponder) Prompt(*Ticket) error { return nil }

func validateSpec(spec *Spec) error {
	if spec == nil {
		return errcode.New(errcode.InvalidArgument, "interaction spec is required")
	}
	if spec.OperationID == "" {
		return errcode.New(errcode.InvalidArgument, "interaction operation ID is required")
	}
	if err := validateBinding(&spec.Binding); err != nil {
		return err
	}
	switch spec.Kind {
	case KindConfirm:
		if len(spec.Options) > 0 {
			return errcode.New(errcode.InvalidArgument, "confirm interaction must not carry selection options")
		}
		return nil
	case KindSelect:
		return validateOptions(spec.Options)
	default:
		return errcode.New(errcode.InvalidArgument, "interaction kind is invalid")
	}
}

func validateBinding(b *Binding) error {
	if b.SessionID == "" || b.InvocationID == "" || b.AttemptID == "" || b.ToolName == "" || b.CheckGeneration == 0 {
		return errcode.New(errcode.InvalidArgument, "interaction binding is incomplete")
	}
	return nil
}

func validateOptions(options []Option) error {
	if len(options) == 0 {
		return errcode.New(errcode.InvalidArgument, "selection interaction requires at least one option")
	}
	seen := make(map[string]struct{}, len(options))
	for _, opt := range options {
		if opt.ID == "" || opt.Label == "" {
			return errcode.New(errcode.InvalidArgument, "selection option requires both ID and label")
		}
		if _, dup := seen[opt.ID]; dup {
			return errcode.New(errcode.InvalidArgument, "selection options must have distinct IDs")
		}
		seen[opt.ID] = struct{}{}
	}
	return nil
}

// requiresEveryTime reports whether the request must always ask the user and
// may only offer a single-use approval (tool-system §5.3 and AGENTS.md:
// prod and unknown environment tags always ask and ignore session grants).
func requiresEveryTime(spec *Spec) bool {
	if spec.Important {
		return true
	}
	switch spec.Binding.EnvTag {
	case "", "dev", "local", "test":
		return false
	default:
		return true
	}
}

func sameSpec(t *Ticket, spec *Spec) bool {
	if t.Kind != spec.Kind || t.Binding != spec.Binding || len(t.Options) != len(spec.Options) {
		return false
	}
	for i := range t.Options {
		if t.Options[i] != spec.Options[i] {
			return false
		}
	}
	return true
}

func cloneTicket(t *Ticket) *Ticket {
	cp := *t
	if len(t.Options) > 0 {
		cp.Options = append([]Option(nil), t.Options...)
	}
	return &cp
}
