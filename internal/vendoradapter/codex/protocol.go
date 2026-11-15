package codex

import "encoding/json"

// JSON-RPC methods used by the Codex adapter (matched against the pinned 0.167.0 schema).
const (
	MethodInitialize         = "initialize"
	MethodInitialized        = "initialized"
	MethodThreadStart        = "thread/start"
	MethodThreadResume       = "thread/resume"
	MethodThreadCompactStart = "thread/compact/start"
	MethodTurnStart          = "turn/start"
	MethodTurnSteer          = "turn/steer"
	MethodTurnInterrupt      = "turn/interrupt"
	MethodReviewStart        = "review/start"
	MethodModelList          = "model/list"
	MethodRateLimitsRead     = "account/rateLimits/read"

	MethodThreadStarted         = "thread/started"
	MethodThreadNameUpdated     = "thread/name/updated"
	MethodTurnStarted           = "turn/started"
	MethodTurnCompleted         = "turn/completed"
	MethodTurnPlanUpdated       = "turn/plan/updated"
	MethodTokenUsageUpdated     = "thread/tokenUsage/updated"
	MethodRateLimitsUpdated     = "account/rateLimits/updated"
	MethodItemStarted           = "item/started"
	MethodItemCompleted         = "item/completed"
	MethodAgentMessageDelta     = "item/agentMessage/delta"
	MethodReasoningSummaryDelta = "item/reasoning/summaryTextDelta"
	MethodReasoningTextDelta    = "item/reasoning/textDelta"
	MethodCommandOutputDelta    = "item/commandExecution/outputDelta"
	MethodFileChangeOutputDelta = "item/fileChange/outputDelta"
	MethodPlanDelta             = "item/plan/delta"
	MethodError                 = "error"
	MethodServerRequestResolved = "serverRequest/resolved"

	MethodCommandApproval     = "item/commandExecution/requestApproval"
	MethodFileChangeApproval  = "item/fileChange/requestApproval"
	MethodPermissionsApproval = "item/permissions/requestApproval"
	MethodRequestUserInput    = "item/tool/requestUserInput"
)

// Turn status, item types, item statuses, approval decisions, and review target constants.
const (
	TurnStatusInProgress  = "inProgress"
	TurnStatusCompleted   = "completed"
	TurnStatusInterrupted = "interrupted"
	TurnStatusFailed      = "failed"

	ItemTypeUserMessage       = "userMessage"
	ItemTypeAgentMessage      = "agentMessage"
	ItemTypeReasoning         = "reasoning"
	ItemTypeCommandExecution  = "commandExecution"
	ItemTypeFileChange        = "fileChange"
	ItemTypeMcpToolCall       = "mcpToolCall"
	ItemTypePlan              = "plan"
	ItemTypeContextCompaction = "contextCompaction"
	ItemTypeEnteredReview     = "enteredReviewMode"
	ItemTypeExitedReview      = "exitedReviewMode"

	ItemStatusInProgress = "inProgress"
	ItemStatusCompleted  = "completed"
	ItemStatusFailed     = "failed"
	ItemStatusDeclined   = "declined"

	DecisionAccept                        = "accept"
	DecisionAcceptForSession              = "acceptForSession"
	DecisionAcceptWithExecpolicyAmendment = "acceptWithExecpolicyAmendment"
	DecisionDecline                       = "decline"
	DecisionCancel                        = "cancel"

	PermissionScopeTurn    = "turn"
	PermissionScopeSession = "session"

	ReviewTargetUncommitted = "uncommittedChanges"
	ReviewTargetBaseBranch  = "baseBranch"
	ReviewTargetCommit      = "commit"
	ReviewTargetCustom      = "custom"
	ReviewDeliveryInline    = "inline"
)

type (
	// ClientInfo identifies Fugue in initialize.
	ClientInfo struct {
		Name    string  `json:"name"`
		Title   *string `json:"title,omitempty"`
		Version string  `json:"version"`
	}
	// InitializeCapabilities declares client capabilities during initialize.
	InitializeCapabilities struct {
		ExperimentalAPI bool `json:"experimentalApi"`
	}
	// InitializeParams is the request payload for initialize.
	InitializeParams struct {
		ClientInfo   ClientInfo              `json:"clientInfo"`
		Capabilities *InitializeCapabilities `json:"capabilities,omitempty"`
	}
	// InitializeResponse is the response payload for initialize.
	InitializeResponse struct {
		UserAgent      string `json:"userAgent"`
		CodexHome      string `json:"codexHome"`
		PlatformFamily string `json:"platformFamily"`
		PlatformOS     string `json:"platformOs"`
	}
)

