package agy

// Verbatim `session/request_permission` normalization and JSON-RPC response
// encoding (VR-05, vendor-runtime §6, interaction §7, §9).
//
// `agy` sends `session/request_permission` with its own `options` (`optionId`,
// `name`, `kind`: `allow_once`, `allow_always`, `reject_once`, `reject_always`)
// and a `toolCall` object (`title`, `kind`, `rawInput`, `locations`, `content`).
// The normalizer emits a `vendor.ServerRequest` with verbatim option order and
// compact `toolCall` JSON detail. `Session.Resolve` answers at most once with:
//   - `{"outcome":"selected","optionId":<user choice>}` when a user selects an option,
//   - `{"outcome":"cancelled"}` when the turn was cancelled via `session/cancel`, or
//   - a JSON-RPC error (`-32000`) on every failure path (never a synthesized option).

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
	maxSummaryBytes  = 2 << 10
	truncationSuffix = "… [truncated, full input in detail]"
)

type pendingServerRequest struct {
	rawID  json.RawMessage
	method string
}

type requestPermissionToolCall struct {
	ToolCallID string          `json:"toolCallId"`
	Name       string          `json:"name"`
	Title      string          `json:"title"`
	Kind       string          `json:"kind"`
	RawInput   json.RawMessage `json:"rawInput"`
}

func (n *Normalizer) normalizeServerRequest(frame *Frame) Observation {
	if frame.Method != MethodSessionRequestPermission {
		return n.unsupported(n.frameRef(frame), "acp:"+frame.Method)
	}
	return n.normalizeRequestPermission(frame)
}

func (n *Normalizer) normalizeRequestPermission(frame *Frame) Observation {
	if frame.CanonicalID == "" {
		return n.unsupported(n.frameRef(frame), "acp:"+MethodSessionRequestPermission)
	}
	var params RequestPermissionParams
	if json.Unmarshal(frame.Params, &params) != nil || params.SessionID == "" || len(params.ToolCall) == 0 || len(params.Options) == 0 {
		return n.unsupported(n.frameRef(frame), "acp:"+MethodSessionRequestPermission)
	}
	var tc requestPermissionToolCall
	if json.Unmarshal(params.ToolCall, &tc) != nil || tc.ToolCallID == "" {
		return n.unsupported(n.frameRef(frame), "acp:"+MethodSessionRequestPermission)
	}
	optionIDs, optionDetails, ok := validatePermissionOptions(params.Options)
	if !ok {
		return n.unsupported(n.frameRef(frame), "acp:"+MethodSessionRequestPermission)
	}
	detail := compactJSON(params.ToolCall)
	summary := boundSummary(permissionSummary(&tc, detail))
	return Observation{
		ServerRequest: &vendor.ServerRequest{
			RequestID:     frame.CanonicalID,
			TurnID:        n.currentTurnID(),
			ItemID:        tc.ToolCallID,
			Method:        MethodSessionRequestPermission,
			Kind:          vendor.ServerRequestConfirm,
			Summary:       summary,
			Detail:        detail,
			Options:       optionIDs,
			OptionDetails: optionDetails,
		},
	}
}

func validatePermissionOptions(opts []PermissionOption) ([]string, []vendor.ServerRequestOption, bool) {
	ids := make([]string, 0, len(opts))
	details := make([]vendor.ServerRequestOption, 0, len(opts))
	seen := make(map[string]bool, len(opts))
	for _, opt := range opts {
		id := strings.TrimSpace(opt.OptionID)
		name := strings.TrimSpace(opt.Name)
		if id == "" || name == "" || !isValidPermissionOptionKind(opt.Kind) || seen[id] {
			return nil, nil, false
		}
		seen[id] = true
		ids = append(ids, id)
		details = append(details, vendor.ServerRequestOption{
			ID:   id,
			Name: name,
			Kind: opt.Kind,
		})
	}
	return ids, details, len(ids) > 0
}

func isValidPermissionOptionKind(kind string) bool {
	switch kind {
	case PermissionOptionKindAllowOnce,
		PermissionOptionKindAllowAlways,
		PermissionOptionKindRejectOnce,
		PermissionOptionKindRejectAlways:
		return true
	default:
		return false
	}
}

