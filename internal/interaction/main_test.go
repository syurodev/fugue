package interaction

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const (
	testSessionID   = "sess-1"
	testWorkspaceID = "ws-1"
	testToolName    = "file.write"
	optionDBLocal   = "db-local"
	optionDBStage   = "db-stage"
	testEnvDev      = "dev"
	testEnvProd     = "prod"
	testEnvStg      = "stg"
	testTagUnknown  = "unknown"
	testBogus       = "bogus"
)

func startTime() time.Time {
	return time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
}

func defaultBinding() Binding {
	return Binding{
		SessionID:       testSessionID,
		WorkspaceID:     testWorkspaceID,
		InvocationID:    "inv-1",
		AttemptID:       "att-1",
		CheckGeneration: 1,
		ToolName:        testToolName,
		GrantScopeKey:   "tool:" + testToolName,
		HasTarget:       true,
		ArgsDigest:      "sha256:abc",
		TargetRevision:  "target-v1",
		EnvTag:          testEnvDev,
	}
}

func confirmSpec(opID string) *Spec {
	return &Spec{
		OperationID: opID,
		Kind:        KindConfirm,
		Summary:     "Write main.go",
		Offers:      []Outcome{OutcomeAllowedOnce, OutcomeAllowedSession},
		ExpiresAt:   time.Time{},
		Binding:     defaultBinding(),
	}
}

func selectSpec(opID string) *Spec {
	return &Spec{
		OperationID: opID,
		Kind:        KindSelect,
		Summary:     "Choose database profile",
		Options: []Option{
			{ID: optionDBLocal, Label: "Local Postgres"},
			{ID: optionDBStage, Label: "Staging replica"},
		},
		ExpiresAt: time.Time{},
		Binding:   defaultBinding(),
	}
}

// Shared test constants; goconst requires the repeated literals to be named.
const (
	vendorOptionAllow  = "allow"
	vendorOptionAlways = "always"
	vendorOptionDeny   = "deny"
	session2           = "sess-2"
	genericOptionLabel = "Option"
	relayDenyNote      = "Not while the tests run"
	// questionUnofferedID is the option ID no question offers.
	questionUnofferedID = "db-unknown"
)

func testQuestion() Question {
	return Question{
		ID:       "db",
		Question: "Which database should the tests use?",
		Header:   "Test DB",
		Options: []Option{
			{ID: optionDBLocal, Label: "Local Postgres", Description: "Same as production"},
			{ID: optionDBStage, Label: "Staging replica"},
		},
		MultiSelect: false,
	}
}

func questionSpec(opID string) *Spec {
	return &Spec{
		OperationID: opID,
		Kind:        KindQuestion,
		Summary:     "ask_user",
		Questions:   []Question{testQuestion()},
		ExpiresAt:   time.Time{},
		Binding:     defaultBinding(),
	}
}

func vendorOptions() []Option {
	return []Option{
		{ID: vendorOptionAllow, Label: "Allow once"},
		{ID: vendorOptionAlways, Label: "Always allow"},
		{ID: vendorOptionDeny, Label: "Deny"},
	}
}

func mustVendorKey(t *testing.T, turnID, vendorRequestID string) string {
	t.Helper()
	key, err := VendorOperationKey(testSessionID, turnID, vendorRequestID)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// vendorBinding is the vendor-shaped binding of a relayed request (VR-05):
// session plus turn and vendor request; no native check fields.
func vendorBinding(turnID, vendorRequestID string) Binding {
	return Binding{
		SessionID:       testSessionID,
		TurnID:          turnID,
		VendorRequestID: vendorRequestID,
	}
}

func relayedSpec(t *testing.T, turnID, vendorRequestID string) *Spec {
	t.Helper()
	return &Spec{
		OperationID: mustVendorKey(t, turnID, vendorRequestID),
		Kind:        KindConfirm,
		Summary:     "Vendor wants to run npm install",
		Relayed:     true,
		Options:     vendorOptions(),
		ExpiresAt:   time.Time{},
		Binding:     vendorBinding(turnID, vendorRequestID),
	}
}

func relayedQuestionSpec(t *testing.T, turnID, vendorRequestID string) *Spec {
	t.Helper()
	return &Spec{
		OperationID: mustVendorKey(t, turnID, vendorRequestID),
		Kind:        KindQuestion,
		Summary:     "Vendor asks before proceeding",
		Relayed:     true,
		Questions:   []Question{testQuestion()},
		Body:        "Context the vendor attached",
		ExpiresAt:   time.Time{},
		Binding:     vendorBinding(turnID, vendorRequestID),
	}
}

type recordingResponder struct {
	mu      sync.Mutex
	prompts []*Ticket
	err     error
}

func (r *recordingResponder) Prompt(ticket *Ticket) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.prompts = append(r.prompts, ticket)
	return nil
}

func (r *recordingResponder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prompts)
}

type failingGenerator struct {
	failAfter int
	calls     int
}

func (g *failingGenerator) NewID() (string, error) {
	g.calls++
	if g.calls > g.failAfter {
		return "", errcode.New(errcode.Internal, "entropy exhausted")
	}
	return fmt.Sprintf("gen-%d", g.calls), nil
}

type stubTagPolicy struct {
	mu        sync.Mutex
	revisions map[string]uint64
	confirms  map[string]bool
}

func newStubTagPolicy() *stubTagPolicy {
	return &stubTagPolicy{
		revisions: map[string]uint64{testEnvDev: 1, testEnvStg: 1, testEnvProd: 1},
		confirms:  map[string]bool{testEnvDev: false, testEnvStg: true, testEnvProd: true},
	}
}

func (p *stubTagPolicy) RequiresConfirmation(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if name == "" {
		return true
	}
	c, ok := p.confirms[name]
	if !ok {
		return true
	}
	return c
}

func (p *stubTagPolicy) PolicyRevision(name string) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.revisions[name]
}

func (p *stubTagPolicy) set(name string, requiresConfirmation bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.confirms[name] = requiresConfirmation
	p.revisions[name]++
	return nil
}

func newTestOwner(t *testing.T) (*Owner, *clock.Fake, *recordingResponder, *[]AuditEvent) {
	t.Helper()
	fakeClock := clock.NewFake(startTime())
	responder := &recordingResponder{}
	var audits []AuditEvent
	owner, err := NewOwner(&Config{
		Clock:     fakeClock,
		IDs:       ids.NewSequence("id"),
		Responder: responder,
		Headless:  false,
		TagPolicy: newStubTagPolicy(),
		Audit: func(ev AuditEvent) {
			audits = append(audits, ev)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return owner, fakeClock, responder, &audits
}

func mustEnsure(t *testing.T, owner *Owner, spec *Spec) *Ticket {
	t.Helper()
	ticket, _, err := owner.Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func mustAnswer(t *testing.T, owner *Owner, decision *Decision) *Ticket {
	t.Helper()
	ticket, err := owner.Answer(decision)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func wantCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", want)
	}
	if got := errcode.CodeOf(err); got != want {
		t.Fatalf("code = %q, want %q (%v)", got, want, err)
	}
}
