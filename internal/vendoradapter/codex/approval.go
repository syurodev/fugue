package codex

// Server-request normalization and JSON-RPC approval response encoding (VR-05,
// interaction v0.16). Codex server requests become relayed confirm or question
// observations with verbatim options, a <= 2 KiB summary, and compact JSON
// detail; Session.Resolve sends the user's choice (or a JSON-RPC error response
// when the host rejects/expires the relay) back to Codex at most once.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

const (
	maxSummaryBytes        = 2 << 10
	truncationSuffix       = "… [truncated, full input in detail]"
	jsonRPCServerErrorCode = -32000
)

type pendingServerRequest struct {
	rawID  json.RawMessage
	method string
	params json.RawMessage
}

type (
	// CommandApprovalParams is the server request payload for item/commandExecution/requestApproval.
	CommandApprovalParams struct {
		ThreadID                    string          `json:"threadId"`
		TurnID                      string          `json:"turnId"`
		ItemID                      string          `json:"itemId"`
		ApprovalID                  *string         `json:"approvalId"`
		Reason                      *string         `json:"reason"`
		Command                     *string         `json:"command"`
		Cwd                         *string         `json:"cwd"`
		CommandActions              []CommandAction `json:"commandActions"`
		ProposedExecpolicyAmendment []string        `json:"proposedExecpolicyAmendment"`
	}
	// CommandApprovalResponse answers item/commandExecution/requestApproval.
	CommandApprovalResponse struct {
		Decision any `json:"decision"`
	}
	// FileChangeApprovalParams is the server request payload for item/fileChange/requestApproval.
	FileChangeApprovalParams struct {
		ThreadID  string  `json:"threadId"`
		TurnID    string  `json:"turnId"`
		ItemID    string  `json:"itemId"`
		Reason    *string `json:"reason"`
		GrantRoot *string `json:"grantRoot"`
	}
	// FileChangeApprovalResponse answers item/fileChange/requestApproval.
	FileChangeApprovalResponse struct {
		Decision string `json:"decision"`
	}
	// PermissionsApprovalParams is the server request payload for item/permissions/requestApproval.
	PermissionsApprovalParams struct {
		ThreadID    string          `json:"threadId"`
		TurnID      string          `json:"turnId"`
		ItemID      string          `json:"itemId"`
		Cwd         string          `json:"cwd"`
		Reason      *string         `json:"reason"`
		Permissions json.RawMessage `json:"permissions"`
	}
	// PermissionsApprovalResponse answers item/permissions/requestApproval.
	PermissionsApprovalResponse struct {
		Permissions json.RawMessage `json:"permissions"`
		Scope       string          `json:"scope,omitempty"`
	}
	// UserInputOption is one selectable option in item/tool/requestUserInput.
	UserInputOption struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	// UserInputQuestion is one question in item/tool/requestUserInput.
	UserInputQuestion struct {
		ID       string            `json:"id"`
		Header   string            `json:"header"`
		Question string            `json:"question"`
		IsOther  bool              `json:"isOther"`
		IsSecret bool              `json:"isSecret"`
		Options  []UserInputOption `json:"options"`
	}
	// ToolRequestUserInputParams is the server request payload for item/tool/requestUserInput.
	ToolRequestUserInputParams struct {
		ThreadID   string              `json:"threadId"`
		TurnID     string              `json:"turnId"`
		ItemID     string              `json:"itemId"`
		IsBlocking bool                `json:"isBlocking"`
		Questions  []UserInputQuestion `json:"questions"`
	}
	// ToolUserInputAnswer holds answers for a single question ID.
	ToolUserInputAnswer struct {
		Answers []string `json:"answers"`
	}
	// ToolRequestUserInputResponse answers item/tool/requestUserInput.
	ToolRequestUserInputResponse struct {
		Answers map[string]ToolUserInputAnswer `json:"answers"`
	}
)

