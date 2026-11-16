package agy

import "encoding/json"

// ACP v1 constants and JSON-RPC methods used by `agy acp`.
const (
	ProtocolVersion = 1

	MethodInitialize               = "initialize"
	MethodSessionNew               = "session/new"
	MethodSessionLoad              = "session/load"
	MethodSessionPrompt            = "session/prompt"
	MethodSessionCancel            = "session/cancel"
	MethodSessionSetConfigOption   = "session/set_config_option"
	MethodSessionUpdate            = "session/update"
	MethodSessionRequestPermission = "session/request_permission"
)

// ACP session/update discriminators and stop reasons.
const (
	UpdateAgentMessageChunk           = "agent_message_chunk"
	UpdateAgentThoughtChunk           = "agent_thought_chunk"
	UpdateUserMessageChunk            = "user_message_chunk"
	UpdateToolCall                    = "tool_call"
	UpdateToolCallUpdate              = "tool_call_update"
	UpdatePlan                        = "plan"
	UpdateAvailableCommands           = "available_commands_update"
	UpdateConfigOptions               = "config_option_update"
	UpdateCurrentMode                 = "current_mode_update"
	UpdateUsage                       = "usage_update"
	UpdateSessionInfo                 = "session_info_update"
	StopReasonEndTurn                 = "end_turn"
	StopReasonCancelled               = "cancelled"
	StopReasonMaxTokens               = "max_tokens"
	StopReasonRefusal                 = "refusal"
	ToolContentTypeContent            = "content"
	ToolContentTypeDiff               = "diff"
	ToolContentTypeTerminal           = "terminal"
	ToolStatusPendingWire             = "pending"
	ToolStatusInProgressWire          = "in_progress"
	ToolStatusCompletedWire           = "completed"
	ToolStatusFailedWire              = "failed"
	ToolStatusErrorWire               = "error"
	RPCErrorServerError               = -32000
	RPCErrorMethodNotSupported        = -32601
	RPCErrorInvalidParams             = -32602
	RPCErrorPermissionNotRelayed      = "the approval request could not be completed"
	RPCErrorMethodNotSupportedByFugue = "method not supported by Fugue"

	PermissionOptionKindAllowOnce    = "allow_once"
	PermissionOptionKindAllowAlways  = "allow_always"
	PermissionOptionKindRejectOnce   = "reject_once"
	PermissionOptionKindRejectAlways = "reject_always"
	PermissionOutcomeSelected        = "selected"
	PermissionOutcomeCancelled       = "cancelled"
)

type (
	// ClientInfo identifies Fugue in the ACP initialize request.
	ClientInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	// InitializeParams is the request payload for ACP initialize.
	InitializeParams struct {
		ProtocolVersion    int             `json:"protocolVersion"`
		ClientCapabilities map[string]any  `json:"clientCapabilities"`
		ClientInfo         ClientInfo      `json:"clientInfo"`
		Meta               json.RawMessage `json:"_meta,omitempty"`
	}

	// PromptCapabilities declares what prompt content blocks the agent accepts.
	PromptCapabilities struct {
		Image           bool `json:"image"`
		Audio           bool `json:"audio"`
		EmbeddedContext bool `json:"embeddedContext"`
	}

	// SessionCapabilities declares the ACP session methods advertised by the agent.
	SessionCapabilities struct {
		Resume                json.RawMessage `json:"resume,omitempty"`
		Close                 json.RawMessage `json:"close,omitempty"`
		Load                  json.RawMessage `json:"load,omitempty"`
		List                  json.RawMessage `json:"list,omitempty"`
		AdditionalDirectories json.RawMessage `json:"additionalDirectories,omitempty"`
	}

	// AgentCapabilities captures the vendor-runtime §5.3 capabilities from initialize.
	AgentCapabilities struct {
		LoadSession             bool                 `json:"loadSession"`
		LoadSessionSnake        bool                 `json:"load_session"`
		PromptCapabilities      PromptCapabilities   `json:"promptCapabilities"`
		SessionCapabilities     *SessionCapabilities `json:"sessionCapabilities,omitempty"`
		SessionCapabilitiesBody *SessionCapabilities `json:"session_capabilities,omitempty"`
		Meta                    json.RawMessage      `json:"_meta,omitempty"`
	}

	// AgentInfo describes the ACP server implementation and version.
	AgentInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}

	// InitializeResult is the result payload for ACP initialize.
	// Meta is read-only informational data and never affects safety or routing.
	InitializeResult struct {
		ProtocolVersion   int               `json:"protocolVersion"`
		AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
		AgentInfo         *AgentInfo        `json:"agentInfo,omitempty"`
		Meta              json.RawMessage   `json:"_meta,omitempty"`
	}

	// HandshakeCapabilities records the §5.3 capability booleans verified during initialize.
	HandshakeCapabilities struct {
		ProtocolVersion       int
		LoadSession           bool
		Resume                bool
		Close                 bool
		List                  bool
		AdditionalDirectories bool
		ImagePrompt           bool
		EmbeddedContextPrompt bool
	}
)

