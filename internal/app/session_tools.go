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
const SessionToolMaxBytes = 512 << 10

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
	Roles []string
	// Available reports whether the tool is offered to this session at all
	// (F11.2a: [session].teams gates fugue_team_propose). A nil Available
	// offers the tool to every session that passes the role check.
	Available  func(ctx context.Context, info vendorstore.Session) bool
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
	recorder     approvalRecorder
	tombstones   *approveTombstones
}

func newSessionToolService(tokens *sessionTokens, tools []*FugueTool, sessions sessionRecords,
	interactions *InteractionService, tags interaction.TagPolicy, gen ids.Generator, recorder approvalRecorder,
) *SessionToolService {
	byName := make(map[string]*FugueTool, len(tools))
	for _, tool := range tools {
		if tool.Name == approveToolName {
			continue
		}
		byName[tool.Name] = tool
	}
	return &SessionToolService{
		tokens: tokens, tools: byName, sessions: sessions, interactions: interactions,
		tags: tags, ids: gen, recorder: recorder, tombstones: newApproveTombstones(),
	}
}

// Attach verifies a presented token. The error never says why it failed.
func (s *SessionToolService) Attach(token string) (SessionBinding, error) {
	binding, ok := s.tokens.verify(token)
	if !ok {
		return SessionBinding{}, errcode.New(errcode.PermissionDenied, "the session token is not valid")
	}
	return binding, nil
}

// callBuiltinApprove serves the approve relay to the sessions that may see
// it; every other session gets the same refusal an unknown tool gets.
func (s *SessionToolService) callBuiltinApprove(ctx context.Context, binding SessionBinding, args []byte) (FugueToolResult, error) {
	if !approveServed(s.sessionVendor(ctx, binding)) {
		return FugueToolResult{}, errcode.New(errcode.NotFound, "the tool is not available to this session")
	}
	return s.callApprove(ctx, binding, args)
}

// sessionVendor reads the vendor of the binding's session; a snapshot that
// fails has no vendor, which serves nothing vendor-specific.
func (s *SessionToolService) sessionVendor(ctx context.Context, binding SessionBinding) string {
	info, err := s.sessions.Snapshot(ctx, binding.SessionID)
	if err != nil {
		return ""
	}
	return info.Vendor
}

// approveServed reports whether the binding's session sees the approve relay
// tool: only Claude sessions, whose approval requests arrive as MCP
// permission prompts (vendor-runtime VR-05). Codex and agy relays arrive as
// protocol server requests through their adapters, so their bridge serves no
// approve tool — a model calling it could answer its own request.
func approveServed(vendor string) bool {
	return vendor == VendorClaude
}

