package app

import (
	"slices"
	"testing"

	pureread "github.com/syurodev/fugue/internal/tools/pure_read"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/kernel"
)

// Literal values the tool executor tests repeat.
const (
	toolCallID     = "call-1"
	toolAttempt    = "ta-1"
	toolArgsRef    = "args-1"
	toolResponseID = "resp-1"
)

// toolRunEvents folds a Trace A run up to the tool dispatch: the model answered
// tool_use, the call passed its check and was dispatched under fence 9.
func toolRunEvents() []kernel.Event {
	return append(modelAnsweredEvents(), toolDispatchedEvents()...)
}

// modelAnsweredEvents fold the model half: plan, response, usage.
func modelAnsweredEvents() []kernel.Event {
	plan := &kernel.ModelRequestPlannedEvent{
		Envelope:              runEventEnvelope(1, "cmd-1"),
		InvocationID:          "m1",
		Purpose:               purposeTurn,
		FirstAttemptID:        "a1",
		ModelRef:              "faux-model",
		ContextRef:            "ctx-1",
		ToolDeclarationRefs:   nil,
		EstimatedBudgetTokens: 0,
	}
	response := &kernel.ModelResponseRecordedEvent{
		Envelope:     runEventEnvelope(4, "cmd-4"),
		AttemptID:    "a1",
		InvocationID: "m1",
		ResponseRef:  "resp-1",
		UsageStatus:  "known",
		FinishReason: kernel.FinishReasonToolUse,
	}
	usage := &kernel.BudgetObservedEvent{
		Envelope:     runEventEnvelope(5, "cmd-4"),
		AttemptID:    "a1",
		WindowID:     "",
		TokensUsed:   1,
		DurationUsed: 0,
		Basis:        "provider_reported",
	}
	return []kernel.Event{plan, response, usage}
}

// toolDispatchedEvents fold the tool half: plan, check, ready, dispatch.
func toolDispatchedEvents() []kernel.Event {
	toolPlan := &kernel.ToolCallPlannedEvent{
		Envelope:          runEventEnvelope(6, "cmd-4"),
		ToolCallID:        toolCallID,
		SourcePosition:    0,
		DescriptorVersion: descriptorPureRead,
		EffectClass:       kernel.EffectClassPureRead,
		FirstAttemptID:    toolAttempt,
		InputRef:          toolArgsRef,
		Terminates:        false,
		RepeatsPrevious:   false,
	}
	check := &kernel.ToolCheckStartedEvent{
		Envelope:        runEventEnvelope(7, "cmd-5"),
		ToolCallID:      toolCallID,
		AttemptID:       toolAttempt,
		CheckGeneration: 1,
		FrontierCursor:  0,
	}
	ready := &kernel.ToolReadyRecordedEvent{
		Envelope:                 runEventEnvelope(8, "cmd-6"),
		ToolCallID:               toolCallID,
		CheckGeneration:          1,
		ValidatedArgsRef:         toolArgsRef,
		GateObservation:          "allowed",
		OneUseApprovalReceiptRef: "",
	}
	dispatch := &kernel.ToolDispatchStartedEvent{
		Envelope:          runEventEnvelope(9, "cmd-7"),
		ToolCallID:        toolCallID,
		AttemptID:         toolAttempt,
		CheckGeneration:   1,
		ExecutorFence:     9,
		Token:             1,
		ReceiptRef:        "tool-auth-1",
		BudgetReservation: "",
	}
	return []kernel.Event{toolPlan, check, ready, dispatch}
}

func toolWork() *coordinator.Work {
	dispatch := &kernel.ToolDispatchStartedEvent{
		Envelope:          kernel.Envelope{},
		ToolCallID:        toolCallID,
		AttemptID:         toolAttempt,
		CheckGeneration:   1,
		ExecutorFence:     9,
		Token:             1,
		ReceiptRef:        "tool-auth-1",
		BudgetReservation: "",
	}
	lease := coordinator.Lease{
		SessionID: "s1", RunID: "r1", InvocationID: toolCallID, AttemptID: toolAttempt,
		AdmissionToken: 1, OwnerIncarnation: 1, ExecutorFence: 9,
	}
	return &coordinator.Work{Lease: lease, Dispatch: dispatch}
}