type (
	// ThreadStartParams is the request payload for thread/start.
	// ApprovalPolicy and Sandbox are omitted so Codex's policy stays at its default (VR-04).
	ThreadStartParams struct {
		Cwd                   string `json:"cwd,omitempty"`
		Model                 string `json:"model,omitempty"`
		DeveloperInstructions string `json:"developerInstructions,omitempty"`
	}
	// ThreadResumeParams is the request payload for thread/resume.
	ThreadResumeParams struct {
		ThreadID              string `json:"threadId"`
		Cwd                   string `json:"cwd,omitempty"`
		Model                 string `json:"model,omitempty"`
		DeveloperInstructions string `json:"developerInstructions,omitempty"`
	}
	// Thread holds minimal thread identity read from thread/start and thread/resume.
	Thread struct {
		ID   string  `json:"id"`
		Name *string `json:"name"`
		Cwd  string  `json:"cwd"`
	}
	// ThreadStartResponse is the response payload for thread/start and thread/resume.
	ThreadStartResponse struct {
		Thread          Thread  `json:"thread"`
		Model           string  `json:"model"`
		ApprovalPolicy  string  `json:"approvalPolicy"`
		ReasoningEffort *string `json:"reasoningEffort"`
	}
)

// UserInputElement is a text input element for turn/start and turn/steer.
type UserInputElement struct {
	Type         string            `json:"type"`
	Text         string            `json:"text"`
	TextElements []json.RawMessage `json:"text_elements"`
}

// NewTextInput constructs a single text UserInputElement.
func NewTextInput(text string) UserInputElement {
	return UserInputElement{
		Type:         "text",
		Text:         text,
		TextElements: []json.RawMessage{},
	}
}

type (
	// TurnStartParams is the request payload for turn/start.
	TurnStartParams struct {
		ThreadID string             `json:"threadId"`
		Input    []UserInputElement `json:"input"`
		Model    string             `json:"model,omitempty"`
	}
	// TurnError describes a turn error in Turn or error notifications.
	TurnError struct {
		Message           string          `json:"message"`
		CodexErrorInfo    json.RawMessage `json:"codexErrorInfo"`
		AdditionalDetails *string         `json:"additionalDetails"`
	}
	// Turn is the minimal turn representation in turn/start, turn/started, and turn/completed.
	Turn struct {
		ID     string     `json:"id"`
		Status string     `json:"status"`
		Error  *TurnError `json:"error"`
	}
	// TurnStartResponse is the response payload for turn/start.
	TurnStartResponse struct {
		Turn Turn `json:"turn"`
	}
	// TurnSteerParams is the request payload for turn/steer.
	TurnSteerParams struct {
		ThreadID       string             `json:"threadId"`
		ExpectedTurnID string             `json:"expectedTurnId"`
		Input          []UserInputElement `json:"input"`
	}
	// TurnSteerResponse is the response payload for turn/steer.
	TurnSteerResponse struct {
		TurnID string `json:"turnId"`
	}
	// TurnInterruptParams is the request payload for turn/interrupt.
	TurnInterruptParams struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
	}
	// ThreadCompactStartParams is the request payload for thread/compact/start.
	ThreadCompactStartParams struct {
		ThreadID string `json:"threadId"`
	}
)

