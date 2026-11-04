package faux

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"github.com/syurodev/fugue/internal/modeladapter"
)

func TestScriptEmitsOrderedContentAndTerminalEvidence(t *testing.T) {
	t.Parallel()
	events := []modeladapter.Event{
		{Kind: modeladapter.EventStart},
		{Kind: modeladapter.EventTextStart, ContentIndex: 0},
		{Kind: modeladapter.EventTextDelta, ContentIndex: 0, Delta: "hello"},
		{Kind: modeladapter.EventThinkingStart, ContentIndex: 1},
		{Kind: modeladapter.EventThinkingDelta, ContentIndex: 1, Delta: "because"},
		{Kind: modeladapter.EventToolCallStart, ContentIndex: 2, CallID: "call-1", Name: "read"},
		{Kind: modeladapter.EventToolCallDelta, ContentIndex: 2, Delta: `{"path":"README.md"}`},
		{Kind: modeladapter.EventToolCallEnd, ContentIndex: 2, Arguments: `{"path":"README.md"}`},
		{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonToolUse, Usage: modeladapter.Usage{Input: 8, Output: 4}},
	}
	provider := New(Script{Events: events})
	got := receive(t, provider.Stream(t.Context(), &modeladapter.Request{Model: "fake"}))
	if !reflect.DeepEqual(got, events) {
		t.Fatalf("stream events = %#v, want %#v", got, events)
	}
	if stats := provider.Snapshot(); stats != (Stats{Calls: 1, Completed: 1}) {
		t.Fatalf("stats = %+v, want one completed call", stats)
	}
}

func TestScriptsRepeatInDeclaredOrderAndClassifyFailures(t *testing.T) {
	t.Parallel()
	classes := []modeladapter.ErrorClass{
		modeladapter.ErrorRateLimit, modeladapter.ErrorOverloaded, modeladapter.ErrorServer,
		modeladapter.ErrorTimeout, modeladapter.ErrorTransport, modeladapter.ErrorEmptyResponse,
		modeladapter.ErrorContextOverflow, modeladapter.ErrorAuth, modeladapter.ErrorPermission,
		modeladapter.ErrorQuota, modeladapter.ErrorInvalidRequest, modeladapter.ErrorModelNotFound,
		modeladapter.ErrorRefusal, modeladapter.ErrorContentFilter,
	}
	scripts := make([]Script, len(classes))
	firstPass := make([][]modeladapter.Event, len(classes))
	for i, class := range classes {
		scripts[i] = Script{Events: []modeladapter.Event{
			{Kind: modeladapter.EventError, Failure: &modeladapter.Failure{Class: class, Code: "scripted"}},
		}}
	}
	provider := New(scripts...)
	for i := range scripts {
		firstPass[i] = receive(t, provider.Stream(t.Context(), &modeladapter.Request{}))
		if len(firstPass[i]) != 1 || firstPass[i][0].Failure.Class != classes[i] {
			t.Fatalf("failure %d = %#v", i, firstPass[i])
		}
		wantRetryable := i < 6
		if classes[i].Retryable() != wantRetryable {
			t.Errorf("Retryable(%q) = %t, want %t", classes[i], classes[i].Retryable(), wantRetryable)
		}
	}
	replay := New(scripts...)
	for i := range scripts {
		if got := receive(t, replay.Stream(t.Context(), &modeladapter.Request{})); !reflect.DeepEqual(got, firstPass[i]) {
			t.Fatalf("replay %d = %#v, want %#v", i, got, firstPass[i])
		}
	}
	if stats := provider.Snapshot(); stats.Calls != int64(len(classes)) || stats.Failed != int64(len(classes)) || stats.Active != 0 {
		t.Fatalf("failure evidence = %+v", stats)
	}
}

func TestCutPointsCloseWithoutInventingTerminalOutcome(t *testing.T) {
	t.Parallel()
	events := []modeladapter.Event{
		{Kind: modeladapter.EventStart},
		{Kind: modeladapter.EventTextDelta, Delta: "partial"},
		{Kind: modeladapter.EventThinkingDelta, Delta: "partial thought"},
		{Kind: modeladapter.EventDone},
	}
	for _, cutAfter := range []int{0, 1, 3} {
		t.Run(strconv.Itoa(cutAfter), func(t *testing.T) {
			t.Parallel()
			assertCutPoint(t, events, cutAfter)
		})
	}
}

func assertCutPoint(t *testing.T, events []modeladapter.Event, cutAfter int) {
	t.Helper()
	provider := New(Script{Events: events, CutAfter: cutAfter, Cut: true})
	got := receive(t, provider.Stream(t.Context(), &modeladapter.Request{}))
	if len(got) != cutAfter {
		t.Fatalf("received %d events, want %d", len(got), cutAfter)
	}
	for i := range got {
		event := got[i]
		if event.Kind == modeladapter.EventDone || event.Kind == modeladapter.EventError {
			t.Fatalf("cut stream invented terminal event: %+v", event)
		}
	}
	if stats := provider.Snapshot(); stats.Interrupted != 1 || stats.Active != 0 {
		t.Fatalf("cut evidence = %+v", stats)
	}
}

func TestCancellationStopsStreamAndClosesIt(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	provider := New(Script{Events: []modeladapter.Event{
		{Kind: modeladapter.EventStart},
		{Kind: modeladapter.EventTextDelta, Delta: "blocked until read"},
	}})
	stream := provider.Stream(ctx, &modeladapter.Request{})
	if event := <-stream; event.Kind != modeladapter.EventStart {
		t.Fatalf("first event = %+v, want start", event)
	}
	cancel()
	for range stream {
	}
	if stats := provider.Snapshot(); stats.Canceled != 1 || stats.Active != 0 {
		t.Fatalf("cancellation evidence = %+v", stats)
	}
}

func TestScriptFailureIsOwned(t *testing.T) {
	t.Parallel()
	failure := &modeladapter.Failure{Class: modeladapter.ErrorAuth, Code: "initial"}
	provider := New(Script{Events: []modeladapter.Event{{Kind: modeladapter.EventError, Failure: failure}}})
	failure.Class = modeladapter.ErrorQuota
	got := receive(t, provider.Stream(t.Context(), nil))
	if got[0].Failure.Class != modeladapter.ErrorAuth {
		t.Fatal("mutating constructor input changed the provider's retained script")
	}
}

func TestReceivedFailureDoesNotChangeNextCall(t *testing.T) {
	t.Parallel()
	failure := &modeladapter.Failure{Class: modeladapter.ErrorAuth, Code: "initial"}
	script := Script{Events: []modeladapter.Event{{Kind: modeladapter.EventError, Failure: failure}}}
	provider := New(script, script)
	got := receive(t, provider.Stream(t.Context(), nil))
	got[0].Failure.Class = modeladapter.ErrorQuota
	next := receive(t, provider.Stream(t.Context(), nil))
	if next[0].Failure.Class != modeladapter.ErrorAuth {
		t.Fatal("mutating a received failure changed the next call's scripted output")
	}
}

func receive(t *testing.T, stream <-chan modeladapter.Event) []modeladapter.Event {
	t.Helper()
	var events []modeladapter.Event
	for event := range stream {
		events = append(events, event)
	}
	return events
}
