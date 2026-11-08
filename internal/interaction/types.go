package interaction

import (
	"slices"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Kind distinguishes the supported interaction request shapes.
type Kind string

// The supported interaction kinds (interaction §4.1).
const (
	KindConfirm  Kind = "confirm"
	KindSelect   Kind = "select"
	KindQuestion Kind = "question"
)

// Outcome is the terminal state of an interaction request.
type Outcome string

// The request outcomes defined by tool-system §5.1 and interaction §4.1.
const (
	OutcomeAllowedOnce    Outcome = "allowed_once"
	OutcomeAllowedSession Outcome = "allowed_session"
	OutcomeDenied         Outcome = "denied"
	OutcomeCancelled      Outcome = "cancelled"
	OutcomeUnavailable    Outcome = "unavailable"
	OutcomeExpired        Outcome = "expired"
	OutcomeAnswered       Outcome = "answered"
)

// Option is one selectable choice offered by a KindSelect request, by a
// question or by a relayed vendor request. ID and Label are shown verbatim;
// Description is an optional longer explanation (ask-plan §4).
type Option struct {
	ID          string
	Label       string
	Description string
}

// Question is one question of a KindQuestion request (ask-plan §4). The
// question text is model-authored content and must be shown to the user
// labelled as the agent's question (interaction §7).
type Question struct {
	ID          string
	Question    string
	Header      string
	Options     []Option
	MultiSelect bool
}

// Answer is the user's reply to one Question of a KindQuestion request. A
// reply carries options from the offered set, free text, or both.
type Answer struct {
	QuestionID string
	OptionIDs  []string
	Text       string
}

// Binding ties a request and its resulting receipt to the exact execution
// context and revisions observed during Prepare. Any change in these fields
// invalidates the receipt at ClaimReceipt or ClaimSelection. A native request
// fills the check fields and leaves the vendor fields empty; a relayed vendor
// request fills SessionID, TurnID and VendorRequestID and leaves the check
// fields empty (vendor-runtime VR-05).
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
	// TurnID and VendorRequestID identify the vendor turn and the vendor's
	// own request inside it; they exist only on relayed bindings.
	TurnID          string
	VendorRequestID string
}

// Spec describes an interaction to ensure for an operation ID.
type Spec struct {
	OperationID string
	Kind        Kind
	// Summary is the content shown to the user, built by the tool from
	// canonical args or relayed verbatim from the vendor's prompt.
	Summary string
	// Relayed marks a request relayed from a vendor runtime (vendor-runtime
	// VR-05): Options are the vendor's own choices shown verbatim, the owner
	// never creates a grant or an execution receipt for the request, and the
	// answer is delivered back to the caller once.
	Relayed bool
	// Offers is the set of outcomes offered to the user of a non-relayed
	// confirm. Requests that must ask every time never offer
	// OutcomeAllowedSession (interaction §3.2 step 1, I-03).
	Offers    []Outcome
	Options   []Option
	Questions []Question
	// Body is optional text shown alongside the questions of a
	// KindQuestion request, such as a plan to approve (ask-plan §3.2).
	Body      string
	ExpiresAt time.Time
	Binding   Binding
}

// Ticket is the immutable snapshot of an interaction request returned to
// callers and delivered to the Responder. It carries request content
// (interaction §7): it lives in owner memory and goes only to trusted
// principals, never into audit, logs or errors.
type Ticket struct {
	RequestID   string
	OperationID string
	Incarnation string
	Kind        Kind
	Summary     string
	Relayed     bool
	// Offers is the effective set of outcomes the user may answer with.
	Offers           []Outcome
	Options          []Option
	Questions        []Question
	Body             string
	Binding          Binding
	CreatedAt        time.Time
	ExpiresAt        time.Time
	Resolved         bool
	ResolvedAt       time.Time
	Outcome          Outcome
	SelectedOptionID string
	// Text is the user's note delivered with the decision: the deny note of
	// a confirm or the optional message of a relayed answer.
	Text      string
	Answers   []Answer
	ReceiptID string
}

