package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	approveToolName             = "approve"
	approveToolDescription      = "Relay a vendor tool permission request to the user and return allow or deny."
	approveUnavailableMessage   = "the approval request could not be completed"
	approveOptionAllow          = "allow"
	approveOptionDeny           = "deny"
	approveMaxTombstonesPerSess = 256
	approveToolSchemaText       = `{"type":"object","properties":{"tool_name":{"type":"string"},"input":{},"tool_use_id":{"type":"string"}},"required":["tool_name","tool_use_id"]}`
)

// approvalRecorder provides the session's active turn and single-writer event
// journal for recording ApprovalDecided outcomes.
type approvalRecorder interface {
	ActiveTurnID(ctx context.Context, sessionID string) (string, error)
	RecordEvent(ctx context.Context, sessionID string, event *vendorstore.Event) error
}

func approveToolView() FugueToolView {
	return FugueToolView{
		Name:        approveToolName,
		Description: approveToolDescription,
		InputSchema: json.RawMessage(approveToolSchemaText),
	}
}

func approveToolError() FugueToolResult {
	return FugueToolResult{Text: approveUnavailableMessage, IsError: true}
}

type rawApproveRequest struct {
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
}

type parsedApproveArgs struct {
	toolName     string
	toolUseID    string
	rawInput     json.RawMessage
	compactInput string
	digest       [sha256.Size]byte
}

func parseApproveArgs(args []byte) (*parsedApproveArgs, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		return nil, errcode.New(errcode.InvalidArgument, "approve arguments are required")
	}
	var req rawApproveRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "decode approve arguments", err)
	}
	if req.ToolName == "" || req.ToolUseID == "" {
		return nil, errcode.New(errcode.InvalidArgument, "approve requires tool_name and tool_use_id")
	}
	if !utf8.ValidString(req.ToolName) || !utf8.ValidString(req.ToolUseID) {
		return nil, errcode.New(errcode.InvalidArgument, "approve fields must be valid UTF-8")
	}
	trimmed := bytes.TrimSpace(req.Input)
	if len(trimmed) == 0 {
		trimmed = []byte("{}")
	} else if !json.Valid(trimmed) {
		return nil, errcode.New(errcode.InvalidArgument, "approve input must be valid JSON")
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, trimmed); err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "compact approve input", err)
	}
	return &parsedApproveArgs{
		toolName:     req.ToolName,
		toolUseID:    req.ToolUseID,
		rawInput:     json.RawMessage(trimmed),
		compactInput: compacted.String(),
		digest:       approveArgsDigest(req.ToolName, compacted.Bytes()),
	}, nil
}