func seededToolPayloads(t *testing.T) map[string][]byte {
	t.Helper()
	args, err := codec.MarshalCanonical(pureread.Args{Key: "k"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	payloads := seededContext(t, []contextItem{{Role: contextRoleUser, Ref: execInputRef, Content: []byte("the input")}})
	payloads[toolArgsRef] = args
	payloads[toolResponseID] = []byte("the response")
	return payloads
}

func TestToolCheckUsesPrepareAndRecordsTheGate(t *testing.T) {
	t.Parallel()

	tool := pureread.New(map[string][]byte{"k": []byte("the value")})
	executor := &toolExecutor{tool: tool, ids: execGen("check"), source: newFakeSource(nil, seededToolPayloads(t))}

	ref, err := executor.check(t.Context(), "s1", &kernel.ToolCallPlannedEvent{Envelope: kernel.Envelope{}, ToolCallID: toolCallID, SourcePosition: 0, DescriptorVersion: descriptorPureRead, EffectClass: kernel.EffectClassPureRead, FirstAttemptID: toolAttempt, InputRef: toolArgsRef, Terminates: false, RepeatsPrevious: false})
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if ref != "args-1" {
		t.Fatalf("validated args ref %q, want the sealed args payload", ref)
	}
	if tool.Snapshot().PrepareCalls != 1 || tool.Snapshot().ReadyPlans != 1 {
		t.Fatalf("prepare did not run exactly once: %+v", tool.Snapshot())
	}
}

func TestInvalidToolArgsNeverDispatch(t *testing.T) {
	t.Parallel()

	payloads := seededToolPayloads(t)
	blank, err := codec.MarshalCanonical(pureread.Args{Key: "   "})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	payloads["args-blank"] = blank
	payloads["args-broken"] = []byte(`not json`)

	tool := pureread.New(map[string][]byte{"k": []byte("the value")})
	executor := &toolExecutor{tool: tool, ids: execGen("invalid"), source: newFakeSource(nil, payloads)}

	for _, id := range []string{"args-blank", "args-broken", "missing"} {
		plan := &kernel.ToolCallPlannedEvent{
			Envelope:          kernel.Envelope{},
			ToolCallID:        execCallID,
			SourcePosition:    0,
			DescriptorVersion: descriptorPureRead,
			EffectClass:       kernel.EffectClassPureRead,
			FirstAttemptID:    toolAttempt,
			InputRef:          id,
			Terminates:        false,
			RepeatsPrevious:   false,
		}
		if _, err := executor.check(t.Context(), "s1", plan); err == nil {
			t.Fatalf("check of %q must fail closed", id)
		}
	}
	if tool.Snapshot().ReadyPlans != 0 {
		t.Fatalf("%d ready plans, want none", tool.Snapshot().ReadyPlans)
	}
}

func TestToolResultPlansTheNextStepWithItsContext(t *testing.T) {
	t.Parallel()

	tool := pureread.New(map[string][]byte{"k": []byte("the value")})
	capture := &reportCapture{}
	executor := &toolExecutor{tool: tool, ids: execGen("tool-result"), source: newFakeSource(toolRunEvents(), seededToolPayloads(t))}

	executor.Execute(t.Context(), toolWork(), capture.report)

	requireToolResultCommand(t, capture.single(t))
}

func requireToolResultCommand(t *testing.T, obs *coordinator.Observation) {
	t.Helper()
	cmd, ok := obs.Command.(*kernel.ObserveToolResultCommand)
	if !ok {
		t.Fatalf("reported %T, want ObserveToolResultCommand", obs.Command)
	}
	if cmd.ToolCallID != toolCallID || cmd.AttemptID != toolAttempt || cmd.Outcome != "succeeded" ||
		cmd.ResultRef == "" || cmd.EffectEvidence == "" || cmd.SourcePosition != 0 {
		t.Fatalf("tool result incomplete: %+v", cmd)
	}
	if obs.Facts.ExecutorFence != 9 {
		t.Fatalf("result lost the executor fence")
	}
	requireNextPlan(t, obs)
}

func requireNextPlan(t *testing.T, obs *coordinator.Observation) {
	t.Helper()
	plan := obs.Facts.ModelPlan
	if plan == nil || plan.Purpose != "turn" || plan.ModelRef != execModelRef ||
		plan.InvocationID == "" || plan.FirstAttemptID == "" || plan.ContextRef == "" {
		t.Fatalf("next step plan incomplete: %+v", plan)
	}
	kinds := payloadKinds(obs.Payloads)
	if kinds["tool_result"] != findResultRef(t, obs) {
		t.Fatalf("the result payload is not sealed with the command: %v", kinds)
	}
	if kinds["context"] != plan.ContextRef {
		t.Fatalf("the next step context is not sealed with the command: %v", kinds)
	}
	requireContextItems(t, findPayload(obs.Payloads, plan.ContextRef))
}

func findResultRef(t *testing.T, obs *coordinator.Observation) string {
	t.Helper()
	cmd, ok := obs.Command.(*kernel.ObserveToolResultCommand)
	if !ok {
		t.Fatalf("reported %T", obs.Command)
	}
	return cmd.ResultRef
}

func requireContextItems(t *testing.T, plaintext []byte) {
	t.Helper()
	items, err := decodeContext(plaintext)
	if err != nil {
		t.Fatalf("decode the new context: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("next context has %d items, want 3", len(items))
	}
	requireContextItem(t, items[0], "user", "input-1", "the input")
	requireContextItem(t, items[1], "assistant", toolResponseID, "the response")
	requireContextItem(t, items[2], "tool", "", "the value")
}

func requireContextItem(t *testing.T, item contextItem, role, ref, content string) {
	t.Helper()
	if item.Role != role || !slices.Equal(item.Content, []byte(content)) {
		t.Fatalf("%s item wrong: %+v", role, item)
	}
	if ref != "" && item.Ref != ref {
		t.Fatalf("%s item ref %q, want %q", role, item.Ref, ref)
	}
}

func findPayload(payloads []coordinator.Payload, id string) []byte {
	for _, p := range payloads {
		if p.ID == id {
			return p.Plaintext
		}
	}
	return nil
}
