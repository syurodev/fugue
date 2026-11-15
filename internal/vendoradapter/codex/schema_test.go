package codex

// VR-E02: verify that every client request method, client notification method,
// server notification method, server request method, ThreadItem variant, and
// field read/written by the Codex adapter exists in the pinned 0.167.0 JSON
// schema with the expected type.

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	schemaTypeString  = "string"
	schemaTypeObject  = "object"
	schemaTypeInteger = "integer"
	schemaTypeArray   = "array"
)

type schemaDoc struct {
	Definitions map[string]json.RawMessage `json:"definitions"`
}

type schemaNode struct {
	BooleanSchema bool
	Type          any                   `json:"type"`
	Enum          []string              `json:"enum"`
	Properties    map[string]schemaNode `json:"properties"`
	Items         *schemaNode           `json:"items"`
	Ref           string                `json:"$ref"`
	OneOf         []schemaNode          `json:"oneOf"`
	AnyOf         []schemaNode          `json:"anyOf"`
	AllOf         []schemaNode          `json:"allOf"`
}

func (s *schemaNode) UnmarshalJSON(data []byte) error {
	var b bool
	if json.Unmarshal(data, &b) == nil {
		s.BooleanSchema = b
		return nil
	}
	type rawSchemaNode schemaNode
	var raw rawSchemaNode
	if err := json.Unmarshal(data, &raw); err != nil {
		return errcode.Wrap(errcode.InvalidArgument, "decode schema node", err)
	}
	*s = schemaNode(raw)
	return nil
}

func loadPinnedSchema(t *testing.T) map[string]schemaNode {
	t.Helper()
	raw := readFile(t, filepath.Join("testdata", "schema", "codex_app_server_protocol.schemas.json"))
	var doc schemaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode pinned schema: %v", err)
	}
	flat := make(map[string]schemaNode, len(doc.Definitions)*4)
	for k, v := range doc.Definitions {
		if k == "v2" {
			var v2Defs map[string]schemaNode
			if err := json.Unmarshal(v, &v2Defs); err != nil {
				t.Fatalf("decode v2 definitions: %v", err)
			}
			for vk := range v2Defs {
				flat[vk] = v2Defs[vk]
				flat["v2/"+vk] = v2Defs[vk]
			}
			continue
		}
		var node schemaNode
		if err := json.Unmarshal(v, &node); err != nil {
			t.Fatalf("decode definition %s: %v", k, err)
		}
		flat[k] = node
	}
	return flat
}

func resolveSchemaRef(defs map[string]schemaNode, node *schemaNode) schemaNode {
	if node.Ref != "" {
		name := strings.TrimPrefix(node.Ref, "#/definitions/")
		if target, ok := defs[name]; ok {
			return target
		}
	}
	if len(node.AllOf) == 1 {
		return resolveSchemaRef(defs, &node.AllOf[0])
	}
	return *node
}

func collectUnionMethods(defs map[string]schemaNode, unionName string) map[string]schemaNode {
	root, ok := defs[unionName]
	if !ok {
		return nil
	}
	out := make(map[string]schemaNode)
	for i := range root.OneOf {
		resolved := resolveSchemaRef(defs, &root.OneOf[i])
		mProp, hasMethod := resolved.Properties["method"]
		if !hasMethod || len(mProp.Enum) == 0 {
			continue
		}
		out[mProp.Enum[0]] = resolved
	}
	return out
}

func assertPropertySpecs(t *testing.T, defs map[string]schemaNode, specs []string) {
	t.Helper()
	for _, spec := range specs {
		lhs, wantType, ok := strings.Cut(spec, ":")
		if !ok {
			t.Fatalf("malformed spec %q", spec)
		}
		typeName, field, ok := strings.Cut(lhs, ".")
		if !ok {
			t.Fatalf("malformed spec %q", spec)
		}
		node, ok := defs[typeName]
		if !ok {
			t.Fatalf("schema definition %q not found", typeName)
		}
		prop, ok := node.Properties[field]
		if !ok {
			t.Fatalf("%s.%s does not exist in pinned schema", typeName, field)
		}
		if !schemaMatchesType(defs, &prop, wantType) {
			t.Errorf("%s.%s does not match expected type %q in pinned schema (got %+v)", typeName, field, wantType, prop)
		}
	}
}