func approveArgsDigest(toolName string, compactInput []byte) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write([]byte(toolName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(compactInput)
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func formatApproveSummary(toolName, compactInput string) string {
	raw := toolName + ": " + compactInput
	if len(raw) <= interaction.MaxSummaryBytes {
		return raw
	}
	const ellipsis = "..."
	limit := interaction.MaxSummaryBytes - len(ellipsis)
	cut := limit
	for cut > 0 && !utf8.RuneStart(raw[cut]) {
		cut--
	}
	return raw[:cut] + ellipsis
}

func formatApproveResult(allow bool, originalInput json.RawMessage, note string) (FugueToolResult, error) {
	var payload any
	if allow {
		payload = struct {
			Behavior     string          `json:"behavior"`
			UpdatedInput json.RawMessage `json:"updatedInput"`
		}{
			Behavior:     approveOptionAllow,
			UpdatedInput: originalInput,
		}
	} else {
		payload = struct {
			Behavior string `json:"behavior"`
			Message  string `json:"message"`
		}{
			Behavior: approveOptionDeny,
			Message:  note,
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return FugueToolResult{}, errcode.Wrap(errcode.Internal, "marshal approve response", err)
	}
	return FugueToolResult{Text: string(raw)}, nil
}

func (s *SessionToolService) callApprove(ctx context.Context, binding SessionBinding, args []byte) (FugueToolResult, error) {
	req, err := parseApproveArgs(args)
	if err != nil {
		return FugueToolResult{}, err
	}
	if res, ok := s.executeApprove(ctx, binding, req); ok {
		return res, nil
	}
	return approveToolError(), nil
}

func (s *SessionToolService) executeApprove(ctx context.Context, binding SessionBinding, req *parsedApproveArgs) (FugueToolResult, bool) {
	turnID, opID, ok := s.prepareApprove(ctx, binding, req)
	if !ok {
		return FugueToolResult{}, false
	}
	if res, handled := s.replayTombstone(binding, opID, req); handled {
		return res, !res.IsError
	}
	choice, ok := s.awaitAndTakeRelayed(ctx, binding, opID, req, buildApproveSpec(binding.SessionID, turnID, opID, req))
	if !ok || !s.canApproveNow(binding) {
		return FugueToolResult{}, false
	}
	if err := s.recordApprovalDecided(ctx, binding, turnID, opID, req, choice.allow, choice.note); err != nil {
		return FugueToolResult{}, false
	}
	if !s.canApproveNow(binding) {
		return FugueToolResult{}, false
	}
	res, err := formatApproveResult(choice.allow, req.rawInput, choice.note)
	if err != nil {
		return FugueToolResult{}, false
	}
	s.tombstones.put(binding.SessionID, opID, req.digest, choice.allow, choice.note)
	return res, true
}

func (s *SessionToolService) prepareApprove(ctx context.Context, binding SessionBinding, req *parsedApproveArgs) (string, string, bool) {
	if !s.canApproveNow(binding) {
		return "", "", false
	}
	turnID, err := s.recorder.ActiveTurnID(ctx, binding.SessionID)
	if err != nil || turnID == "" {
		return "", "", false
	}
	opID, err := interaction.VendorOperationKey(binding.SessionID, turnID, req.toolUseID)
	if err != nil {
		return "", "", false
	}
	return turnID, opID, true
}

func buildApproveSpec(sessionID, turnID, opID string, req *parsedApproveArgs) *interaction.Spec {
	return &interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Summary:     formatApproveSummary(req.toolName, req.compactInput),
		Relayed:     true,
		Options: []interaction.Option{
			{ID: approveOptionAllow, Label: "Allow"},
			{ID: approveOptionDeny, Label: "Deny"},
		},
		Binding: interaction.Binding{
			SessionID:       sessionID,
			TurnID:          turnID,
			VendorRequestID: req.toolUseID,
		},
	}
}

func (s *SessionToolService) awaitAndTakeRelayed(ctx context.Context, binding SessionBinding, opID string, req *parsedApproveArgs, spec *interaction.Spec) (approveChoice, bool) {
	ticket, err := s.interactions.Ensure(spec)
	if err != nil {
		return approveChoice{}, false
	}
	resolved, err := s.interactions.awaitResolvedUntil(ctx, ticket.RequestID, s.tokens.watch(binding))
	if err != nil || resolved == nil || !resolved.Resolved {
		if !s.tokens.live(binding) {
			if _, invErr := s.interactions.owner.InvalidateOperation(opID); invErr != nil {
				return approveChoice{}, false
			}
		}
		return approveChoice{}, false
	}
	if !s.canApproveNow(binding) {
		return approveChoice{}, false
	}
	if _, ok := translateRelayedOutcome(resolved); !ok {
		return approveChoice{}, false
	}
	delivered, err := s.interactions.TakeRelayed(opID)
	if err != nil {
		return s.fallbackRelayedTombstone(err, binding, opID, req)
	}
	return translateRelayedOutcome(delivered)
}

func (s *SessionToolService) fallbackRelayedTombstone(err error, binding SessionBinding, opID string, req *parsedApproveArgs) (approveChoice, bool) {
	code := errcode.CodeOf(err)
	if code != errcode.NotFound && code != errcode.InteractionNotFound {
		return approveChoice{}, false
	}
	entry, ok := s.tombstones.get(binding.SessionID, opID)
	if !ok || entry.digest != req.digest || !s.canApproveNow(binding) {
		return approveChoice{}, false
	}
	return approveChoice{allow: entry.allow, note: entry.note}, true
}

func (s *SessionToolService) canApproveNow(binding SessionBinding) bool {
	return s.tokens.live(binding) && s.interactions != nil && !s.interactions.IsVaultLocked() && s.recorder != nil
}

func (s *SessionToolService) replayTombstone(binding SessionBinding, opID string, req *parsedApproveArgs) (FugueToolResult, bool) {
	entry, ok := s.tombstones.get(binding.SessionID, opID)
	if !ok {
		return FugueToolResult{}, false
	}
	if entry.digest != req.digest || !s.canApproveNow(binding) {
		return approveToolError(), true
	}
	res, err := formatApproveResult(entry.allow, req.rawInput, entry.note)
	if err != nil {
		return approveToolError(), true
	}
	return res, true
}

type approveChoice struct {
	allow bool
	note  string
}

func translateRelayedOutcome(ticket *interaction.Ticket) (approveChoice, bool) {
	switch ticket.Outcome {
	case interaction.OutcomeAnswered:
		switch ticket.SelectedOptionID {
		case approveOptionAllow:
			return approveChoice{allow: true}, true
		case approveOptionDeny:
			return approveChoice{allow: false, note: ticket.Text}, true
		default:
			return approveChoice{}, false
		}
	case interaction.OutcomeDenied:
		return approveChoice{allow: false, note: ticket.Text}, true
	case interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession,
		interaction.OutcomeCancelled, interaction.OutcomeUnavailable, interaction.OutcomeExpired:
		return approveChoice{}, false
	default:
		return approveChoice{}, false
	}
}

func (s *SessionToolService) recordApprovalDecided(ctx context.Context, binding SessionBinding, turnID, opID string, req *parsedApproveArgs, allow bool, note string) error {
	if s.recorder == nil || s.ids == nil {
		return errcode.New(errcode.FailedPrecondition, approveUnavailableMessage)
	}
	itemID, err := s.ids.NewID()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "generate approval event ID", err)
	}
	decision := approveOptionDeny
	if allow {
		decision = approveOptionAllow
	}
	sealedBody, err := json.Marshal(struct {
		OperationID string `json:"operation_id"`
		ToolName    string `json:"tool_name"`
		ToolUseID   string `json:"tool_use_id"`
		Decision    string `json:"decision"`
		Note        string `json:"note,omitempty"`
	}{
		OperationID: opID,
		ToolName:    req.toolName,
		ToolUseID:   req.toolUseID,
		Decision:    decision,
		Note:        note,
	})
	if err != nil {
		return errcode.Wrap(errcode.Internal, "marshal approval decision", err)
	}
	if recErr := s.recorder.RecordEvent(ctx, binding.SessionID, &vendorstore.Event{
		Type:    vendorstore.ApprovalDecided,
		TurnID:  turnID,
		Payload: sealedBody,
		Item: &vendorstore.Item{
			ID:     itemID,
			TurnID: turnID,
			Kind:   vendorstore.KindApproval,
			Metadata: vendorstore.ItemMetadata{
				RequestKind: approveToolName,
				Decision:    decision,
				Decider:     "user",
			},
		},
	}); recErr != nil {
		return errcode.Wrap(errcode.Internal, "record approval decision", recErr)
	}
	return nil
}