// Decision is a user's answer to a pending interaction request. Text carries
// the deny note of a confirm or the optional message of a relayed answer;
// Answers carries the per-question replies of a KindQuestion request.
type Decision struct {
	Incarnation string
	RequestID   string
	Outcome     Outcome
	OptionID    string
	Text        string
	Answers     []Answer
}

// Receipt is a one-use capability issued when a request is approved or when a
// selection is completed. A confirmation receipt authorizes one execution
// attempt matching its Binding; a selection receipt records the chosen option
// for Prepare; a question receipt carries the answers to Execute. No receipt
// is ever issued for a relayed request. Receipts are removed from memory when
// claimed or invalidated.
type Receipt struct {
	ReceiptID        string
	RequestID        string
	OperationID      string
	Incarnation      string
	Kind             Kind
	SelectedOptionID string
	Answers          []Answer
	Binding          Binding
	IssuedAt         time.Time
	ExpiresAt        time.Time
	Claimed          bool
	ClaimedAt        time.Time
}

// AuditEvent is a metadata-only record emitted for every request lifecycle
// transition and receipt claim. It never contains tool arguments, summaries,
// prompts, answers or notes (interaction §4.3, §7).
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
// decision. A nil Responder does not fail the request: it stays pending until
// a client picks it up (interaction §5, I-07). Only a host configured
// Headless resolves requests as OutcomeUnavailable.
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
	if err := checkTextLimit(spec.Summary, MaxSummaryBytes, "summary"); err != nil {
		return err
	}
	if spec.Relayed {
		return validateRelayedSpec(spec)
	}
	// Native binding: the check fields are required and the vendor fields empty.
	if err := validateBinding(&spec.Binding); err != nil {
		return err
	}
	// Native operation IDs keep the legacy F1.5 freedom: any nonempty ID is
	// accepted, with NativeOperationKey as the recommended constructor.
	switch spec.Kind {
	case KindConfirm:
		return validateConfirmSpec(spec)
	case KindSelect:
		return validateSelectSpec(spec)
	case KindQuestion:
		return validateQuestionKindSpec(spec)
	}
	return errcode.New(errcode.InvalidArgument, "interaction kind is invalid")
}

func validateSelectSpec(spec *Spec) error {
	if len(spec.Offers) > 0 {
		return errcode.New(errcode.InvalidArgument, "select interaction must not offer confirm outcomes")
	}
	return validateOptionList(spec.Options, 1, "selection")
}

func validateQuestionKindSpec(spec *Spec) error {
	if len(spec.Offers) > 0 || len(spec.Options) > 0 {
		return errcode.New(errcode.InvalidArgument, "question interaction must not carry confirm offers or top-level options")
	}
	return validateQuestionSpec(spec)
}

func validateBinding(b *Binding) error {
	if b.TurnID != "" || b.VendorRequestID != "" {
		return errcode.New(errcode.InvalidArgument, "native binding must not carry vendor fields")
	}
	if b.SessionID == "" || b.InvocationID == "" || b.AttemptID == "" || b.ToolName == "" || b.CheckGeneration == 0 {
		return errcode.New(errcode.InvalidArgument, "interaction binding is incomplete")
	}
	return nil
}

// validateRelayedBinding validates the vendor binding shape (vendor-runtime
// VR-05): the session plus the vendor turn and request IDs. The native check
// fields are absent because the vendor runtime owns the execution context.
func validateRelayedBinding(b *Binding) error {
	if b.InvocationID != "" || b.AttemptID != "" || b.CheckGeneration != 0 || b.ToolName != "" || b.ArgsDigest != "" {
		return errcode.New(errcode.InvalidArgument, "relayed binding must not carry native fields")
	}
	if b.SessionID == "" || b.TurnID == "" || b.VendorRequestID == "" {
		return errcode.New(errcode.InvalidArgument, "relayed binding requires session, turn and vendor request IDs")
	}
	return nil
}