const jsonNull = "null"

// CapabilitiesSummary extracts the vendor-runtime §5.3 capability set from initialize.
func (r *InitializeResult) CapabilitiesSummary() HandshakeCapabilities {
	caps := r.AgentCapabilities
	sc := caps.SessionCapabilities
	if sc == nil {
		sc = caps.SessionCapabilitiesBody
	}
	var resume, closeCap, listCap, addDirs bool
	if sc != nil {
		resume = len(sc.Resume) > 0 && string(sc.Resume) != jsonNull
		closeCap = len(sc.Close) > 0 && string(sc.Close) != jsonNull
		listCap = len(sc.List) > 0 && string(sc.List) != jsonNull
		addDirs = len(sc.AdditionalDirectories) > 0 && string(sc.AdditionalDirectories) != jsonNull
	}
	return HandshakeCapabilities{
		ProtocolVersion:       r.ProtocolVersion,
		LoadSession:           caps.LoadSession || caps.LoadSessionSnake,
		Resume:                resume,
		Close:                 closeCap,
		List:                  listCap,
		AdditionalDirectories: addDirs,
		ImagePrompt:           caps.PromptCapabilities.Image,
		EmbeddedContextPrompt: caps.PromptCapabilities.EmbeddedContext,
	}
}

type (
	// SessionNewParams is the params payload for session/new.
	SessionNewParams struct {
		Cwd        string `json:"cwd"`
		McpServers []any  `json:"mcpServers"`
	}

	// SessionLoadParams is the params payload for session/load.
	SessionLoadParams struct {
		SessionID  string `json:"sessionId"`
		Cwd        string `json:"cwd"`
		McpServers []any  `json:"mcpServers"`
	}

	// SessionModel describes one selectable model returned by session/new or session/load.
	SessionModel struct {
		ModelID string `json:"modelId"`
		Name    string `json:"name"`
	}

	// SessionModelsState holds the available models and current model selection.
	SessionModelsState struct {
		AvailableModels []SessionModel `json:"availableModels"`
		CurrentModelID  string         `json:"currentModelId"`
	}

	// SessionMode describes one permission/execution mode advertised in session/new.
	SessionMode struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}

	// SessionModeState holds the mode list and current mode ID.
	SessionModeState struct {
		CurrentModeID  string        `json:"currentModeId"`
		AvailableModes []SessionMode `json:"availableModes"`
	}

	// ConfigSelectOption describes one selectable value for a session config option.
	ConfigSelectOption struct {
		Value       string `json:"value"`
		Name        string `json:"name,omitempty"`
		Description string `json:"description,omitempty"`
	}

	// ConfigOption describes one session setting returned by session/new or session/load.
	ConfigOption struct {
		ID           string               `json:"id"`
		Name         string               `json:"name,omitempty"`
		Category     string               `json:"category,omitempty"`
		Type         string               `json:"type,omitempty"`
		CurrentValue json.RawMessage      `json:"currentValue,omitempty"`
		Options      []ConfigSelectOption `json:"options,omitempty"`
	}

	// SessionStateResult captures session/new and session/load responses.
	// Meta (_meta.antigravity) is read-only extra info and never controls behavior.
	SessionStateResult struct {
		SessionID     string              `json:"sessionId"`
		Models        *SessionModelsState `json:"models,omitempty"`
		Modes         *SessionModeState   `json:"modes,omitempty"`
		ConfigOptions []ConfigOption      `json:"configOptions,omitempty"`
		Meta          json.RawMessage     `json:"_meta,omitempty"`
	}

	// SessionSetConfigOptionParams configures a session option such as model.
	SessionSetConfigOptionParams struct {
		SessionID string `json:"sessionId"`
		ConfigID  string `json:"configId"`
		Value     string `json:"value"`
	}

	// ContentBlock is one unit of an ACP prompt or chunk message.
	ContentBlock struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	}

	// PromptParams is the params payload for session/prompt.
	PromptParams struct {
		SessionID string         `json:"sessionId"`
		Prompt    []ContentBlock `json:"prompt"`
	}

	// PromptUsage holds token counts returned at prompt completion.
	PromptUsage struct {
		InputTokens      int64 `json:"inputTokens"`
		OutputTokens     int64 `json:"outputTokens"`
		CachedReadTokens int64 `json:"cachedReadTokens"`
	}

	// PromptResult is the response result of session/prompt.
	PromptResult struct {
		StopReason string       `json:"stopReason"`
		Usage      *PromptUsage `json:"usage,omitempty"`
	}

	// CancelParams is the params payload for the session/cancel notification.
	CancelParams struct {
		SessionID string `json:"sessionId"`
	}
)