func schemaMatchesType(defs map[string]schemaNode, prop *schemaNode, wantType string) bool {
	resolved := resolveSchemaRef(defs, prop)
	if directTypeMatches(resolved.Type, wantType) {
		return true
	}
	if compositeTypeMatches(defs, &resolved, wantType) {
		return true
	}
	return (wantType == schemaTypeString && len(resolved.Enum) > 0) ||
		(wantType == schemaTypeObject && len(resolved.Properties) > 0)
}

func directTypeMatches(rawType any, wantType string) bool {
	switch v := rawType.(type) {
	case string:
		return v == wantType
	case []any:
		for _, elem := range v {
			if s, ok := elem.(string); ok && s == wantType {
				return true
			}
		}
	}
	return false
}

func compositeTypeMatches(defs map[string]schemaNode, node *schemaNode, wantType string) bool {
	for i := range node.AnyOf {
		if schemaMatchesType(defs, &node.AnyOf[i], wantType) {
			return true
		}
	}
	for i := range node.OneOf {
		if schemaMatchesType(defs, &node.OneOf[i], wantType) {
			return true
		}
	}
	for i := range node.AllOf {
		if schemaMatchesType(defs, &node.AllOf[i], wantType) {
			return true
		}
	}
	return false
}

func TestCodexDecodersMatchThePinnedSchema(t *testing.T) {
	t.Parallel()
	defs := loadPinnedSchema(t)

	assertMethodsExistInSchema(t, defs)
	assertRequestAndResponseFieldsMatchSchema(t, defs)
	assertNotificationFieldsMatchSchema(t, defs)
	assertThreadItemVariantsMatchSchema(t, defs)
	assertServerRequestFieldsMatchSchema(t, defs)
}

func assertMethodsExistInSchema(t *testing.T, defs map[string]schemaNode) {
	t.Helper()
	clientReqs := collectUnionMethods(defs, "ClientRequest")
	for _, m := range []string{
		MethodInitialize, MethodThreadStart, MethodThreadResume, MethodThreadCompactStart,
		MethodTurnStart, MethodTurnSteer, MethodTurnInterrupt, MethodReviewStart,
		MethodModelList, MethodRateLimitsRead,
	} {
		if _, ok := clientReqs[m]; !ok {
			t.Errorf("client request method %q missing from ClientRequest in pinned schema", m)
		}
	}

	clientNotifs := collectUnionMethods(defs, "ClientNotification")
	if _, ok := clientNotifs[MethodInitialized]; !ok {
		t.Errorf("client notification %q missing from ClientNotification in pinned schema", MethodInitialized)
	}

	serverNotifs := collectUnionMethods(defs, "ServerNotification")
	for _, m := range []string{
		MethodThreadStarted, MethodThreadNameUpdated, MethodTurnStarted, MethodTurnCompleted,
		MethodTurnPlanUpdated, MethodTokenUsageUpdated, MethodRateLimitsUpdated,
		MethodItemStarted, MethodItemCompleted, MethodAgentMessageDelta,
		MethodReasoningSummaryDelta, MethodReasoningTextDelta, MethodCommandOutputDelta,
		MethodFileChangeOutputDelta, MethodPlanDelta, MethodError, MethodServerRequestResolved,
	} {
		if _, ok := serverNotifs[m]; !ok {
			t.Errorf("server notification %q missing from ServerNotification in pinned schema", m)
		}
	}

	serverReqs := collectUnionMethods(defs, "ServerRequest")
	for _, m := range []string{
		MethodCommandApproval, MethodFileChangeApproval,
		MethodPermissionsApproval, MethodRequestUserInput,
	} {
		if _, ok := serverReqs[m]; !ok {
			t.Errorf("server request %q missing from ServerRequest in pinned schema", m)
		}
	}
}

