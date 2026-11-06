package app

import (
	"context"
	"log/slog"
	"strings"

	pureread "github.com/syurodev/fugue/internal/tools/pure_read"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/modeladapter"
	"github.com/syurodev/fugue/internal/modeladapter/faux"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// attemptSource is what an executor reads to run one dispatched attempt: the
// committed run state, the run's event stream — the model plan survives only in
// the stream, its context reference is not part of the folded state — and the
// sealed payloads those references name.
type attemptSource interface {
	LoadRun(ctx context.Context, runID string) (*kernel.State, uint64, error)
	ReadEvents(ctx context.Context, runID string, fromSequence, toSequence uint64) ([]kernel.Event, error)
	OpenPayload(ctx context.Context, sessionID, payloadID string) ([]byte, error)
}

// Typed failures of the executors. Each leaves the attempt in its committed
// status: the executor reports nothing further, the coordinator reclaims the
// slot, and nothing retries (F3.3 owns retry).
var (
	errNoTerminalOutcome   = errcode.New(errcode.FailedPrecondition, "model stream ended without a terminal outcome")
	errStreamFailed        = errcode.New(errcode.FailedPrecondition, "model stream reported an error")
	errUnsupportedStop     = errcode.New(errcode.FailedPrecondition, "model stop reason has no kernel finish reason")
	errArgsUnparsable      = errcode.New(errcode.InvalidArgument, "tool call arguments are not canonical JSON")
	errArgsUnreadable      = errcode.New(errcode.InvalidArgument, "tool call arguments payload unreadable")
	errPlanNotFound        = errcode.New(errcode.Internal, "run stream has no plan for the invocation")
	errPayloadUnreadable   = errcode.New(errcode.InvalidArgument, "payload unreadable")
	errContextUndecodable  = errcode.New(errcode.InvalidArgument, "context payload undecodable")
	errUsageMissing        = errcode.New(errcode.FailedPrecondition, "completed model stream carried no usage")
	errToolPrepareRejected = errcode.New(errcode.InvalidArgument, "tool check rejected the arguments")
	errToolExecuteFailed   = errcode.New(errcode.FailedPrecondition, "tool execution failed")
)

// modelExecutor runs dispatched model attempts over the faux provider. It
// reports a stream observation for the first chunk and a response observation
// with the response, the usage and one tool plan per proposed call.
type modelExecutor struct {
	provider *faux.Provider
	ids      ids.Generator
	source   attemptSource
}

func (e *modelExecutor) Execute(ctx context.Context, w *coordinator.Work, report func(*coordinator.Observation)) {
	if _, ok := w.Dispatch.(*kernel.ModelDispatchStartedEvent); !ok {
		return
	}
	if err := e.execute(ctx, w, report); err != nil {
		logAbandonedAttempt(ctx, &w.Lease, err)
	}
}

func (e *modelExecutor) execute(ctx context.Context, w *coordinator.Work, report func(*coordinator.Observation)) error {
	messages, err := e.stepMessages(ctx, w)
	if err != nil {
		return err
	}
	return e.run(ctx, w, messages, report)
}

// streamState accumulates one provider response from its stream events.
type streamState struct {
	text    strings.Builder
	calls   []observedCall
	done    *modeladapter.Event
	started bool
}

func (e *modelExecutor) run(ctx context.Context, w *coordinator.Work, messages []modeladapter.Message, report func(*coordinator.Observation)) error {
	state := &streamState{}
	request := &modeladapter.Request{Model: w.Lease.InvocationID, Transcript: messages}
	for event := range e.provider.Stream(ctx, request) {
		if err := e.firstChunk(w, state, &event, report); err != nil {
			return err
		}
		if err := state.consume(&event); err != nil {
			return err
		}
	}
	if err := state.terminal(); err != nil {
		return err
	}
	return e.reportResponse(w, state, report)
}

// firstChunk seals the first chunk of the stream as evidence and reports it. A
// done event alone is no chunk, so it is not reported as a stream.
func (e *modelExecutor) firstChunk(w *coordinator.Work, state *streamState, event *modeladapter.Event, report func(*coordinator.Observation)) error {
	if state.started {
		return nil
	}
	state.started = true
	if event.Kind == modeladapter.EventDone {
		return nil
	}
	chunkID, err := e.ids.NewID()
	if err != nil {
		return errcode.New(errcode.Internal, "new chunk payload id")
	}
	plaintext, err := codec.MarshalCanonical(event)
	if err != nil {
		return errcode.New(errcode.Internal, "marshal first chunk")
	}
	command := &kernel.ObserveModelStreamCommand{
		CommandID:     "",
		RunID:         w.Lease.RunID,
		Origin:        "",
		Token:         0,
		AttemptID:     w.Lease.AttemptID,
		InvocationID:  w.Lease.InvocationID,
		FirstChunkRef: chunkID,
	}
	input, err := canonicalInput(command)
	if err != nil {
		return err
	}
	report(&coordinator.Observation{
		Lease:          w.Lease,
		Command:        command,
		ProtectedInput: input,
		Payloads:       []coordinator.Payload{{ID: chunkID, Kind: kindFirstChunk, Plaintext: plaintext}},
	})
	return nil
}

// consume folds one stream event into the response state. The raw event is
// untrusted provider data: only the shapes below are read, and tool-call
// arguments must later decode exactly into the tool's argument type.
func (s *streamState) consume(event *modeladapter.Event) error {
	switch event.Kind {
	case modeladapter.EventTextDelta:
		s.text.WriteString(event.Delta)
	case modeladapter.EventToolCallStart:
		s.calls = append(s.calls, observedCall{callID: event.CallID})
	case modeladapter.EventToolCallDelta:
		if len(s.calls) > 0 {
			s.calls[len(s.calls)-1].arguments.WriteString(event.Delta)
		}
	case modeladapter.EventDone:
		s.done = event
	case modeladapter.EventError:
		return errStreamFailed
	case modeladapter.EventStart,
		modeladapter.EventTextStart,
		modeladapter.EventTextEnd,
		modeladapter.EventThinkingStart,
		modeladapter.EventThinkingDelta,
		modeladapter.EventThinkingEnd,
		modeladapter.EventToolCallEnd:
		// Content kinds the slice does not accumulate: thinking stays with the
		// provider and a closed call needs no separate evidence.
	}
	return nil
}

// terminal classifies a closed stream without a terminal outcome as a failure;
// only a done event carries the stop reason the kernel decides on.
func (s *streamState) terminal() error {
	if s.done != nil {
		return nil
	}
	return errNoTerminalOutcome
}

func (e *modelExecutor) reportResponse(w *coordinator.Work, state *streamState, report func(*coordinator.Observation)) error {
	finish, ok := finishReason(state.done.StopReason)
	if !ok {
		return errUnsupportedStop
	}
	usage := state.done.Usage
	if usage.Input == 0 && usage.Output == 0 && usage.Reasoning == 0 {
		return errUsageMissing
	}
	responseID, err := e.ids.NewID()
	if err != nil {
		return errcode.New(errcode.Internal, "new response payload id")
	}
	observation := &coordinator.Observation{
		Lease: w.Lease,
		Command: &kernel.ObserveModelResponseCommand{
			CommandID:    "",
			RunID:        w.Lease.RunID,
			Origin:       "",
			Token:        0,
			AttemptID:    w.Lease.AttemptID,
			InvocationID: w.Lease.InvocationID,
			ResponseRef:  responseID,
			UsageStatus:  "known",
			FinishReason: finish,
		},
		Facts: kernel.DecisionFacts{
			Usage: &kernel.BudgetObservedEvent{
				Envelope:     kernel.Envelope{},
				AttemptID:    w.Lease.AttemptID,
				WindowID:     "",
				TokensUsed:   usage.Input - usage.CacheRead + usage.Output + usage.Reasoning,
				DurationUsed: 0,
				Basis:        "provider_reported",
			},
		},
		Payloads: []coordinator.Payload{{ID: responseID, Kind: kindModelResponse, Plaintext: []byte(state.text.String())}},
	}
	if finish == kernel.FinishReasonToolUse {
		if planErr := e.appendToolPlans(observation, state.calls); planErr != nil {
			return planErr
		}
	}
	if observation.ProtectedInput, err = canonicalInput(observation.Command); err != nil {
		return err
	}
	report(observation)
	return nil
}

func (e *modelExecutor) appendToolPlans(observation *coordinator.Observation, calls []observedCall) error {
	for i := range calls {
		plan, argsPayload, err := e.toolPlan(&calls[i], i)
		if err != nil {
			return err
		}
		observation.Facts.ToolPlans = append(observation.Facts.ToolPlans, plan)
		observation.Payloads = append(observation.Payloads, argsPayload)
	}
	return nil
}

// stepMessages opens the step's context payload and turns it into the provider
// transcript.
func (e *modelExecutor) stepMessages(ctx context.Context, w *coordinator.Work) ([]modeladapter.Message, error) {
	state, _, err := e.source.LoadRun(ctx, w.Lease.RunID)
	if err != nil {
		return nil, errcode.New(errcode.Internal, "load run")
	}
	plan, err := findModelPlan(ctx, e.source, w.Lease.RunID, w.Lease.InvocationID, state.LastSequence)
	if err != nil {
		return nil, err
	}
	items, err := openContext(ctx, e.source, w.Lease.SessionID, plan.ContextRef)
	if err != nil {
		return nil, err
	}
	messages := make([]modeladapter.Message, 0, len(items))
	for _, item := range items {
		messages = append(messages, modeladapter.Message{Role: item.Role, Content: string(item.Content)})
	}
	return messages, nil
}

// findModelPlan reads the run's stream for the plan of one invocation: the
// folded state keeps the invocation and its attempts, but the plan's context
// reference and model reference survive only in the event.
func findModelPlan(ctx context.Context, source attemptSource, runID, invocationID string, lastSequence uint64) (*kernel.ModelRequestPlannedEvent, error) {
	events, err := source.ReadEvents(ctx, runID, 1, lastSequence)
	if err != nil {
		return nil, errcode.New(errcode.Internal, "read run events")
	}
	for _, ev := range events {
		if plan, ok := ev.(*kernel.ModelRequestPlannedEvent); ok && plan.InvocationID == invocationID {
			return plan, nil
		}
	}
	return nil, errPlanNotFound
}

// openContext reads and decodes a sealed step context.
func openContext(ctx context.Context, source attemptSource, sessionID, payloadID string) ([]contextItem, error) {
	plaintext, err := source.OpenPayload(ctx, sessionID, payloadID)
	if err != nil {
		return nil, errPayloadUnreadable
	}
	items, err := decodeContext(plaintext)
	if err != nil {
		return nil, errContextUndecodable
	}
	return items, nil
}

// toolPlan turns one observed provider call into a kernel tool plan with its
// canonical arguments payload. The raw arguments are untrusted model output:
// they must decode exactly into the tool's argument shape or the attempt fails.
func (e *modelExecutor) toolPlan(call *observedCall, i int) (*kernel.ToolCallPlannedEvent, coordinator.Payload, error) {
	var args pureread.Args
	if err := codec.UnmarshalCanonical([]byte(call.arguments.String()), &args); err != nil {
		return nil, coordinator.Payload{}, errArgsUnparsable
	}
	canonical, err := codec.MarshalCanonical(args)
	if err != nil {
		return nil, coordinator.Payload{}, errArgsUnparsable
	}
	argsID, err := e.ids.NewID()
	if err != nil {
		return nil, coordinator.Payload{}, errcode.New(errcode.Internal, "new args payload id")
	}
	attemptID, err := e.ids.NewID()
	if err != nil {
		return nil, coordinator.Payload{}, errcode.New(errcode.Internal, "new tool attempt id")
	}
	plan := &kernel.ToolCallPlannedEvent{
		Envelope:          kernel.Envelope{},
		ToolCallID:        call.callID,
		SourcePosition:    i,
		DescriptorVersion: descriptorPureRead,
		EffectClass:       kernel.EffectClassPureRead,
		FirstAttemptID:    attemptID,
		InputRef:          argsID,
		Terminates:        false,
		RepeatsPrevious:   false,
	}
	return plan, coordinator.Payload{ID: argsID, Kind: kindToolArgs, Plaintext: canonical}, nil
}

// observedCall accumulates one provider tool call from the stream events.
type observedCall struct {
	callID    string
	arguments strings.Builder
}

func finishReason(reason modeladapter.StopReason) (kernel.FinishReason, bool) {
	switch reason {
	case modeladapter.StopReasonStop:
		return kernel.FinishReasonStop, true
	case modeladapter.StopReasonToolUse:
		return kernel.FinishReasonToolUse, true
	case modeladapter.StopReasonLength:
		return kernel.FinishReasonLength, true
	case modeladapter.StopReasonRefusal:
		return kernel.FinishReasonRefusal, true
	case modeladapter.StopReasonError, modeladapter.StopReasonAborted:
		// Failures are F3.3's decisions; the executor reports nothing.
		return "", false
	}
	return "", false
}

// canonicalInput marshals a worker command into the protected canonical input
// the execution store seals with the append.
func canonicalInput(command kernel.Command) ([]byte, error) {
	input, err := codec.MarshalCanonical(command)
	if err != nil {
		return nil, errcode.New(errcode.Internal, "marshal command input")
	}
	return input, nil
}

// logAbandonedAttempt writes the executor's single failure line: IDs and the
// error code, never payload or plaintext content.
func logAbandonedAttempt(ctx context.Context, lease *coordinator.Lease, err error) {
	slog.WarnContext(ctx, "executor abandoned the attempt",
		"run_id", lease.RunID, "invocation_id", lease.InvocationID,
		"attempt_id", lease.AttemptID, "code", string(errcode.CodeOf(err)))
}
