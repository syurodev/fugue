package codex

import "encoding/json"

// Codex sends `mcpServer/elicitation/request` both for its own privileged
// approvals and as the general forwarding channel for MCP server
// elicitations. The two are told apart by the request `_meta`:
// `codex_approval_kind` is Codex-privileged and appears only on Codex's own
// approval gates; a forwarded MCP elicitation never carries it (codex-rs
// protocol/src/mcp_approval_meta.rs, measured at rust-v0.167.0). Fugue
// relays only the MCP tool-call gate and refuses everything else, because
// answering a forwarded elicitation would answer for the MCP server instead
// of relaying it (vendor-runtime VR-05).

const (
	// MethodElicitationRequest is the server request method.
	MethodElicitationRequest = "mcpServer/elicitation/request"
)

// The actions of an elicitation answer.
const (
	// ElicitationActionAccept grants the tool call.
	ElicitationActionAccept = "accept"
	// ElicitationActionDecline refuses the tool call.
	ElicitationActionDecline = "decline"

	// ElicitationApprovalKindKey names the request `_meta` field that marks
	// Codex's own approval gates, and ElicitationApprovalKindMcpToolCall is
	// the kind of the MCP tool-call gate.
	ElicitationApprovalKindKey         = "codex_approval_kind"
	ElicitationApprovalKindMcpToolCall = "mcp_tool_call"

	// ElicitationPersistKey names the request `_meta` field listing the
	// persistence scopes the gate offers, and the response `_meta` field
	// carrying the user's choice.
	ElicitationPersistKey = "persist"

	// ElicitationPersistSession and ElicitationPersistAlways are the gate's
	// persistence values: remember the approval for the session, or persist
	// it across sessions (codex-rs core parse_mcp_tool_approval_elicitation_
	// response, rust-v0.167.0).
	ElicitationPersistSession = "session"
	ElicitationPersistAlways  = "always"

	// ElicitationModeForm is the only mode the tool-call gate uses.
	ElicitationModeForm = "form"
)

// Option IDs of the relayed gate: one-time accept, the persistence scopes
// the request listed, and decline.
const (
	ElicitationOptionAccept        = "accept"
	ElicitationOptionAcceptSession = "accept_session"
	ElicitationOptionAcceptAlways  = "accept_always"
	ElicitationOptionDecline       = "decline"
)

type (
	// ElicitationRequestParams is the server request payload for
	// mcpServer/elicitation/request.
	ElicitationRequestParams struct {
		ThreadID        string               `json:"threadId"`
		TurnID          string               `json:"turnId"`
		ServerName      string               `json:"serverName"`
		Mode            string               `json:"mode"`
		Message         string               `json:"message"`
		RequestedSchema json.RawMessage      `json:"requestedSchema,omitempty"`
		Meta            *ElicitationGateMeta `json:"_meta,omitempty"`
	}

	// ElicitationGateMeta is the `_meta` of the tool-call gate. Persist
	// lists the persistence scopes the gate offers.
	ElicitationGateMeta struct {
		ApprovalKind string   `json:"codex_approval_kind,omitempty"`
		Persist      []string `json:"persist,omitempty"`
	}

	// ElicitationRequestResponse answers mcpServer/elicitation/request. Meta
	// carries the chosen persistence scope: persist "session" remembers the
	// approval for the session, "always" persists it across sessions
	// (codex-rs core, rust-v0.167.0).
	ElicitationRequestResponse struct {
		Action string                 `json:"action"`
		Meta   *ElicitationAnswerMeta `json:"_meta,omitempty"`
	}

	// ElicitationAnswerMeta is the `_meta` of an elicitation answer.
	ElicitationAnswerMeta struct {
		Persist string `json:"persist,omitempty"`
	}
)