type (
	// ReviewTarget selects what review/start reviews.
	ReviewTarget struct {
		Type         string `json:"type"`
		Branch       string `json:"branch,omitempty"`
		SHA          string `json:"sha,omitempty"`
		Instructions string `json:"instructions,omitempty"`
	}
	// ReviewStartParams is the request payload for review/start.
	ReviewStartParams struct {
		ThreadID string       `json:"threadId"`
		Target   ReviewTarget `json:"target"`
		Delivery string       `json:"delivery,omitempty"`
	}
	// ReviewStartResponse is the response payload for review/start.
	ReviewStartResponse struct {
		Turn           Turn   `json:"turn"`
		ReviewThreadID string `json:"reviewThreadId"`
	}
	// ModelListParams is the request payload for model/list.
	ModelListParams struct {
		Cursor *string `json:"cursor,omitempty"`
		Limit  *int    `json:"limit,omitempty"`
	}
	// ModelEntry is one model entry in model/list.
	ModelEntry struct {
		ID          string `json:"id"`
		Model       string `json:"model"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
		Hidden      bool   `json:"hidden"`
		IsDefault   bool   `json:"isDefault"`
	}
	// ModelListResponse is the response payload for model/list.
	ModelListResponse struct {
		Data       []ModelEntry `json:"data"`
		NextCursor *string      `json:"nextCursor"`
	}
)

type (
	// RateLimitWindow is one rate-limit window from Codex.
	RateLimitWindow struct {
		UsedPercent        int64  `json:"usedPercent"`
		WindowDurationMins *int64 `json:"windowDurationMins"`
		ResetsAt           *int64 `json:"resetsAt"`
	}
	// RateLimitSnapshot is the plan's rate-limit state at a point in time.
	RateLimitSnapshot struct {
		LimitID              *string          `json:"limitId"`
		LimitName            *string          `json:"limitName"`
		PlanType             *string          `json:"planType"`
		Primary              *RateLimitWindow `json:"primary"`
		Secondary            *RateLimitWindow `json:"secondary"`
		RateLimitReachedType *string          `json:"rateLimitReachedType"`
	}
	// RateLimitsReadResponse is the response payload for account/rateLimits/read.
	RateLimitsReadResponse struct {
		RateLimits          RateLimitSnapshot            `json:"rateLimits"`
		RateLimitsByLimitID map[string]RateLimitSnapshot `json:"rateLimitsByLimitId"`
	}
	// RateLimitsUpdatedNotification is the notification payload for account/rateLimits/updated.
	RateLimitsUpdatedNotification struct {
		RateLimits RateLimitSnapshot `json:"rateLimits"`
	}
)

type (
	// TokenUsageBreakdown holds token counts for a turn or thread total.
	TokenUsageBreakdown struct {
		TotalTokens           int64 `json:"totalTokens"`
		InputTokens           int64 `json:"inputTokens"`
		CachedInputTokens     int64 `json:"cachedInputTokens"`
		OutputTokens          int64 `json:"outputTokens"`
		ReasoningOutputTokens int64 `json:"reasoningOutputTokens"`
	}
	// ThreadTokenUsage holds total and last-turn token counts.
	ThreadTokenUsage struct {
		Total              TokenUsageBreakdown `json:"total"`
		Last               TokenUsageBreakdown `json:"last"`
		ModelContextWindow *int64              `json:"modelContextWindow"`
	}
	// TokenUsageUpdatedNotification is the notification payload for thread/tokenUsage/updated.
	TokenUsageUpdatedNotification struct {
		ThreadID   string           `json:"threadId"`
		TurnID     string           `json:"turnId"`
		TokenUsage ThreadTokenUsage `json:"tokenUsage"`
	}
)

type (
	// ThreadStartedNotification is the notification payload for thread/started.
	ThreadStartedNotification struct {
		Thread Thread `json:"thread"`
	}
	// ThreadNameUpdatedNotification is the notification payload for thread/name/updated.
	ThreadNameUpdatedNotification struct {
		ThreadID   string  `json:"threadId"`
		ThreadName *string `json:"threadName"`
	}
	// TurnStartedNotification is the notification payload for turn/started.
	TurnStartedNotification struct {
		ThreadID string `json:"threadId"`
		Turn     Turn   `json:"turn"`
	}
	// TurnCompletedNotification is the notification payload for turn/completed.
	TurnCompletedNotification struct {
		ThreadID string `json:"threadId"`
		Turn     Turn   `json:"turn"`
	}
	// TurnPlanStep is one step in turn/plan/updated.
	TurnPlanStep struct {
		Step   string `json:"step"`
		Status string `json:"status"`
	}
	// TurnPlanUpdatedNotification is the notification payload for turn/plan/updated.
	TurnPlanUpdatedNotification struct {
		ThreadID    string         `json:"threadId"`
		TurnID      string         `json:"turnId"`
		Explanation *string        `json:"explanation"`
		Plan        []TurnPlanStep `json:"plan"`
	}
	// ErrorNotification is the notification payload for error.
	ErrorNotification struct {
		Error     TurnError `json:"error"`
		WillRetry bool      `json:"willRetry"`
		ThreadID  string    `json:"threadId"`
		TurnID    string    `json:"turnId"`
	}
	// ItemNotification is the notification payload for item/started and item/completed.
	ItemNotification struct {
		ThreadID string          `json:"threadId"`
		TurnID   string          `json:"turnId"`
		Item     json.RawMessage `json:"item"`
	}
	// ItemHeader identifies the type and ID of a ThreadItem.
	ItemHeader struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
)

type (
	// UserMessageItem is the userMessage variant of ThreadItem.
	UserMessageItem struct {
		ID      string            `json:"id"`
		Content []json.RawMessage `json:"content"`
	}
	// AgentMessageItem is the agentMessage variant of ThreadItem.
	AgentMessageItem struct {
		ID    string  `json:"id"`
		Text  string  `json:"text"`
		Phase *string `json:"phase"`
	}
	// ReasoningItem is the reasoning variant of ThreadItem.
	ReasoningItem struct {
		ID      string   `json:"id"`
		Summary []string `json:"summary"`
		Content []string `json:"content"`
	}
	// CommandAction is one parsed command action on a commandExecution item.
	CommandAction struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	// CommandExecutionItem is the commandExecution variant of ThreadItem.
	CommandExecutionItem struct {
		ID               string          `json:"id"`
		Command          string          `json:"command"`
		Cwd              string          `json:"cwd"`
		Status           string          `json:"status"`
		CommandActions   []CommandAction `json:"commandActions"`
		AggregatedOutput *string         `json:"aggregatedOutput"`
		ExitCode         *int            `json:"exitCode"`
		DurationMs       *int64          `json:"durationMs"`
	}
	// PatchChangeKind describes whether a file change is add, delete, or update.
	PatchChangeKind struct {
		Type     string  `json:"type"`
		MovePath *string `json:"move_path"`
	}
	// FileUpdateChange is one file entry inside a fileChange ThreadItem.
	FileUpdateChange struct {
		Path string          `json:"path"`
		Kind PatchChangeKind `json:"kind"`
		Diff string          `json:"diff"`
	}
	// FileChangeItem is the fileChange variant of ThreadItem.
	FileChangeItem struct {
		ID      string             `json:"id"`
		Changes []FileUpdateChange `json:"changes"`
		Status  string             `json:"status"`
	}
	// McpToolCallError is the error object on an mcpToolCall ThreadItem.
	McpToolCallError struct {
		Message string `json:"message"`
	}
	// McpToolCallItem is the mcpToolCall variant of ThreadItem.
	McpToolCallItem struct {
		ID         string            `json:"id"`
		Server     string            `json:"server"`
		Tool       string            `json:"tool"`
		Status     string            `json:"status"`
		Arguments  json.RawMessage   `json:"arguments"`
		Result     json.RawMessage   `json:"result"`
		Error      *McpToolCallError `json:"error"`
		DurationMs *int64            `json:"durationMs"`
	}
	// PlanItem is the plan variant of ThreadItem.
	PlanItem struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	// ReviewModeItem is the enteredReviewMode and exitedReviewMode variant of ThreadItem.
	ReviewModeItem struct {
		ID     string `json:"id"`
		Review string `json:"review"`
	}
)

type (
	// DeltaNotification is the payload for item/{agentMessage,commandExecution/output,fileChange/output,plan}/delta.
	DeltaNotification struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		ItemID   string `json:"itemId"`
		Delta    string `json:"delta"`
	}
	// ReasoningSummaryDeltaNotification is item/reasoning/summaryTextDelta.
	ReasoningSummaryDeltaNotification struct {
		ThreadID     string `json:"threadId"`
		TurnID       string `json:"turnId"`
		ItemID       string `json:"itemId"`
		Delta        string `json:"delta"`
		SummaryIndex int64  `json:"summaryIndex"`
	}
	// ReasoningTextDeltaNotification is item/reasoning/textDelta.
	ReasoningTextDeltaNotification struct {
		ThreadID     string `json:"threadId"`
		TurnID       string `json:"turnId"`
		ItemID       string `json:"itemId"`
		Delta        string `json:"delta"`
		ContentIndex int64  `json:"contentIndex"`
	}
	// ServerRequestResolvedNotification is serverRequest/resolved.
	ServerRequestResolvedNotification struct {
		ThreadID  string          `json:"threadId"`
		RequestID json.RawMessage `json:"requestId"`
	}
)