func (n *Normalizer) normalizeServerRequest(frame *Frame) Observation {
	switch frame.Method {
	case MethodCommandApproval:
		return n.normalizeCommandApproval(frame)
	case MethodFileChangeApproval:
		return n.normalizeFileChangeApproval(frame)
	case MethodPermissionsApproval:
		return n.normalizePermissionsApproval(frame)
	case MethodRequestUserInput:
		return n.normalizeUserInputRequest(frame)
	default:
		return n.unsupported(n.frameRef(frame), frame.Method)
	}
}

func (n *Normalizer) normalizeCommandApproval(frame *Frame) Observation {
	var params CommandApprovalParams
	if json.Unmarshal(frame.Params, &params) != nil || params.TurnID == "" || params.ItemID == "" {
		return n.unsupported(n.frameRef(frame), frame.Method)
	}
	summary := "Command execution approval"
	if params.Command != nil && *params.Command != "" {
		summary = "Command: " + *params.Command
	}
	if params.Reason != nil && *params.Reason != "" {
		summary += " (" + *params.Reason + ")"
	}
	return Observation{
		ServerRequest: &ServerRequest{
			RequestID: frame.CanonicalID,
			TurnID:    n.resolveTurnID(params.TurnID),
			ItemID:    params.ItemID,
			Method:    frame.Method,
			Kind:      vendor.ServerRequestConfirm,
			Summary:   boundSummary(summary),
			Detail:    compactJSON(frame.Params),
			Options:   []string{DecisionAccept, DecisionAcceptForSession, DecisionDecline, DecisionCancel},
		},
	}
}

func (n *Normalizer) normalizeFileChangeApproval(frame *Frame) Observation {
	var params FileChangeApprovalParams
	if json.Unmarshal(frame.Params, &params) != nil || params.TurnID == "" || params.ItemID == "" {
		return n.unsupported(n.frameRef(frame), frame.Method)
	}
	summary := "File change approval for " + params.ItemID
	if params.Reason != nil && *params.Reason != "" {
		summary += " (" + *params.Reason + ")"
	}
	detail := compactJSON(frame.Params)
	if changes, ok := n.fileChanges[params.ItemID]; ok && len(changes) > 0 {
		detail = enrichFileChangeDetail(frame.Params, changes)
	}
	return Observation{
		ServerRequest: &ServerRequest{
			RequestID: frame.CanonicalID,
			TurnID:    n.resolveTurnID(params.TurnID),
			ItemID:    params.ItemID,
			Method:    frame.Method,
			Kind:      vendor.ServerRequestConfirm,
			Summary:   boundSummary(summary),
			Detail:    detail,
			Options:   []string{DecisionAccept, DecisionAcceptForSession, DecisionDecline, DecisionCancel},
		},
	}
}

func enrichFileChangeDetail(rawParams json.RawMessage, changes []FileUpdateChange) string {
	var merged map[string]any
	if json.Unmarshal(rawParams, &merged) != nil {
		return compactJSON(rawParams)
	}
	if _, exists := merged["changes"]; !exists {
		merged["changes"] = changes
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return compactJSON(rawParams)
	}
	return string(encoded)
}

func (n *Normalizer) normalizePermissionsApproval(frame *Frame) Observation {
	var params PermissionsApprovalParams
	if json.Unmarshal(frame.Params, &params) != nil || params.TurnID == "" || params.ItemID == "" {
		return n.unsupported(n.frameRef(frame), frame.Method)
	}
	summary := "Permissions approval in " + params.Cwd
	if params.Reason != nil && *params.Reason != "" {
		summary += " (" + *params.Reason + ")"
	}
	return Observation{
		ServerRequest: &ServerRequest{
			RequestID: frame.CanonicalID,
			TurnID:    n.resolveTurnID(params.TurnID),
			ItemID:    params.ItemID,
			Method:    frame.Method,
			Kind:      vendor.ServerRequestConfirm,
			Summary:   boundSummary(summary),
			Detail:    compactJSON(frame.Params),
			Options:   []string{PermissionScopeTurn, PermissionScopeSession, DecisionDecline},
		},
	}
}