func validateConfirmSpec(spec *Spec) error {
	if len(spec.Questions) > 0 || spec.Body != "" {
		return errcode.New(errcode.InvalidArgument, "confirm interaction must not carry question content")
	}
	if len(spec.Options) > 0 {
		return errcode.New(errcode.InvalidArgument, "confirm interaction must not carry selection options")
	}
	if len(spec.Offers) == 0 {
		return errcode.New(errcode.InvalidArgument, "confirm interaction requires at least one offered outcome")
	}
	seen := make(map[Outcome]struct{}, len(spec.Offers))
	for _, outcome := range spec.Offers {
		switch outcome {
		case OutcomeAllowedOnce, OutcomeAllowedSession:
		case OutcomeDenied, OutcomeCancelled, OutcomeUnavailable, OutcomeExpired, OutcomeAnswered:
			return errcode.New(errcode.InvalidArgument, "confirm offers only approval outcomes")
		}
		if _, dup := seen[outcome]; dup {
			return errcode.New(errcode.InvalidArgument, "confirm offers must be distinct")
		}
		seen[outcome] = struct{}{}
	}
	if slices.Contains(spec.Offers, OutcomeAllowedSession) && requiresEveryTime(&spec.Binding) {
		return errcode.New(errcode.InvalidArgument, "requests that always ask never offer a session grant")
	}
	return nil
}

// validateRelayedSpec validates a request relayed from a vendor runtime
// (vendor-runtime VR-05): no Fugue rule applies to the request, so Offers are
// rejected and the operation ID must be built by VendorOperationKey from the
// binding. A relayed confirm carries the vendor's summary and options; a
// relayed question carries the vendor's Questions (and optional Body)
// verbatim.
func validateRelayedSpec(spec *Spec) error {
	if spec.Kind != KindConfirm && spec.Kind != KindQuestion {
		return errcode.New(errcode.InvalidArgument, "relayed requests are confirm or question interactions")
	}
	if len(spec.Offers) > 0 {
		return errcode.New(errcode.InvalidArgument, "relayed requests never offer Fugue outcomes")
	}
	if err := validateRelayedKey(spec); err != nil {
		return err
	}
	switch spec.Kind {
	case KindConfirm:
		if len(spec.Questions) > 0 || spec.Body != "" {
			return errcode.New(errcode.InvalidArgument, "relayed confirm carries only the vendor summary and options")
		}
		return validateOptionList(spec.Options, 1, "relayed")
	case KindQuestion:
		if len(spec.Options) > 0 {
			return errcode.New(errcode.InvalidArgument, "relayed question carries questions, not top-level options")
		}
		return validateQuestionSpec(spec)
	case KindSelect:
		return errcode.New(errcode.InvalidArgument, "relayed requests are confirm or question interactions")
	}
	return errcode.New(errcode.InvalidArgument, "relayed requests are confirm or question interactions")
}

// validateRelayedKey checks the vendor binding shape and requires the
// operation ID to be built by VendorOperationKey from it.
func validateRelayedKey(spec *Spec) error {
	if err := validateRelayedBinding(&spec.Binding); err != nil {
		return err
	}
	key, err := VendorOperationKey(spec.Binding.SessionID, spec.Binding.TurnID, spec.Binding.VendorRequestID)
	if err != nil {
		return err
	}
	if spec.OperationID != key {
		return errcode.New(errcode.InvalidArgument, "relayed operation ID must be built by VendorOperationKey from its binding")
	}
	return nil
}

func validateQuestionSpec(spec *Spec) error {
	if len(spec.Questions) == 0 {
		return errcode.New(errcode.InvalidArgument, "question interaction requires at least one question")
	}
	if err := checkTextLimit(spec.Body, MaxPromptBytes, "question body"); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(spec.Questions))
	for i := range spec.Questions {
		q := &spec.Questions[i]
		if q.ID == "" || q.Question == "" {
			return errcode.New(errcode.InvalidArgument, "question requires both ID and text")
		}
		if _, dup := seen[q.ID]; dup {
			return errcode.New(errcode.InvalidArgument, "question IDs must be distinct")
		}
		seen[q.ID] = struct{}{}
		if err := checkTextLimit(q.Question, MaxPromptBytes, "question text"); err != nil {
			return err
		}
		if err := checkTextLimit(q.Header, MaxPromptBytes, "question header"); err != nil {
			return err
		}
		if err := validateOptionList(q.Options, 0, "question"); err != nil {
			return err
		}
	}
	return nil
}

