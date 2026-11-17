package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

// Resolve sends the user's choice (or a JSON-RPC error response when d.Error != nil)
// back to Codex for req.ID, at most once.
func (s *Session) Resolve(ctx context.Context, req vendor.ApprovalRef, d vendor.Decision) error {
	if req.ID == "" {
		return errcode.New(errcode.InvalidArgument, "approval request id is required")
	}
	if ctx.Err() != nil {
		return errcode.Wrap(errcode.Canceled, "codex resolve canceled", ctx.Err())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateClosed {
		return s.closedErrLocked()
	}
	if s.resolvedApprovals[req.ID] {
		return errcode.New(errcode.FailedPrecondition, "codex server request already resolved")
	}
	pending, ok := s.pendingApprovals[req.ID]
	if !ok {
		return errcode.New(errcode.NotFound, "codex server request not found")
	}
	raw, err := s.encodeServerRequestResponse(pending, d)
	if err != nil {
		return err
	}
	return s.commitApprovalResponseLocked(req.ID, raw)
}

func (s *Session) rejectUnrelayedServerRequest(id, message string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateClosed || s.resolvedApprovals[id] {
		return
	}
	pending, ok := s.pendingApprovals[id]
	if !ok {
		return
	}
	if message == "" {
		message = "unsupported server request"
	}
	raw, err := s.codec.EncodeResponse(pending.rawID, nil, &RPCError{
		Code:    jsonRPCServerErrorCode,
		Message: message,
	})
	if err != nil {
		return
	}
	if wErr := s.commitApprovalResponseLocked(id, raw); wErr != nil {
		return
	}
}

func (s *Session) commitApprovalResponseLocked(id string, raw []byte) error {
	delete(s.pendingApprovals, id)
	s.resolvedApprovals[id] = true
	if err := s.proc.WriteLine(bytes.TrimRight(raw, "\r\n")); err != nil {
		if errors.Is(err, process.ErrStdinClosed) {
			return vendor.ErrSessionClosed
		}
		return errcode.Wrap(errcode.Internal, "write codex approval response", err)
	}
	return nil
}

func (s *Session) encodeServerRequestResponse(pending pendingServerRequest, d vendor.Decision) ([]byte, error) {
	if d.Error != nil {
		return s.codec.EncodeResponse(pending.rawID, nil, &RPCError{
			Code:    jsonRPCServerErrorCode,
			Message: d.Error.Error(),
		})
	}
	switch pending.method {
	case MethodCommandApproval:
		return s.encodeCommandApprovalResponse(pending, d)
	case MethodFileChangeApproval:
		return s.codec.EncodeResponse(pending.rawID, FileChangeApprovalResponse{Decision: resolveChoice(d)}, nil)
	case MethodPermissionsApproval:
		return s.encodePermissionsResponse(pending, d)
	case MethodRequestUserInput:
		return s.encodeUserInputResponse(pending, d)
	case MethodElicitationRequest:
		return s.encodeElicitationResponse(pending, d)
	default:
		return s.codec.EncodeResponse(pending.rawID, nil, &RPCError{
			Code:    jsonRPCServerErrorCode,
			Message: "unsupported server request method",
		})
	}
}

func (s *Session) encodeCommandApprovalResponse(pending pendingServerRequest, d vendor.Decision) ([]byte, error) {
	choice := resolveChoice(d)
	if choice == DecisionAcceptWithExecpolicyAmendment {
		var params CommandApprovalParams
		if json.Unmarshal(pending.params, &params) != nil || len(params.ProposedExecpolicyAmendment) == 0 {
			return nil, errcode.New(errcode.InvalidArgument, "missing proposedExecpolicyAmendment")
		}
		return s.codec.EncodeResponse(pending.rawID, CommandApprovalResponse{
			Decision: AcceptWithExecpolicyAmendmentDecision{
				AcceptWithExecpolicyAmendment: ExecpolicyAmendmentPayload{
					ExecpolicyAmendment: params.ProposedExecpolicyAmendment,
				},
			},
		}, nil)
	}
	return s.codec.EncodeResponse(pending.rawID, CommandApprovalResponse{Decision: choice}, nil)
}

// encodeElicitationResponse answers the MCP tool-call gate with the user's
// choice: the actions Codex reads are accept and decline.
func (s *Session) encodeElicitationResponse(pending pendingServerRequest, d vendor.Decision) ([]byte, error) {
	action := ElicitationActionDecline
	if d.Allow {
		action = ElicitationActionAccept
	}
	return s.codec.EncodeResponse(pending.rawID, ElicitationRequestResponse{Action: action}, nil)
}

func resolveChoice(d vendor.Decision) string {
	if d.Message != "" {
		return d.Message
	}
	if d.Allow {
		return DecisionAccept
	}
	return DecisionDecline
}

func (s *Session) encodePermissionsResponse(pending pendingServerRequest, d vendor.Decision) ([]byte, error) {
	choice := d.Message
	if choice == DecisionDecline || (!d.Allow && choice == "") {
		return s.codec.EncodeResponse(pending.rawID, PermissionsApprovalResponse{
			Permissions: json.RawMessage(`{}`),
			Scope:       PermissionScopeTurn,
		}, nil)
	}
	var params PermissionsApprovalParams
	if json.Unmarshal(pending.params, &params) != nil {
		params.Permissions = nil
	}
	perms := params.Permissions
	if len(perms) == 0 {
		perms = json.RawMessage(`{}`)
	}
	scope := PermissionScopeTurn
	if choice == PermissionScopeSession {
		scope = PermissionScopeSession
	}
	return s.codec.EncodeResponse(pending.rawID, PermissionsApprovalResponse{
		Permissions: perms,
		Scope:       scope,
	}, nil)
}

func (s *Session) encodeUserInputResponse(pending pendingServerRequest, d vendor.Decision) ([]byte, error) {
	if !d.Allow && len(d.Answers) == 0 {
		return s.codec.EncodeResponse(pending.rawID, ToolRequestUserInputResponse{
			Answers: map[string]ToolUserInputAnswer{},
		}, nil)
	}
	var params ToolRequestUserInputParams
	if json.Unmarshal(pending.params, &params) != nil {
		params.Questions = nil
	}
	answers := make(map[string]ToolUserInputAnswer, len(params.Questions))
	for _, q := range params.Questions {
		if vals := d.Answers[q.ID]; len(vals) > 0 {
			answers[q.ID] = ToolUserInputAnswer{Answers: vals}
		}
	}
	return s.codec.EncodeResponse(pending.rawID, ToolRequestUserInputResponse{Answers: answers}, nil)
}

func (s *Session) rejectPendingApprovalsLocked() {
	for id, req := range s.pendingApprovals {
		delete(s.pendingApprovals, id)
		s.resolvedApprovals[id] = true
		raw, err := s.codec.EncodeResponse(req.rawID, nil, &RPCError{
			Code:    jsonRPCServerErrorCode,
			Message: "the approval request could not be completed",
		})
		if err != nil {
			continue
		}
		if wErr := s.proc.WriteLine(bytes.TrimRight(raw, "\r\n")); wErr != nil {
			continue
		}
	}
}
