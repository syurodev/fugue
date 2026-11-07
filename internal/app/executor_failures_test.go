package app

import (
	"errors"
	"testing"

	pureread "github.com/syurodev/fugue/internal/tools/pure_read"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/modeladapter"
	"github.com/syurodev/fugue/internal/modeladapter/faux"
)

// requireNoTerminal reports whether none of the observations is terminal.
func requireNoTerminal(t *testing.T, capture *reportCapture) {
	t.Helper()
	for _, obs := range capture.all() {
		switch obs.Command.(type) {
		case *kernel.ObserveModelResponseCommand, *kernel.ObserveToolResultCommand:
			t.Fatalf("a terminal observation was reported despite the failure")
		}
	}
}

func TestExecutorIgnoresForeignDispatch(t *testing.T) {
	t.Parallel()

	model := &modelExecutor{provider: streamScript(nil), ids: execGen("foreign"), source: newFakeSource(nil, nil)}
	tool := &toolExecutor{tool: pureread.New(nil), ids: execGen("foreign-tool"), source: newFakeSource(nil, nil)}

	modelCapture := &reportCapture{}
	model.Execute(t.Context(), toolWork(), modelCapture.report)
	toolCapture := &reportCapture{}
	tool.Execute(t.Context(), modelWork(), toolCapture.report)

	if len(modelCapture.all()) != 0 || len(toolCapture.all()) != 0 {
		t.Fatalf("an executor must ignore work dispatched for the other kind")
	}
}

func TestModelExecutorFailsClosed(t *testing.T) {
	t.Parallel()
	t.Run("unreadable context", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		executor := &modelExecutor{provider: streamScript(nil), ids: execGen("no-ctx"), source: newFakeSource(modelRunEvents(testModelFence), nil)}
		executor.Execute(t.Context(), modelWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("no plan in the stream", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		executor := &modelExecutor{provider: streamScript(nil), ids: execGen("no-plan"), source: newFakeSource(nil, seededContext(t, nil))}
		executor.Execute(t.Context(), modelWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("undecodable context", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		source := newFakeSource(modelRunEvents(testModelFence), map[string][]byte{testContextRef: []byte("not json")})
		executor := &modelExecutor{provider: streamScript(nil), ids: execGen("bad-ctx"), source: source}
		executor.Execute(t.Context(), modelWork(), capture.report)
		requireNoTerminal(t, capture)
	})
}

// The stream-side failures: what the provider itself produces.
func TestModelStreamFailsClosed(t *testing.T) {
	t.Parallel()
	t.Run("cut stream", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		script := faux.New(faux.Script{
			Events:   []modeladapter.Event{{Kind: modeladapter.EventStart}},
			Cut:      true,
			CutAfter: 0,
		})
		executor := &modelExecutor{provider: script, ids: execGen("cut"), source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, nil))}
		executor.Execute(t.Context(), modelWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("error event", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		script := streamScript([]modeladapter.Event{{
			Kind:    modeladapter.EventError,
			Failure: &modeladapter.Failure{Class: modeladapter.ErrorServer, Code: "boom", Message: "provider failed"},
		}})
		executor := &modelExecutor{provider: script, ids: execGen("error"), source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, nil))}
		executor.Execute(t.Context(), modelWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("no usage", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		script := streamScript([]modeladapter.Event{{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonStop}})
		executor := &modelExecutor{provider: script, ids: execGen("no-usage"), source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, nil))}
		executor.Execute(t.Context(), modelWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("usage out of range", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		script := streamScript([]modeladapter.Event{{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonStop, Usage: modeladapter.Usage{Input: 100, CacheRead: 200, Output: 1}}})
		executor := &modelExecutor{provider: script, ids: execGen("usage-range"), source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, nil))}
		executor.Execute(t.Context(), modelWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("orphan tool call delta", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		script := streamScript([]modeladapter.Event{
			{Kind: modeladapter.EventStart},
			{Kind: modeladapter.EventToolCallDelta, Delta: validArgsJSON},
			{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonToolUse, Usage: modeladapter.Usage{Output: 1}},
		})
		executor := &modelExecutor{provider: script, ids: execGen("orphan-delta"), source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, nil))}
		executor.Execute(t.Context(), modelWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("unsupported stop reason", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		script := streamScript([]modeladapter.Event{{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonAborted, Usage: modeladapter.Usage{Output: 1}}})
		executor := &modelExecutor{provider: script, ids: execGen("aborted"), source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, nil))}
		executor.Execute(t.Context(), modelWork(), capture.report)
		requireNoTerminal(t, capture)
	})
}

