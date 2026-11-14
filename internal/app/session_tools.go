package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// SessionToolMaxBytes bounds one session tool RPC request on the wire.
const SessionToolMaxBytes = 256 << 10

// FugueTool is one MCP tool Fugue serves to vendor sessions through
// `fugue mcp-bridge`. Every call passes the interaction policy before Run
// (vendor-runtime VR-13). Stage A registers none in production: the team
// tools arrive with F11.2, and tests register theirs through
// HostDeps.TestFugueTools.
type FugueTool struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema of the arguments, shown to the vendor.
	InputSchema json.RawMessage
	// Roles lists the session roles that may see and call the tool; empty
	// means every role.
	Roles      []string
	Descriptor interaction.Descriptor
	// Plan derives the policy facts of one call from its arguments. It is
	// pure and runs before any effect.
	Plan func(args json.RawMessage) (FugueToolPlan, error)
	// Run executes the call after the policy allowed it.
	Run func(ctx context.Context, sessionID string, args json.RawMessage) (string, error)
}

// FugueToolPlan is what Decide and the confirmation prompt need to know about
// one call.
type FugueToolPlan struct {
	Call            interaction.Call
	Summary         string
	GrantScopeLabel string
}

// FugueToolView is the tools/list shape of one tool.
type FugueToolView struct {
	Name, Description string
	InputSchema       []byte
}

// FugueToolResult is a tool's answer to the vendor. It is data for the
// vendor model, never an instruction to Fugue.
type FugueToolResult struct {
	Text    string
	IsError bool
}

// NewReadOnlyTool builds a tool that only reads: it never asks and runs in
// every mode. Tests register it through HostDeps.TestFugueTools.
func NewReadOnlyTool(name, description string, schema json.RawMessage, run func(ctx context.Context, sessionID string, args json.RawMessage) (string, error)) *FugueTool {
	return &FugueTool{
		Name: name, Description: description, InputSchema: schema,
		Descriptor: interaction.Descriptor{Risk: interaction.RiskRead, Confirm: interaction.ConfirmNever},
		Plan:       func(json.RawMessage) (FugueToolPlan, error) { return FugueToolPlan{}, nil },
		Run:        run,
	}
}

type sessionRecords interface {
	Snapshot(ctx context.Context, sessionID string) (vendorstore.Session, error)
}

// SessionToolService is the Host API boundary of the session principal
// (host-api §7, HA-06): it turns a session token into a binding and serves
// that binding the Fugue tools of its session and role, and nothing else.
type SessionToolService struct {
	tokens       *sessionTokens
	tools        map[string]*FugueTool
	sessions     sessionRecords
	interactions *InteractionService
	tags         interaction.TagPolicy
	ids          ids.Generator
}

