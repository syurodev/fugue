package codex

// Server-request normalization and JSON-RPC approval response encoding (VR-05,
// interaction v0.16). Codex server requests become relayed confirm or question
// observations with verbatim options, a <= 2 KiB summary, and compact JSON
// detail; Session.Resolve sends the user's choice (or a JSON-RPC error response
// when the host rejects/expires the relay) back to Codex at most once.

import (
	"bytes"
	"encoding/json"
	"strings"

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
	// ExecpolicyAmendmentPayload holds an approved execpolicy amendment rule list.
	ExecpolicyAmendmentPayload struct {
		ExecpolicyAmendment []string `json:"execpolicy_amendment"`
	}
	// AcceptWithExecpolicyAmendmentDecision encodes Codex's execpolicy amendment decision object.
	AcceptWithExecpolicyAmendmentDecision struct {
		AcceptWithExecpolicyAmendment ExecpolicyAmendmentPayload `json:"acceptWithExecpolicyAmendment"`
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
	case MethodElicitationRequest:
		return n.normalizeElicitationRequest(frame)
	default:
		return n.unsupported(n.frameRef(frame), frame.Method)
	}
}

// normalizeElicitationRequest relays Codex's MCP tool-call gate verbatim
// (VR-05): the message and params reach the user as they came, and the user
// chooses how to allow it. `mcpServer/elicitation/request` is also Codex's
// general channel for forwarded MCP server elicitations — form schemas with
// required input, url mode — and those are refused with the unsupported
// path: Fugue never answers a forwarded elicitation for the MCP server, and
// an accept without the content the schema asks for would be a Fugue-made
// decision.
func (n *Normalizer) normalizeElicitationRequest(frame *Frame) Observation {
	var params ElicitationRequestParams
	if json.Unmarshal(frame.Params, &params) != nil || params.TurnID == "" || params.Message == "" {
		return n.unsupported(n.frameRef(frame), frame.Method)
	}
	if !isMcpToolCallGate(&params) {
		return n.unsupported(n.frameRef(frame), frame.Method)
	}
	return Observation{
		ServerRequest: &ServerRequest{
			RequestID: frame.CanonicalID,
			TurnID:    n.resolveTurnID(params.TurnID),
			ItemID:    params.ServerName,
			Method:    frame.Method,
			Kind:      vendor.ServerRequestConfirm,
			Summary:   boundSummary(params.Message),
			Detail:    compactJSON(frame.Params),
			Options:   elicitationGateOptions(params.Meta),
		},
	}
}

// isMcpToolCallGate reports whether the elicitation is Codex's MCP tool-call
// approval gate: a form whose `_meta` marks the privileged
// `mcp_tool_call` approval kind and whose requested schema asks for no
// input. Any other shape is a forwarded MCP elicitation or another
// privileged gate, not a tool-call approval.
func isMcpToolCallGate(params *ElicitationRequestParams) bool {
	if params.Mode != ElicitationModeForm {
		return false
	}
	if params.Meta == nil || params.Meta.ApprovalKind != ElicitationApprovalKindMcpToolCall {
		return false
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if len(params.RequestedSchema) == 0 || json.Unmarshal(params.RequestedSchema, &schema) != nil {
		return false
	}
	return len(schema.Properties) == 0 && len(schema.Required) == 0
}

// elicitationGateOptions lists the answers the gate's own `_meta` offers:
// one-time accept, each persistence scope the request lists, and decline.
func elicitationGateOptions(meta *ElicitationGateMeta) []string {
	options := []string{ElicitationOptionAccept}
	if meta != nil {
		for _, persist := range meta.Persist {
			switch persist {
			case ElicitationPersistSession:
				options = append(options, ElicitationOptionAcceptSession)
			case ElicitationPersistAlways:
				options = append(options, ElicitationOptionAcceptAlways)
			}
		}
	}
	return append(options, ElicitationOptionDecline)
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
	options := []string{DecisionAccept, DecisionAcceptForSession}
	if len(params.ProposedExecpolicyAmendment) > 0 {
		options = append(options, DecisionAcceptWithExecpolicyAmendment)
	}
	options = append(options, DecisionDecline, DecisionCancel)
	return Observation{
		ServerRequest: &ServerRequest{
			RequestID: frame.CanonicalID,
			TurnID:    n.resolveTurnID(params.TurnID),
			ItemID:    params.ItemID,
			Method:    frame.Method,
			Kind:      vendor.ServerRequestConfirm,
			Summary:   boundSummary(summary),
			Detail:    compactJSON(frame.Params),
			Options:   options,
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
			Secret:   q.IsSecret,
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

func unrelayedServerRequestMessage(_ *Observation) string {
	return "unsupported server request"
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