type (
	// SessionUpdateParams wraps one ACP session/update notification.
	SessionUpdateParams struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
		Meta      json.RawMessage `json:"_meta,omitempty"`
	}

	// ToolCallContent is an entry inside a tool_call or tool_call_update content array.
	ToolCallContent struct {
		Type    string        `json:"type"`
		Content *ContentBlock `json:"content,omitempty"`
		Path    string        `json:"path,omitempty"`
		OldText *string       `json:"oldText,omitempty"`
		NewText string        `json:"newText,omitempty"`
	}

	// ToolCallLocation names a workspace file referenced by a tool call.
	ToolCallLocation struct {
		Path string `json:"path"`
		Line *int   `json:"line,omitempty"`
	}

	// PlanEntry is one step of an ACP plan update.
	PlanEntry struct {
		Content  string `json:"content"`
		Priority string `json:"priority,omitempty"`
		Status   string `json:"status,omitempty"`
	}

	// AvailableCommand is one slash command entry in available_commands_update.
	AvailableCommand struct {
		Name        string `json:"name"`
		Description string `json:"description,omitempty"`
	}

	// UpdateWire decodes all supported ACP session/update variants while keeping
	// Meta strictly read-only.
	UpdateWire struct {
		SessionUpdate     string             `json:"sessionUpdate"`
		MessageID         string             `json:"messageId,omitempty"`
		Content           json.RawMessage    `json:"content,omitempty"`
		ToolCallID        string             `json:"toolCallId,omitempty"`
		Name              string             `json:"name,omitempty"`
		Title             string             `json:"title,omitempty"`
		Kind              string             `json:"kind,omitempty"`
		Status            string             `json:"status,omitempty"`
		Locations         []ToolCallLocation `json:"locations,omitempty"`
		RawInput          json.RawMessage    `json:"rawInput,omitempty"`
		RawOutput         json.RawMessage    `json:"rawOutput,omitempty"`
		Entries           []PlanEntry        `json:"entries,omitempty"`
		AvailableCommands []AvailableCommand `json:"availableCommands,omitempty"`
		Meta              json.RawMessage    `json:"_meta,omitempty"`
	}

	// PermissionOption describes one choice in session/request_permission.
	PermissionOption struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	}

	// RequestPermissionParams is the params payload for session/request_permission.
	RequestPermissionParams struct {
		SessionID string             `json:"sessionId"`
		ToolCall  json.RawMessage    `json:"toolCall"`
		Options   []PermissionOption `json:"options"`
		Meta      json.RawMessage    `json:"_meta,omitempty"`
	}

	// RequestPermissionResult is the ACP JSON-RPC result payload for
	// session/request_permission: `{"outcome":"selected","optionId":...}` or
	// `{"outcome":"cancelled"}`.
	RequestPermissionResult struct {
		Outcome  string `json:"outcome"`
		OptionID string `json:"optionId,omitempty"`
	}
)