func permissionSummary(tc *requestPermissionToolCall, detail string) string {
	title := strings.TrimSpace(tc.Title)
	if title != "" {
		return title
	}
	name := strings.TrimSpace(tc.Name)
	if name != "" {
		return name + ": " + detail
	}
	return MethodSessionRequestPermission + ": " + detail
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
	if frame.CanonicalID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seenApprovals[frame.CanonicalID] {
		return false
	}
	s.seenApprovals[frame.CanonicalID] = true
	s.pendingApprovals[frame.CanonicalID] = pendingServerRequest{
		rawID:  append(json.RawMessage(nil), frame.ID...),
		method: frame.Method,
	}
	return true
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
		message = RPCErrorPermissionNotRelayed
	}
	raw, err := s.codec.EncodeResponseError(pending.rawID, RPCErrorServerError, message)
	if err != nil {
		return
	}
	if err := s.commitApprovalResponseLocked(id, raw); err != nil {
		return
	}
}

// Resolve sends the user's permission choice, a turn-cancel outcome, or a
// JSON-RPC error response back to `agy` for req.ID, at most once.
func (s *Session) Resolve(ctx context.Context, req vendor.ApprovalRef, d vendor.Decision) error {
	if req.ID == "" {
		return errcode.New(errcode.InvalidArgument, "approval request id is required")
	}
	if ctx.Err() != nil {
		return errcode.Wrap(errcode.Canceled, "agy resolve canceled", ctx.Err())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateClosed {
		return s.closedErrLocked()
	}
	if s.resolvedApprovals[req.ID] {
		return errcode.New(errcode.FailedPrecondition, "agy server request already resolved")
	}
	pending, ok := s.pendingApprovals[req.ID]
	if !ok {
		return errcode.New(errcode.NotFound, "agy server request not found")
	}
	raw, err := s.encodePermissionResponse(pending, d)
	if err != nil {
		return err
	}
	return s.commitApprovalResponseLocked(req.ID, raw)
}

func (s *Session) encodePermissionResponse(pending pendingServerRequest, d vendor.Decision) ([]byte, error) {
	if d.Error != nil {
		return s.codec.EncodeResponseError(pending.rawID, RPCErrorServerError, d.Error.Error())
	}
	if pending.method != MethodSessionRequestPermission {
		return s.codec.EncodeResponseError(pending.rawID, RPCErrorMethodNotSupported, RPCErrorMethodNotSupportedByFugue)
	}
	if d.Cancelled {
		return s.codec.EncodeResponseResult(pending.rawID, RequestPermissionResult{
			Outcome: PermissionOutcomeCancelled,
		})
	}
	optionID := strings.TrimSpace(d.Message)
	if optionID == "" {
		return s.codec.EncodeResponseError(pending.rawID, RPCErrorServerError, RPCErrorPermissionNotRelayed)
	}
	return s.codec.EncodeResponseResult(pending.rawID, RequestPermissionResult{
		Outcome:  PermissionOutcomeSelected,
		OptionID: optionID,
	})
}

func (s *Session) commitApprovalResponseLocked(id string, raw []byte) error {
	delete(s.pendingApprovals, id)
	s.resolvedApprovals[id] = true
	if err := s.proc.WriteLine(bytes.TrimRight(raw, "\r\n")); err != nil {
		if errors.Is(err, process.ErrStdinClosed) {
			return vendor.ErrSessionClosed
		}
		return errcode.Wrap(errcode.Internal, "write agy permission response", err)
	}
	return nil
}

func (s *Session) rejectPendingApprovalsLocked() {
	for id, req := range s.pendingApprovals {
		delete(s.pendingApprovals, id)
		s.resolvedApprovals[id] = true
		raw, err := s.codec.EncodeResponseError(req.rawID, RPCErrorServerError, RPCErrorPermissionNotRelayed)
		if err != nil {
			continue
		}
		if writeErr := s.proc.WriteLine(bytes.TrimRight(raw, "\r\n")); writeErr != nil {
			continue
		}
	}
}