func TestToolExecutorFailsClosed(t *testing.T) {
	t.Parallel()
	t.Run("unknown invocation", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		executor := &toolExecutor{tool: pureread.New(nil), ids: execGen("tool-no-inv"), source: newFakeSource(nil, seededToolPayloads(t))}
		executor.Execute(t.Context(), toolWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("unreadable arguments", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		executor := &toolExecutor{tool: pureread.New(nil), ids: execGen("tool-no-args"), source: newFakeSource(toolRunEvents(), nil)}
		executor.Execute(t.Context(), toolWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("undecodable arguments", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		payloads := seededToolPayloads(t)
		payloads[toolArgsRef] = []byte("not json")
		executor := &toolExecutor{tool: pureread.New(nil), ids: execGen("tool-bad-args"), source: newFakeSource(toolRunEvents(), payloads)}
		executor.Execute(t.Context(), toolWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("rejected arguments", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		payloads := seededToolPayloads(t)
		blank, err := codec.MarshalCanonical(pureread.Args{Key: "   "})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		payloads[toolArgsRef] = blank
		executor := &toolExecutor{tool: pureread.New(nil), ids: execGen("tool-blank-args"), source: newFakeSource(toolRunEvents(), payloads)}
		executor.Execute(t.Context(), toolWork(), capture.report)
		requireNoTerminal(t, capture)
	})
	t.Run("no selected response for the context", func(t *testing.T) {
		t.Parallel()
		capture := &reportCapture{}
		// The stream records the tool batch without the response that selected
		// it, so no response reference exists to extend the context with.
		source := newFakeSource(append(modelRunEvents(testModelFence), toolDispatchedEvents()...), seededToolPayloads(t))
		executor := &toolExecutor{tool: pureread.New(map[string][]byte{"k": []byte("the value")}), ids: execGen("tool-no-resp"), source: source}
		executor.Execute(t.Context(), toolWork(), capture.report)
		requireNoTerminal(t, capture)
	})
}

// The fail-closed paths keep the attempt where its dispatch left it: the run
// state gains nothing and nothing terminal is reported.
func TestFailuresKeepTheAttemptRequesting(t *testing.T) {
	t.Parallel()

	store := newAppStore(admittedModelState())
	capture := &reportCapture{}
	executor := &modelExecutor{
		provider: streamScript([]modeladapter.Event{{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonAborted, Usage: modeladapter.Usage{Output: 1}}}),
		ids:      execGen("abort-state"),
		source:   newFakeSource(modelRunEvents(3), seededContext(t, nil)),
	}
	sess, err := coordinator.Start(t.Context(), &coordinator.Config{
		SessionID: "s1", OwnerIncarnation: 1, WorkerCapacity: 1,
		Store: store, Executor: executor, Clock: execClock(), IDs: execGen("abort-session"),
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(sess.Stop)

	if _, submitErr := sess.Submit(t.Context(), dispatchRequest("cmd-disp-abort")); submitErr != nil {
		t.Fatalf("submit dispatch: %v", submitErr)
	}

	state, _, err := store.LoadRun(t.Context(), "r1")
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if got := state.ModelInvocations["m1"].Attempts["a1"].Status; got != kernel.ModelAttemptStatusRequesting {
		t.Fatalf("attempt status %q, want requesting", got)
	}
	requireNoTerminal(t, capture)
}

func TestFakeSourceRejectsUnknownPayload(t *testing.T) {
	t.Parallel()

	if _, err := newFakeSource(nil, nil).OpenPayload(t.Context(), "s1", "missing"); !errors.Is(err, errUnknownPayload) {
		t.Fatalf("unknown payload error %v", err)
	}
}

// TestMultipleToolCallsFailTheAttempt pins the slice's single next step: a
// tool_use response proposing two calls fails closed before anything terminal
// is reported (Q-30 owns the canonical record and the context fan-in).
func TestMultipleToolCallsFailTheAttempt(t *testing.T) {
	t.Parallel()

	capture := &reportCapture{}
	script := streamScript([]modeladapter.Event{
		{Kind: modeladapter.EventStart},
		{Kind: modeladapter.EventToolCallStart, CallID: "call-1", Name: pureReadTool},
		{Kind: modeladapter.EventToolCallDelta, Delta: validArgsJSON},
		{Kind: modeladapter.EventToolCallEnd},
		{Kind: modeladapter.EventToolCallStart, CallID: "call-2", Name: pureReadTool},
		{Kind: modeladapter.EventToolCallDelta, Delta: `{"Key":"k2"}`},
		{Kind: modeladapter.EventToolCallEnd},
		{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonToolUse, Usage: modeladapter.Usage{Output: 5}},
	})
	executor := &modelExecutor{provider: script, ids: execGen("multi-call"), source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, nil))}
	executor.Execute(t.Context(), modelWork(), capture.report)

	requireNoTerminal(t, capture)
	for _, obs := range capture.all() {
		if _, ok := obs.Command.(*kernel.ObserveModelStreamCommand); !ok {
			t.Fatalf("reported %T before the response was known to fail", obs.Command)
		}
	}
}