func (s *SessionToolService) endSession(sessionID string) {
	s.tombstones.clearSession(sessionID)
}

func (s *SessionToolService) endAllSessions() {
	s.tombstones.clearAll()
}

type approveTombstoneEntry struct {
	digest [sha256.Size]byte
	allow  bool
	note   string
}

type sessionApproveTombstone struct {
	order   []string
	entries map[string]approveTombstoneEntry
}

type approveTombstones struct {
	mu       sync.Mutex
	sessions map[string]*sessionApproveTombstone
}

func newApproveTombstones() *approveTombstones {
	return &approveTombstones{sessions: make(map[string]*sessionApproveTombstone)}
}

func (t *approveTombstones) get(sessionID, opID string) (approveTombstoneEntry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sess, ok := t.sessions[sessionID]
	if !ok {
		return approveTombstoneEntry{}, false
	}
	entry, ok := sess.entries[opID]
	return entry, ok
}

func (t *approveTombstones) put(sessionID, opID string, digest [sha256.Size]byte, allow bool, note string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sess, ok := t.sessions[sessionID]
	if !ok {
		sess = &sessionApproveTombstone{entries: make(map[string]approveTombstoneEntry)}
		t.sessions[sessionID] = sess
	}
	if _, exists := sess.entries[opID]; !exists {
		if len(sess.order) >= approveMaxTombstonesPerSess {
			oldest := sess.order[0]
			sess.order = sess.order[1:]
			delete(sess.entries, oldest)
		}
		sess.order = append(sess.order, opID)
	}
	sess.entries[opID] = approveTombstoneEntry{digest: digest, allow: allow, note: note}
}

func (t *approveTombstones) clearSession(sessionID string) {
	t.mu.Lock()
	delete(t.sessions, sessionID)
	t.mu.Unlock()
}

func (t *approveTombstones) clearAll() {
	t.mu.Lock()
	clear(t.sessions)
	t.mu.Unlock()
}