func (n *Normalizer) normalizeUserInputRequest(frame *Frame) Observation {
	var params ToolRequestUserInputParams
	if json.Unmarshal(frame.Params, &params) != nil || params.TurnID == "" || params.ItemID == "" || len(params.Questions) == 0 {
		return n.unsupported(n.frameRef(frame), frame.Method)
	}
	var qOpts []vendor.ServerRequestQuestionOption
	questions := make([]vendor.ServerRequestQuestionItem, 0, len(params.Questions))
	allowFreeText := false
	summaryParts := make([]string, 0, len(params.Questions))
	for _, q := range params.Questions {
		if q.Question != "" {
			summaryParts = append(summaryParts, q.Question)
		}
		if q.IsOther || len(q.Options) == 0 {
			allowFreeText = true
		}
		opts := make([]vendor.ServerRequestQuestionOption, 0, len(q.Options))
		for _, opt := range q.Options {
			item := vendor.ServerRequestQuestionOption{
				Key:         opt.Label,
				Label:       opt.Label,
				Description: opt.Description,
			}
			opts = append(opts, item)
			qOpts = append(qOpts, item)
		}
		questions = append(questions, vendor.ServerRequestQuestionItem{
			ID:       q.ID,
			Header:   q.Header,
			Question: q.Question,
			Options:  opts,
		})
	}
	summary := strings.Join(summaryParts, "; ")
	if summary == "" {
		summary = "Tool requested user input"
	}
	return Observation{
		ServerRequest: &ServerRequest{
			RequestID:       frame.CanonicalID,
			TurnID:          n.resolveTurnID(params.TurnID),
			ItemID:          params.ItemID,
			Method:          frame.Method,
			Kind:            vendor.ServerRequestQuestion,
			Summary:         boundSummary(summary),
			Detail:          compactJSON(frame.Params),
			Questions:       questions,
			QuestionOptions: qOpts,
			AllowFreeText:   allowFreeText,
		},
	}
}

func boundSummary(s string) string {
	if len(s) <= maxSummaryBytes {
		return s
	}
	keep := max(maxSummaryBytes-len(truncationSuffix), 0)
	for keep > 0 && (s[keep]&0xC0) == 0x80 {
		keep--
	}
	return s[:keep] + truncationSuffix
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(bytes.TrimSpace(raw))
	}
	return buf.String()
}

func (s *Session) registerServerRequest(frame *Frame) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seenApprovals[frame.CanonicalID] {
		return false
	}
	s.seenApprovals[frame.CanonicalID] = true
	s.pendingApprovals[frame.CanonicalID] = pendingServerRequest{
		rawID:  append(json.RawMessage(nil), frame.ID...),
		method: frame.Method,
		params: append(json.RawMessage(nil), frame.Params...),
	}
	return true
}

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
	delete(s.pendingApprovals, req.ID)
	s.resolvedApprovals[req.ID] = true
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
		return s.codec.EncodeResponse(pending.rawID, CommandApprovalResponse{Decision: resolveChoice(d)}, nil)
	case MethodFileChangeApproval:
		return s.codec.EncodeResponse(pending.rawID, FileChangeApprovalResponse{Decision: resolveChoice(d)}, nil)
	case MethodPermissionsApproval:
		return s.encodePermissionsResponse(pending, d)
	case MethodRequestUserInput:
		return s.encodeUserInputResponse(pending, d)
	default:
		return s.codec.EncodeResponse(pending.rawID, nil, &RPCError{
			Code:    jsonRPCServerErrorCode,
			Message: "unsupported server request method",
		})
	}
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
	if !d.Allow && len(d.Answers) == 0 && d.Message == "" {
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
		if vals, ok := d.Answers[q.ID]; ok {
			answers[q.ID] = ToolUserInputAnswer{Answers: vals}
			continue
		}
		answers[q.ID] = ToolUserInputAnswer{Answers: []string{d.Message}}
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