func validateOptionList(options []Option, minCount int, what string) error {
	if len(options) < minCount {
		return errcode.New(errcode.InvalidArgument, what+" interaction requires at least one option")
	}
	if len(options) > MaxOptions {
		return errcode.New(errcode.InvalidArgument, what+" interaction exceeds the option limit")
	}
	seen := make(map[string]struct{}, len(options))
	for _, opt := range options {
		if opt.ID == "" || opt.Label == "" {
			return errcode.New(errcode.InvalidArgument, what+" option requires both ID and label")
		}
		if _, dup := seen[opt.ID]; dup {
			return errcode.New(errcode.InvalidArgument, what+" options must have distinct IDs")
		}
		seen[opt.ID] = struct{}{}
	}
	return nil
}

// requiresEveryTime reports whether the request must always ask the user and
// may never offer a session grant (interaction §3.2 step 1 and AGENTS.md:
// prod and unknown environment tags always ask and ignore session grants).
func requiresEveryTime(b *Binding) bool {
	switch b.EnvTag {
	case "", "dev", "local", "test":
		return false
	default:
		return true
	}
}

// knownOutcome reports whether outcome is one of the defined outcomes;
// decisions with an unknown value are rejected before any switch on it.
func knownOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeAllowedOnce, OutcomeAllowedSession, OutcomeDenied,
		OutcomeCancelled, OutcomeUnavailable, OutcomeExpired, OutcomeAnswered:
		return true
	}
	return false
}

// effectiveOffers returns the outcomes a user may answer a request with. A
// relayed request never offers a Fugue approval outcome (VR-05): choosing one
// of the vendor's options — including the vendor's own reject or "always
// allow" — records the choice as answered, and denied stays the Fugue-level
// deny with an optional note. No receipt or grant is ever created.
func effectiveOffers(spec *Spec) []Outcome {
	if spec.Relayed {
		return []Outcome{OutcomeAnswered, OutcomeDenied}
	}
	return spec.Offers
}

func sameSpec(t *Ticket, spec *Spec) bool {
	if t.Kind != spec.Kind || t.Binding != spec.Binding || t.Relayed != spec.Relayed ||
		t.Summary != spec.Summary || t.Body != spec.Body {
		return false
	}
	if !slices.Equal(t.Offers, effectiveOffers(spec)) || !slices.Equal(t.Options, spec.Options) {
		return false
	}
	return sameQuestions(t.Questions, spec.Questions)
}

func sameQuestions(a, b []Question) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Question != b[i].Question || a[i].Header != b[i].Header || a[i].MultiSelect != b[i].MultiSelect {
			return false
		}
		if !slices.Equal(a[i].Options, b[i].Options) {
			return false
		}
	}
	return true
}

func hasOption(options []Option, id string) bool {
	return slices.ContainsFunc(options, func(opt Option) bool {
		return opt.ID == id
	})
}

func cloneTicket(t *Ticket) *Ticket {
	cp := *t
	cp.Offers = slices.Clone(t.Offers)
	cp.Options = slices.Clone(t.Options)
	cp.Questions = cloneQuestions(t.Questions)
	cp.Answers = cloneAnswers(t.Answers)
	return &cp
}

func cloneQuestions(questions []Question) []Question {
	if questions == nil {
		return nil
	}
	out := make([]Question, len(questions))
	for i := range questions {
		out[i] = questions[i]
		out[i].Options = slices.Clone(questions[i].Options)
	}
	return out
}

func cloneAnswers(answers []Answer) []Answer {
	if answers == nil {
		return nil
	}
	out := make([]Answer, len(answers))
	for i := range answers {
		out[i] = answers[i]
		out[i].OptionIDs = slices.Clone(answers[i].OptionIDs)
	}
	return out
}
