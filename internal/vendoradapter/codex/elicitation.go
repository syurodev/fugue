package codex

import "encoding/json"

// Codex gates an MCP tool call with a server request: the app-server asks,
// on behalf of the named MCP server, whether the tool may run (measured on
// 0.167.0 with the fugue bridge, 2026-11-16). Fugue relays the request
// verbatim and answers once with the user's action (vendor-runtime VR-05).

const (
	// MethodElicitationRequest is the server request method.
	MethodElicitationRequest = "mcpServer/elicitation/request"
)

// The actions of an elicitation answer: ElicitationActionAccept grants the
// tool call, ElicitationActionDecline refuses it, ElicitationActionCancel
// cancels the turn.
const (
	ElicitationActionAccept  = "accept"
	ElicitationActionDecline = "decline"
	ElicitationActionCancel  = "cancel"
)

type (
	// ElicitationRequestParams is the server request payload for
	// mcpServer/elicitation/request.
	ElicitationRequestParams struct {
		ThreadID        string          `json:"threadId"`
		TurnID          string          `json:"turnId"`
		ServerName      string          `json:"serverName"`
		Mode            string          `json:"mode"`
		Message         string          `json:"message"`
		RequestedSchema json.RawMessage `json:"requestedSchema,omitempty"`
	}

	// ElicitationRequestResponse answers mcpServer/elicitation/request.
	ElicitationRequestResponse struct {
		Action string `json:"action"`
	}
)