// List returns the tools of the binding's role, by name. The approve relay
// is listed to Claude sessions only, and a tool whose Available gate fails
// for this session is not listed.
func (s *SessionToolService) List(ctx context.Context, binding SessionBinding) ([]FugueToolView, error) {
	if !s.tokens.live(binding) {
		return nil, errSessionEnded()
	}
	info := s.lazySnapshot(ctx, binding.SessionID)
	out := make([]FugueToolView, 0, len(s.tools)+1)
	if approveServed(s.sessionVendor(ctx, binding)) {
		out = append(out, approveToolView())
	}
	for _, tool := range s.tools {
		if !servesRole(tool, binding.Role) || !toolAvailable(ctx, tool, info) {
			continue
		}
		out = append(out, FugueToolView{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	slices.SortFunc(out, func(a, b FugueToolView) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// lazySnapshot reads the session record at most once, and only when a tool's
// Available gate asks for it.
func (s *SessionToolService) lazySnapshot(ctx context.Context, sessionID string) func() (vendorstore.Session, bool) {
	var info *vendorstore.Session
	return func() (vendorstore.Session, bool) {
		if info == nil {
			snapshot, err := s.sessions.Snapshot(ctx, sessionID)
			if err != nil {
				return vendorstore.Session{}, false
			}
			info = &snapshot
		}
		return *info, true
	}
}

// toolAvailable applies the tool's Available gate; a session that cannot be
// read fails closed.
func toolAvailable(ctx context.Context, tool *FugueTool, info func() (vendorstore.Session, bool)) bool {
	if tool.Available == nil {
		return true
	}
	snapshot, ok := info()
	return ok && tool.Available(ctx, snapshot)
}

func (s *SessionToolService) recordActivity(sessionID string) {
	if tracker, ok := s.recorder.(interface{ TouchSession(string) }); ok {
		tracker.TouchSession(sessionID)
	}
}

// Call runs one tool call of the binding's session through Decide: the
// session's approval mode, the env-tag hard rule (I-03), grants and the
// tool's own facts decide whether it runs, asks the user or is refused.
// The built-in approve relay bypasses Decide completely (vendor-runtime VR-05)
// and is served to Claude sessions only.
func (s *SessionToolService) Call(ctx context.Context, binding SessionBinding, name string, args []byte) (FugueToolResult, error) {
	if !s.tokens.live(binding) {
		return FugueToolResult{}, errSessionEnded()
	}
	s.recordActivity(binding.SessionID)
	defer s.recordActivity(binding.SessionID)
	if name == approveToolName {
		return s.callBuiltinApprove(ctx, binding, args)
	}
	tool, ok := s.lookupTool(ctx, binding, name)
	if !ok {
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
	stopped, run, err := s.admit(ctx, binding, check, tool, &plan, &verdict)
	if err != nil || !run {
		return stopped, err
	}
	if !s.tokens.live(binding) {
		return FugueToolResult{}, errSessionEnded()
	}
	return runTool(ctx, tool, binding.SessionID, args), nil
}

// admit applies the verdict: run reports whether the tool may run; when it
// may not, the result is what the vendor's model reads instead.
func (s *SessionToolService) admit(ctx context.Context, binding SessionBinding, check *interaction.Binding, tool *FugueTool, plan *FugueToolPlan, verdict *interaction.Verdict,
) (result FugueToolResult, run bool, err error) {
	switch verdict.Kind {
	case interaction.VerdictRefuse:
		return FugueToolResult{Text: "refused by the session policy: " + verdict.Reason, IsError: true}, false, nil
	case interaction.VerdictAsk:
		allowed, askErr := s.confirm(ctx, binding, check, tool, plan, verdict.Offers)
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
func (s *SessionToolService) confirm(ctx context.Context, binding SessionBinding, check *interaction.Binding, tool *FugueTool, plan *FugueToolPlan, offers []interaction.Outcome) (bool, error) {
	spec, err := buildConfirmSpec(check, tool, plan, offers)
	if err != nil {
		return false, err
	}
	ticket, err := s.interactions.Ensure(spec)
	if err != nil {
		return false, err
	}
	if !ticket.Resolved {
		if ticket, err = s.interactions.AwaitResolved(ctx, ticket.RequestID); err != nil {
			// The caller is gone: the prompt must not outlive the call.
			if _, cancelErr := s.interactions.owner.InvalidateOperation(spec.OperationID); cancelErr != nil {
				return false, errcode.Wrap(errcode.Internal, "cancel the abandoned tool confirmation", cancelErr)
			}
			return false, err
		}
	}
	if !s.tokens.live(binding) {
		return false, errSessionEnded()
	}
	if ticket.Outcome != interaction.OutcomeAllowedOnce && ticket.Outcome != interaction.OutcomeAllowedSession {
		return false, nil
	}
	if _, err := s.interactions.owner.ClaimReceipt(ticket.ReceiptID, check); err != nil {
		return false, errcode.New(errcode.FailedPrecondition, "the approval could not be used for this call")
	}
	return true, nil
}

func buildConfirmSpec(check *interaction.Binding, tool *FugueTool, plan *FugueToolPlan, offers []interaction.Outcome) (*interaction.Spec, error) {
	operation, err := interaction.NativeOperationKey(check.InvocationID, check.AttemptID, check.CheckGeneration)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "key the tool confirmation", err)
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
	return &interaction.Spec{
		OperationID: operation, Kind: interaction.KindConfirm, Summary: plan.Summary, Risk: string(risk),
		GrantScopeLabel: label, Offers: offers, Binding: *check,
	}, nil
}

// lookupTool finds the tool and applies the role and availability gates with
// one outcome, so a session never learns why a tool is missing.
func (s *SessionToolService) lookupTool(ctx context.Context, binding SessionBinding, name string) (*FugueTool, bool) {
	tool, ok := s.tools[name]
	if !ok || !servesRole(tool, binding.Role) {
		return nil, false
	}
	if tool.Available != nil {
		info, err := s.sessions.Snapshot(ctx, binding.SessionID)
		if err != nil || !tool.Available(ctx, info) {
			return nil, false
		}
	}
	return tool, true
}

func servesRole(tool *FugueTool, role string) bool {
	return len(tool.Roles) == 0 || slices.Contains(tool.Roles, role)
}

func errSessionEnded() error {
	return errcode.New(errcode.PermissionDenied, "the session token is no longer valid")
}