func newSessionToolService(tokens *sessionTokens, tools []*FugueTool, sessions sessionRecords,
	interactions *InteractionService, tags interaction.TagPolicy, gen ids.Generator,
) *SessionToolService {
	byName := make(map[string]*FugueTool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	return &SessionToolService{tokens: tokens, tools: byName, sessions: sessions, interactions: interactions, tags: tags, ids: gen}
}

// Attach verifies a presented token. The error never says why it failed.
func (s *SessionToolService) Attach(token string) (SessionBinding, error) {
	binding, ok := s.tokens.verify(token)
	if !ok {
		return SessionBinding{}, errcode.New(errcode.PermissionDenied, "the session token is not valid")
	}
	return binding, nil
}

// List returns the tools of the binding's role, by name.
func (s *SessionToolService) List(binding SessionBinding) ([]FugueToolView, error) {
	if !s.tokens.live(binding) {
		return nil, errSessionEnded()
	}
	out := make([]FugueToolView, 0, len(s.tools))
	for _, tool := range s.tools {
		if servesRole(tool, binding.Role) {
			out = append(out, FugueToolView{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
		}
	}
	slices.SortFunc(out, func(a, b FugueToolView) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Call runs one tool call of the binding's session through Decide: the
// session's approval mode, the env-tag hard rule (I-03), grants and the
// tool's own facts decide whether it runs, asks the user or is refused.
func (s *SessionToolService) Call(ctx context.Context, binding SessionBinding, name string, args []byte) (FugueToolResult, error) {
	if !s.tokens.live(binding) {
		return FugueToolResult{}, errSessionEnded()
	}
	tool, ok := s.tools[name]
	if !ok || !servesRole(tool, binding.Role) {
		return FugueToolResult{}, errcode.New(errcode.NotFound, "the tool is not available to this session")
	}
	if len(args) == 0 {
		args = []byte("{}")
	}
	plan, err := planCall(tool, args)
	if err != nil {
		return FugueToolResult{}, err
	}
	check, mode, err := s.bind(ctx, binding, tool, &plan, args)
	if err != nil {
		return FugueToolResult{}, err
	}
	verdict, err := interaction.Decide(mode, tool.Descriptor, plan.Call, s.tags, s.interactions.owner.GrantLookupFor(check))
	if err != nil {
		return FugueToolResult{}, errcode.New(errcode.Internal, "the tool policy could not be evaluated")
	}
	stopped, run, err := s.admit(ctx, check, tool, &plan, &verdict)
	if err != nil || !run {
		return stopped, err
	}
	return runTool(ctx, tool, binding.SessionID, args), nil
}

// admit applies the verdict: run reports whether the tool may run; when it
// may not, the result is what the vendor's model reads instead.
func (s *SessionToolService) admit(ctx context.Context, check *interaction.Binding, tool *FugueTool, plan *FugueToolPlan, verdict *interaction.Verdict,
) (result FugueToolResult, run bool, err error) {
	switch verdict.Kind {
	case interaction.VerdictRefuse:
		return FugueToolResult{Text: "refused by the session policy: " + verdict.Reason, IsError: true}, false, nil
	case interaction.VerdictAsk:
		allowed, askErr := s.confirm(ctx, check, tool, plan, verdict.Offers)
		if askErr != nil {
			return FugueToolResult{}, false, askErr
		}
		if !allowed {
			return FugueToolResult{Text: "the user did not allow this call", IsError: true}, false, nil
		}
	case interaction.VerdictRun:
	}
	return FugueToolResult{}, true, nil
}

func planCall(tool *FugueTool, args []byte) (FugueToolPlan, error) {
	if !json.Valid(args) {
		return FugueToolPlan{}, errcode.New(errcode.InvalidArgument, "the tool arguments are not valid JSON")
	}
	plan, err := tool.Plan(args)
	if err != nil {
		// A Plan error may quote the arguments; the client gets a fixed text.
		return FugueToolPlan{}, errcode.New(errcode.InvalidArgument, "the tool arguments are not valid")
	}
	return plan, nil
}

func runTool(ctx context.Context, tool *FugueTool, sessionID string, args []byte) FugueToolResult {
	text, err := tool.Run(ctx, sessionID, args)
	if err != nil {
		_, msg := errcode.Expose(err)
		return FugueToolResult{Text: msg, IsError: true}
	}
	return FugueToolResult{Text: text}
}

// bind reads the session's approval mode once, at the start of the check
// (interaction §3.2), and builds the binding the confirmation and its
// receipt are tied to. Each call is its own invocation.
func (s *SessionToolService) bind(ctx context.Context, binding SessionBinding, tool *FugueTool, plan *FugueToolPlan, args []byte,
) (*interaction.Binding, interaction.ApprovalMode, error) {
	info, err := s.sessions.Snapshot(ctx, binding.SessionID)
	if err != nil || info.State == vendorstore.StateClosed {
		return nil, "", errSessionEnded()
	}
	invocation, err := s.ids.NewID()
	if err != nil {
		return nil, "", errcode.Wrap(errcode.Internal, "allocate a tool invocation identity", err)
	}
	digest := sha256.Sum256(args)
	return &interaction.Binding{
		SessionID: binding.SessionID, WorkspaceID: info.WorkspaceID,
		InvocationID: invocation, AttemptID: invocation, CheckGeneration: 1,
		ToolName: tool.Name, GrantScopeKey: plan.Call.GrantScopeKey,
		HasTarget: plan.Call.HasTarget, EnvTag: plan.Call.EnvTag, ArgsDigest: hex.EncodeToString(digest[:]),
	}, interaction.ApprovalMode(info.ApprovalMode), nil
}

// confirm asks the user and waits for the answer, however long no client is
// connected (I-07). An approval is a one-use receipt bound to this exact
// call; it is claimed here, at the execution boundary.
func (s *SessionToolService) confirm(ctx context.Context, check *interaction.Binding, tool *FugueTool, plan *FugueToolPlan, offers []interaction.Outcome) (bool, error) {
	operation, err := interaction.NativeOperationKey(check.InvocationID, check.AttemptID, check.CheckGeneration)
	if err != nil {
		return false, errcode.Wrap(errcode.Internal, "key the tool confirmation", err)
	}
	if check.GrantScopeKey == "" {
		offers = []interaction.Outcome{interaction.OutcomeAllowedOnce}
	}
	// The grant scope is shown only when a session grant is on offer.
	label := plan.GrantScopeLabel
	if !slices.Contains(offers, interaction.OutcomeAllowedSession) {
		label = ""
	}
	risk := tool.Descriptor.Risk
	if tool.Descriptor.Confirm == interaction.ConfirmCustom {
		risk = plan.Call.EffectiveRisk
	}
	ticket, err := s.interactions.Ensure(&interaction.Spec{
		OperationID: operation, Kind: interaction.KindConfirm, Summary: plan.Summary, Risk: string(risk),
		GrantScopeLabel: label, Offers: offers, Binding: *check,
	})
	if err != nil {
		return false, err
	}
	if !ticket.Resolved {
		if ticket, err = s.interactions.AwaitResolved(ctx, ticket.RequestID); err != nil {
			// The caller is gone: the prompt must not outlive the call.
			if _, cancelErr := s.interactions.owner.InvalidateOperation(operation); cancelErr != nil {
				return false, errcode.Wrap(errcode.Internal, "cancel the abandoned tool confirmation", cancelErr)
			}
			return false, err
		}
	}
	if ticket.Outcome != interaction.OutcomeAllowedOnce && ticket.Outcome != interaction.OutcomeAllowedSession {
		return false, nil
	}
	if _, err := s.interactions.owner.ClaimReceipt(ticket.ReceiptID, check); err != nil {
		return false, errcode.New(errcode.FailedPrecondition, "the approval could not be used for this call")
	}
	return true, nil
}

func servesRole(tool *FugueTool, role string) bool {
	return len(tool.Roles) == 0 || slices.Contains(tool.Roles, role)
}

func errSessionEnded() error {
	return errcode.New(errcode.PermissionDenied, "the session token is no longer valid")
}