func assertRequestAndResponseFieldsMatchSchema(t *testing.T, defs map[string]schemaNode) {
	t.Helper()
	assertPropertySpecs(t, defs, []string{
		"InitializeParams.clientInfo:object",
		"InitializeParams.capabilities:object",
		"ClientInfo.name:string",
		"ClientInfo.version:string",
		"InitializeResponse.userAgent:string",
		"InitializeResponse.codexHome:string",
		"ThreadStartParams.cwd:string",
		"ThreadStartParams.model:string",
		"ThreadResumeParams.threadId:string",
		"ThreadResumeParams.cwd:string",
		"ThreadResumeParams.model:string",
		"ThreadStartResponse.thread:object",
		"ThreadStartResponse.model:string",
		"ThreadStartResponse.approvalPolicy:string",
		"Thread.id:string",
		"Thread.cwd:string",
		"TurnStartParams.threadId:string",
		"TurnStartParams.input:array",
		"TurnStartResponse.turn:object",
		"Turn.id:string",
		"Turn.status:string",
		"Turn.error:object",
		"TurnSteerParams.threadId:string",
		"TurnSteerParams.expectedTurnId:string",
		"TurnSteerParams.input:array",
		"TurnSteerResponse.turnId:string",
		"TurnInterruptParams.threadId:string",
		"TurnInterruptParams.turnId:string",
		"ThreadCompactStartParams.threadId:string",
		"ReviewStartParams.threadId:string",
		"ReviewStartParams.target:object",
		"ReviewStartResponse.turn:object",
		"ReviewStartResponse.reviewThreadId:string",
		"ModelListResponse.data:array",
		"GetAccountRateLimitsResponse.rateLimits:object",
		"RateLimitSnapshot.primary:object",
		"RateLimitSnapshot.secondary:object",
		"RateLimitWindow.usedPercent:integer",
		"RateLimitWindow.resetsAt:integer",
	})
}

func assertNotificationFieldsMatchSchema(t *testing.T, defs map[string]schemaNode) {
	t.Helper()
	assertPropertySpecs(t, defs, []string{
		"TurnStartedNotification.threadId:string",
		"TurnStartedNotification.turn:object",
		"TurnCompletedNotification.threadId:string",
		"TurnCompletedNotification.turn:object",
		"TurnPlanUpdatedNotification.threadId:string",
		"TurnPlanUpdatedNotification.turnId:string",
		"TurnPlanUpdatedNotification.plan:array",
		"ThreadTokenUsageUpdatedNotification.threadId:string",
		"ThreadTokenUsageUpdatedNotification.turnId:string",
		"ThreadTokenUsageUpdatedNotification.tokenUsage:object",
		"ThreadTokenUsage.total:object",
		"ThreadTokenUsage.last:object",
		"TokenUsageBreakdown.totalTokens:integer",
		"TokenUsageBreakdown.inputTokens:integer",
		"TokenUsageBreakdown.cachedInputTokens:integer",
		"TokenUsageBreakdown.outputTokens:integer",
		"AccountRateLimitsUpdatedNotification.rateLimits:object",
		"ErrorNotification.error:object",
		"ErrorNotification.willRetry:boolean",
		"ErrorNotification.threadId:string",
		"ErrorNotification.turnId:string",
		"ItemStartedNotification.threadId:string",
		"ItemStartedNotification.turnId:string",
		"ItemStartedNotification.item:object",
		"ItemCompletedNotification.threadId:string",
		"ItemCompletedNotification.turnId:string",
		"ItemCompletedNotification.item:object",
		"AgentMessageDeltaNotification.itemId:string",
		"AgentMessageDeltaNotification.delta:string",
		"ReasoningSummaryTextDeltaNotification.itemId:string",
		"ReasoningSummaryTextDeltaNotification.delta:string",
		"ReasoningTextDeltaNotification.itemId:string",
		"ReasoningTextDeltaNotification.delta:string",
		"CommandExecutionOutputDeltaNotification.itemId:string",
		"CommandExecutionOutputDeltaNotification.delta:string",
	})
}

