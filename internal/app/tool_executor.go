package app

import (
	"context"

	pureread "github.com/syurodev/fugue/internal/tools/pure_read"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// toolExecutor runs dispatched pure_read tool attempts and the pre-dispatch
// check. The check is a host-side activity — no worker lease exists for it —
// so it is a method the driver calls, not an Execute path.
type toolExecutor struct {
	tool   *pureread.Tool
	ids    ids.Generator
	source attemptSource
}

// check validates the planned call against the tool's argument shape and
// returns the validated arguments reference, which is the sealed args payload
// the plan already names. A rejected call returns a typed error and the driver
// submits nothing.
func (e *toolExecutor) check(ctx context.Context, sessionID string, plan *kernel.ToolCallPlannedEvent) (string, error) {
	plaintext, err := e.source.OpenPayload(ctx, sessionID, plan.InputRef)
	if err != nil {
		return "", errArgsUnreadable
	}
	var args pureread.Args
	if err := codec.UnmarshalCanonical(plaintext, &args); err != nil {
		return "", errArgsUnparsable
	}
	if _, err := e.tool.Prepare(ctx, args); err != nil {
		return "", errToolPrepareRejected
	}
	return plan.InputRef, nil
}

func (e *toolExecutor) Execute(ctx context.Context, w *coordinator.Work, report func(*coordinator.Observation)) {
	if _, ok := w.Dispatch.(*kernel.ToolDispatchStartedEvent); !ok {
		return
	}
	if err := e.execute(ctx, w, report); err != nil {
		logAbandonedAttempt(ctx, &w.Lease, err)
	}
}

func (e *toolExecutor) execute(ctx context.Context, w *coordinator.Work, report func(*coordinator.Observation)) error {
	outcome, err := e.run(ctx, w)
	if err != nil {
		return err
	}
	nextPlan, err := e.nextPlan(outcome.modelRef, outcome.contextPayload.ID)
	if err != nil {
		return err
	}

	command := &kernel.ObserveToolResultCommand{
		CommandID:      "",
		RunID:          w.Lease.RunID,
		Origin:         "",
		Token:          0,
		ToolCallID:     w.Lease.InvocationID,
		AttemptID:      w.Lease.AttemptID,
		Outcome:        "succeeded",
		ResultRef:      outcome.resultID,
		Code:           "",
		EffectEvidence: toolEffectEvidence,
		SourcePosition: outcome.sourcePosition,
	}
	input, err := canonicalInput(command)
	if err != nil {
		return err
	}
	report(&coordinator.Observation{
		Lease:   w.Lease,
		Command: command,
		Facts: kernel.DecisionFacts{
			ExecutorFence: w.Lease.ExecutorFence,
			ModelPlan:     nextPlan,
		},
		ProtectedInput: input,
		Payloads: []coordinator.Payload{
			{ID: outcome.resultID, Kind: kindToolResult, Plaintext: outcome.result.Content},
			outcome.contextPayload,
		},
	})
	return nil
}

// toolOutcome carries what one dispatched tool attempt produced, plus what the
// next step needs: its context payload and the model it plans against.
type toolOutcome struct {
	invocation     *kernel.ToolInvocation
	result         pureread.Result
	resultID       string
	modelRef       string
	contextPayload coordinator.Payload
	sourcePosition int
}

// run looks the dispatched attempt up, executes the tool once and seals the
// result together with the next step's context.
func (e *toolExecutor) run(ctx context.Context, w *coordinator.Work) (*toolOutcome, error) {
	state, _, err := e.source.LoadRun(ctx, w.Lease.RunID)
	if err != nil {
		return nil, errcode.New(errcode.Internal, "load run")
	}
	inv := state.ToolInvocations[w.Lease.InvocationID]
	if inv == nil || inv.Attempts[w.Lease.AttemptID] == nil {
		return nil, errPlanNotFound
	}
	result, err := e.executeTool(ctx, w.Lease.SessionID, inv.Plan.InputRef)
	if err != nil {
		return nil, err
	}
	resultID, err := e.ids.NewID()
	if err != nil {
		return nil, errcode.New(errcode.Internal, "new result payload id")
	}
	// The next step's context extends the transcript with the selected response
	// and this result; it is sealed in the same observation as the command that
	// names it.
	contextPayload, modelRef, err := e.extendedContext(ctx, state, w, result.Content, resultID)
	if err != nil {
		return nil, err
	}
	return &toolOutcome{
		invocation:     inv,
		result:         result,
		resultID:       resultID,
		modelRef:       modelRef,
		contextPayload: contextPayload,
		sourcePosition: inv.SourcePosition,
	}, nil
}

// executeTool decodes the sealed arguments, revalidates them and reads once.
func (e *toolExecutor) executeTool(ctx context.Context, sessionID, argsRef string) (pureread.Result, error) {
	argsPlaintext, openErr := e.source.OpenPayload(ctx, sessionID, argsRef)
	if openErr != nil {
		return pureread.Result{}, errArgsUnreadable
	}
	var args pureread.Args
	if decodeErr := codec.UnmarshalCanonical(argsPlaintext, &args); decodeErr != nil {
		return pureread.Result{}, errArgsUnparsable
	}
	plan, prepareErr := e.tool.Prepare(ctx, args)
	if prepareErr != nil {
		return pureread.Result{}, errToolPrepareRejected
	}
	result, execErr := e.tool.Execute(ctx, plan, nil)
	if execErr != nil {
		return pureread.Result{}, errToolExecuteFailed
	}
	return result, nil
}

// extendedContext opens the current step's context, appends the selected
// response and this result, and seals the next step's context payload.
func (e *toolExecutor) extendedContext(ctx context.Context, state *kernel.State, w *coordinator.Work, resultContent []byte, resultID string) (coordinator.Payload, string, error) {
	inv := state.ModelInvocations[state.CurrentModelInvocationID]
	if inv == nil || inv.ContextRef == "" {
		return coordinator.Payload{}, "", errPlanNotFound
	}
	if inv.SelectedAttemptID == "" || inv.Attempts[inv.SelectedAttemptID] == nil {
		return coordinator.Payload{}, "", errPlanNotFound
	}
	items, err := openContext(ctx, e.source, w.Lease.SessionID, inv.ContextRef)
	if err != nil {
		return coordinator.Payload{}, "", err
	}
	responseRef := inv.Attempts[inv.SelectedAttemptID].Response.ResponseRef
	responseText, err := e.source.OpenPayload(ctx, w.Lease.SessionID, responseRef)
	if err != nil {
		return coordinator.Payload{}, "", errPayloadUnreadable
	}
	items = append(items,
		contextItem{Role: "assistant", Ref: responseRef, Content: responseText},
		contextItem{Role: "tool", Ref: resultID, Content: resultContent},
	)
	payload, err := newContext(e.ids, items)
	return payload, inv.ModelRef, err
}

// nextPlan plans the step the kernel opens in the same batch as the result.
func (e *toolExecutor) nextPlan(modelRef, contextRef string) (*kernel.ModelRequestPlannedEvent, error) {
	invocationID, err := e.ids.NewID()
	if err != nil {
		return nil, errcode.New(errcode.Internal, "next invocation id")
	}
	attemptID, err := e.ids.NewID()
	if err != nil {
		return nil, errcode.New(errcode.Internal, "next attempt id")
	}
	return &kernel.ModelRequestPlannedEvent{
		Envelope:              kernel.Envelope{},
		InvocationID:          invocationID,
		Purpose:               purposeTurn,
		FirstAttemptID:        attemptID,
		ModelRef:              modelRef,
		ContextRef:            contextRef,
		ToolDeclarationRefs:   nil,
		EstimatedBudgetTokens: 0,
	}, nil
}