func assertThreadItemVariantsMatchSchema(t *testing.T, defs map[string]schemaNode) {
	t.Helper()
	threadItem, ok := defs["ThreadItem"]
	if !ok {
		t.Fatal("ThreadItem missing from pinned schema")
	}
	variants := make(map[string]schemaNode)
	for i := range threadItem.OneOf {
		resolved := resolveSchemaRef(defs, &threadItem.OneOf[i])
		if tp, has := resolved.Properties["type"]; has && len(tp.Enum) > 0 {
			variants[tp.Enum[0]] = resolved
		}
	}
	for _, want := range []string{
		ItemTypeUserMessage, ItemTypeAgentMessage, ItemTypeReasoning,
		ItemTypeCommandExecution, ItemTypeFileChange, ItemTypeMcpToolCall,
		ItemTypePlan, ItemTypeContextCompaction, ItemTypeEnteredReview, ItemTypeExitedReview,
	} {
		if _, exists := variants[want]; !exists {
			t.Errorf("ThreadItem variant %q missing in pinned schema", want)
		}
	}
	cmdItem := variants[ItemTypeCommandExecution]
	for field, kind := range map[string]string{
		"id": schemaTypeString, "command": schemaTypeString, "cwd": schemaTypeString,
		keyStatus: schemaTypeString, "aggregatedOutput": schemaTypeString,
		"exitCode": schemaTypeInteger, "durationMs": schemaTypeInteger,
	} {
		prop := cmdItem.Properties[field]
		if !schemaMatchesType(defs, &prop, kind) {
			t.Errorf("commandExecution.%s type mismatch in pinned schema", field)
		}
	}
}

func assertServerRequestFieldsMatchSchema(t *testing.T, defs map[string]schemaNode) {
	t.Helper()
	assertPropertySpecs(t, defs, []string{
		"CommandExecutionRequestApprovalParams.threadId:string",
		"CommandExecutionRequestApprovalParams.turnId:string",
		"CommandExecutionRequestApprovalParams.itemId:string",
		"CommandExecutionRequestApprovalParams.command:string",
		"CommandExecutionRequestApprovalParams.cwd:string",
		"CommandExecutionRequestApprovalParams.reason:string",
		"CommandExecutionRequestApprovalParams.proposedExecpolicyAmendment:array",
		"FileChangeRequestApprovalParams.threadId:string",
		"FileChangeRequestApprovalParams.turnId:string",
		"FileChangeRequestApprovalParams.itemId:string",
		"FileChangeRequestApprovalParams.reason:string",
		"PermissionsRequestApprovalParams.threadId:string",
		"PermissionsRequestApprovalParams.turnId:string",
		"PermissionsRequestApprovalParams.itemId:string",
		"PermissionsRequestApprovalParams.permissions:object",
		"PermissionsRequestApprovalResponse.permissions:object",
		"PermissionsRequestApprovalResponse.scope:string",
		"ToolRequestUserInputParams.threadId:string",
		"ToolRequestUserInputParams.turnId:string",
		"ToolRequestUserInputParams.itemId:string",
		"ToolRequestUserInputParams.questions:array",
		"ToolRequestUserInputResponse.answers:object",
	})

	for _, defName := range []string{"FileChangeApprovalDecision", "CommandExecutionApprovalDecision"} {
		decNode := defs[defName]
		var enums []string
		enums = append(enums, decNode.Enum...)
		for i := range decNode.OneOf {
			enums = append(enums, decNode.OneOf[i].Enum...)
		}
		for _, want := range []string{DecisionAccept, DecisionAcceptForSession, DecisionDecline, DecisionCancel} {
			if !slices.Contains(enums, want) {
				t.Errorf("%s missing %q in pinned schema", defName, want)
			}
		}
	}
	foundExecpolicy := false
	for i := range defs["CommandExecutionApprovalDecision"].OneOf {
		variant := defs["CommandExecutionApprovalDecision"].OneOf[i]
		if payload, ok := variant.Properties[DecisionAcceptWithExecpolicyAmendment]; ok {
			arr := payload.Properties["execpolicy_amendment"]
			if schemaMatchesType(defs, &arr, schemaTypeArray) {
				foundExecpolicy = true
			}
		}
	}
	if !foundExecpolicy {
		t.Errorf("CommandExecutionApprovalDecision missing %s.execpolicy_amendment array in pinned schema", DecisionAcceptWithExecpolicyAmendment)
	}
}
