package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

const (
	testAppIncarnation = "inc-1"
	testAppSession     = "session"
	testInvID          = "inv-1"
	testAttID          = "att-1"
	testToolID         = "tool-id"
	testSessionID      = "sess-1"
	testEnvDev         = "dev"
	testAllow          = "allow"
)

func newTestInteractionService(t *testing.T, clk clock.Clock) (*InteractionService, *EventHub) {
	t.Helper()
	if clk == nil {
		clk = clock.NewFake(time.Unix(100, 0))
	}
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("test"))
	hub := NewEventHub(testAppIncarnation)
	owner, err := interaction.NewOwner(interaction.Config{
		Clock: clk,
		IDs:   idGen,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewInteractionService(owner, hub, clk)
	if err != nil {
		t.Fatal(err)
	}
	task := svc.Start(t.Context())
	t.Cleanup(func() {
		if werr := task.Wait(); werr != nil && !errors.Is(werr, context.Canceled) {
			t.Errorf("interaction service task wait: %v", werr)
		}
	})
	return svc, hub
}

func ensureTestConfirm(t *testing.T, svc *InteractionService, opID, summary string) string {
	t.Helper()
	ticket, err := svc.Ensure(&interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Risk:        "write",
		Summary:     summary,
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession},
		Binding: interaction.Binding{
			SessionID:       testSessionID,
			InvocationID:    testInvID,
			AttemptID:       testAttID,
			CheckGeneration: 1,
			ToolName:        "file_writer",
			EnvTag:          testEnvDev,
		},
		GrantScopeLabel: "workspace write",
	})
	if err != nil {
		t.Fatal(err)
	}
	return ticket.RequestID
}

func ensureTestRelayed(t *testing.T, svc *InteractionService, sessionID, turnID, vendorReqID, summary string) string {
	t.Helper()
	opID, err := interaction.VendorOperationKey(sessionID, turnID, vendorReqID)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := svc.Ensure(&interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Summary:     summary,
		Relayed:     true,
		Options: []interaction.Option{
			{ID: testAllow, Label: "Allow"},
			{ID: "deny", Label: "Deny"},
		},
		Binding: interaction.Binding{
			SessionID:       sessionID,
			TurnID:          turnID,
			VendorRequestID: vendorReqID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ticket.RequestID
}

func testSpec(opID, summary string) *interaction.Spec {
	return &interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Risk:        "write",
		Summary:     summary,
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
		Binding: interaction.Binding{
			SessionID:       testSessionID,
			InvocationID:    testInvID,
			AttemptID:       testAttID,
			CheckGeneration: 1,
			ToolName:        "tool-1",
			EnvTag:          "work",
		},
		GrantScopeLabel: "workspace write",
	}
}

func mustEnsure(t *testing.T, svc *InteractionService, spec *interaction.Spec) *interaction.Ticket {
	t.Helper()
	ticket, err := svc.Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func mustListPending(t *testing.T, svc *InteractionService, principal string) ([]PendingInteractionView, EventSeq) {
	t.Helper()
	views, seq, err := svc.ListPending(t.Context(), principal)
	if err != nil {
		t.Fatal(err)
	}
	return views, seq
}

func TestInteractionTopicSnapshot(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	svc, hub := newTestInteractionService(t, clk)

	views, seq := mustListPending(t, svc, string(KindCLI))
	if seq != 0 || len(views) != 0 {
		t.Fatalf("initial ListPending = (%+v, %d), want ([], 0)", views, seq)
	}

	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)

	ticket := mustEnsure(t, svc, testSpec("op-1", "delete file foo"))
	ev, ok := sub.Next(t.Context())
	if !ok {
		t.Fatal("expected requested event")
	}
	if ev.Seq != 1 || ev.Type != EventInteractionRequested {
		t.Fatalf("first event = %+v, want requested event at seq 1", ev)
	}

	views, snapSeq := mustListPending(t, svc, string(KindCLI))
	if snapSeq != 1 || len(views) != 1 || views[0].RequestID != ticket.RequestID {
		t.Fatalf("ListPending = (%+v, %d), want request %s at seq 1", views, snapSeq, ticket.RequestID)
	}
}

func TestInteractionTopicResolve(t *testing.T) {
	t.Parallel()
	svc, hub := newTestInteractionService(t, nil)

	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)

	ticket := mustEnsure(t, svc, testSpec("op-resolve", "resolve test"))
	evReq, ok := sub.Next(t.Context())
	if !ok {
		t.Fatal("expected requested event")
	}
	if evReq.Type != EventInteractionRequested {
		t.Fatalf("first event = %+v, want requested", evReq)
	}

	res, err := svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-res-1",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAllowedOnce {
		t.Fatalf("outcome = %s, want allowed_once", res.Outcome)
	}

	evRes, ok := sub.Next(t.Context())
	if !ok {
		t.Fatal("expected resolved event")
	}
	assertResolvedEventPayload(t, evRes, ticket.RequestID, OutcomeAllowedOnce)

	views, snapSeq := mustListPending(t, svc, string(KindCLI))
	if snapSeq != 2 || len(views) != 0 {
		t.Fatalf("ListPending after resolve = (%+v, %d), want empty at seq 2", views, snapSeq)
	}
}

func assertResolvedEventPayload(t *testing.T, ev Event, wantReqID, wantOutcome string) {
	t.Helper()
	if ev.Seq != 2 || ev.Type != EventInteractionResolved {
		t.Fatalf("event = %+v, want resolved event at seq 2", ev)
	}
	var resPayload resolvedPayload
	if jerr := json.Unmarshal(ev.Payload, &resPayload); jerr != nil {
		t.Fatal(jerr)
	}
	if resPayload.RequestID != wantReqID || resPayload.Outcome != wantOutcome {
		t.Fatalf("resolved payload = %+v, want (%s, %s)", resPayload, wantReqID, wantOutcome)
	}
}

func TestCommandDedupCache(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	cache := newCommandDedupCache(clk, 3, 5*time.Minute)

	cache.Put("cmd-1", AnswerInteractionResult{RequestID: "r-1", Outcome: OutcomeAllowedOnce})
	got, ok := cache.Get("cmd-1")
	if !ok || got.RequestID != "r-1" {
		t.Fatalf("Get cmd-1 = (%+v, %v), want r-1", got, ok)
	}

	cache.Put("cmd-2", AnswerInteractionResult{RequestID: "r-2", Outcome: OutcomeDenied})
	cache.Put("cmd-3", AnswerInteractionResult{RequestID: "r-3", Outcome: OutcomeAllowedOnce})
	cache.Put("cmd-4", AnswerInteractionResult{RequestID: "r-4", Outcome: OutcomeDenied})

	if _, ok := cache.Get("cmd-1"); ok {
		t.Fatal("cmd-1 should have been evicted by capacity")
	}
	if _, ok := cache.Get("cmd-4"); !ok {
		t.Fatal("cmd-4 should be present")
	}

	clk.Advance(6 * time.Minute)
	cache.Put("cmd-5", AnswerInteractionResult{RequestID: "r-5", Outcome: OutcomeAllowedOnce})
	if _, ok := cache.Get("cmd-2"); ok {
		t.Fatal("cmd-2 should have been evicted by TTL")
	}
	if _, ok := cache.Get("cmd-5"); !ok {
		t.Fatal("cmd-5 should be present")
	}
}

func TestInteractionServiceDedupReturnsIdenticalResult(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	ticket, err := svc.Ensure(testSpec("op-1", "dedup test"))
	if err != nil {
		t.Fatal(err)
	}

	cmd := &AnswerInteractionCommand{
		CommandID:   "cmd-dedup",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	}
	res1, err := svc.Answer(t.Context(), cmd)
	if err != nil {
		t.Fatal(err)
	}

	res2, err := svc.Answer(t.Context(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if res1 != res2 {
		t.Fatalf("res1 = %+v, res2 = %+v, want identical", res1, res2)
	}
}

func TestInteractionSessionPrincipalDenied(t *testing.T) {
	t.Parallel()
	svc, hub := newTestInteractionService(t, nil)
	ticket, err := svc.Ensure(testSpec("op-sec-p", "security test"))
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = svc.ListPending(t.Context(), testAppSession)
	if errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("ListPending session = %v, want permission_denied", err)
	}

	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-session",
		Principal:   testAppSession,
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	if errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("Answer session = %v, want permission_denied", err)
	}

	_, err = hub.Subscribe(t.Context(), SubscribeParams{Principal: testAppSession, Topic: TopicInteraction})
	if errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("Subscribe session = %v, want permission_denied", err)
	}
}

func TestInteractionValidationRejections(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	ticket, err := svc.Ensure(testSpec("op-val-rej", "validation test"))
	if err != nil {
		t.Fatal(err)
	}

	// Stale incarnation.
	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-stale",
		Principal:   string(KindCLI),
		Incarnation: "inc-previous",
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	if errcode.CodeOf(err) != errcode.InteractionStaleIncarnation {
		t.Fatalf("Answer stale = %v, want stale_incarnation", err)
	}

	// Option not offered.
	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-unoffered",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedSession,
	})
	if errcode.CodeOf(err) != errcode.InteractionOptionNotOffered {
		t.Fatalf("Answer unoffered = %v, want option_not_offered", err)
	}

	// Request not found.
	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-missing",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   "req-nonexistent",
		Outcome:     OutcomeAllowedOnce,
	})
	if errcode.CodeOf(err) != errcode.InteractionNotFound {
		t.Fatalf("Answer missing = %v, want not_found", err)
	}
}

func TestInteractionTextTooLarge(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	ticket, err := svc.Ensure(testSpec("op-large", "large text test"))
	if err != nil {
		t.Fatal(err)
	}

	bigNote := string(make([]byte, 5000))
	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-bignote",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeDenied,
		Text:        bigNote,
	})
	if errcode.CodeOf(err) != errcode.InteractionTextTooLarge {
		t.Fatalf("Answer big note = %v, want text_too_large", err)
	}
}

func TestInteractionServiceRelayedRejectsSessionGrant(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	reqID := ensureTestRelayed(t, svc, testSessionID, "turn-1", "vreq-1", "relayed op")

	_, err := svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-relayed-grant",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   reqID,
		Outcome:     OutcomeAllowedSession,
	})
	if errcode.CodeOf(err) != errcode.InteractionOptionNotOffered {
		t.Fatalf("Answer relayed with session grant = %v, want option_not_offered", err)
	}
}

func TestInteractionServiceConcurrentAnswers(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	ticket, err := svc.Ensure(testSpec("op-race", "race test"))
	if err != nil {
		t.Fatal(err)
	}

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)

	successes := 0
	alreadyAnswered := 0
	var mu sync.Mutex

	for i := range n {
		idx := i
		supervise.Go(t.Context(), "test.concurrent-answer", func(ctx context.Context) error {
			defer wg.Done()
			cmd := &AnswerInteractionCommand{
				CommandID:   fmt.Sprintf("cmd-race-%d", idx),
				Principal:   string(KindCLI),
				Incarnation: svc.Incarnation(),
				RequestID:   ticket.RequestID,
				Outcome:     OutcomeAllowedOnce,
			}
			_, aerr := svc.Answer(ctx, cmd)
			mu.Lock()
			defer mu.Unlock()
			if aerr == nil {
				successes++
			} else if errcode.CodeOf(aerr) == errcode.InteractionAlreadyAnswered {
				alreadyAnswered++
			}
			return nil
		})
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes)
	}
	if alreadyAnswered != n-1 {
		t.Fatalf("alreadyAnswered = %d, want %d", alreadyAnswered, n-1)
	}
}

func TestInteractionServiceTakeRelayed(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	opID, err := interaction.VendorOperationKey(testSessionID, "turn-1", "vreq-1")
	if err != nil {
		t.Fatal(err)
	}
	spec := &interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Summary:     "take relayed test",
		Relayed:     true,
		Options: []interaction.Option{
			{ID: testAllow, Label: "Allow"},
			{ID: "deny", Label: "Deny"},
		},
		Binding: interaction.Binding{
			SessionID:       testSessionID,
			TurnID:          "turn-1",
			VendorRequestID: "vreq-1",
		},
	}
	ticket, err := svc.Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}

	_, rerr := svc.TakeRelayed(opID)
	if errcode.CodeOf(rerr) != errcode.FailedPrecondition {
		t.Fatalf("TakeRelayed unresolved = %v, want failed_precondition", rerr)
	}

	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-take-1",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		OptionID:    testAllow,
	})
	if err != nil {
		t.Fatal(err)
	}

	taken, err := svc.TakeRelayed(opID)
	if err != nil {
		t.Fatal(err)
	}
	if taken.RequestID != ticket.RequestID {
		t.Fatalf("taken id = %s, want %s", taken.RequestID, ticket.RequestID)
	}
}

func TestInteractionServiceWithOptions(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	reqID := ensureTestConfirm(t, svc, "op-confirm-opt", "confirm with option")
	if reqID == "" {
		t.Fatal("empty reqID")
	}

	spec := &interaction.Spec{
		OperationID: "op-choice",
		Kind:        interaction.KindSelect,
		Summary:     "choose item",
		Options: []interaction.Option{
			{ID: "opt-1", Label: "Option 1"},
			{ID: "opt-2", Label: "Option 2"},
		},
		Binding: interaction.Binding{
			SessionID:       "s-1",
			InvocationID:    testInvID,
			AttemptID:       testAttID,
			CheckGeneration: 1,
			ToolName:        testToolID,
			EnvTag:          testEnvDev,
		},
	}
	ticket, err := svc.Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-bad-opt",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
		OptionID:    "invalid-opt",
	})
	if errcode.CodeOf(err) != errcode.InteractionOptionNotOffered {
		t.Fatalf("want InteractionOptionNotOffered, got %v", err)
	}

	res, err := svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-good-opt",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
		OptionID:    "opt-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAllowedOnce {
		t.Fatalf("got %s, want allowed_once", res.Outcome)
	}
}

func questionTestSpec(opID string) *interaction.Spec {
	return &interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindQuestion,
		Summary:     "answer questions",
		Questions: []interaction.Question{
			{
				ID:       "q1",
				Question: "what is your name?",
			},
		},
		Binding: interaction.Binding{
			SessionID:       "s-1",
			InvocationID:    testInvID,
			AttemptID:       testAttID,
			CheckGeneration: 1,
			ToolName:        testToolID,
			EnvTag:          testEnvDev,
		},
	}
}

func TestInteractionServiceAnswersLimits(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	ticket, err := svc.Ensure(questionTestSpec("op-q-limits"))
	if err != nil {
		t.Fatal(err)
	}

	bigAnswer := string(make([]byte, 17000))
	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-q-large",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		Answers: []InteractionAnswer{
			{QuestionID: "q1", Text: bigAnswer},
		},
	})
	if errcode.CodeOf(err) != errcode.InteractionTextTooLarge {
		t.Fatalf("want InteractionTextTooLarge, got %v", err)
	}

	tooMany := make([]InteractionAnswer, 25)
	for i := range tooMany {
		tooMany[i] = InteractionAnswer{QuestionID: fmt.Sprintf("q%d", i)}
	}
	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-q-many",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		Answers:     tooMany,
	})
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}

	tooManyOpts := make([]string, 25)
	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-q-many-opts",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		Answers: []InteractionAnswer{
			{QuestionID: "q1", OptionIDs: tooManyOpts},
		},
	})
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
}

func TestInteractionServiceAnswersSuccess(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	ticket, err := svc.Ensure(questionTestSpec("op-q-success"))
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-q-valid",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAnswered,
		Answers: []InteractionAnswer{
			{QuestionID: "q1", Text: "Fugue"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAnswered {
		t.Fatalf("outcome = %s, want answered", res.Outcome)
	}
}

func TestInteractionServiceConstructorValidation(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("test"))
	owner, err := interaction.NewOwner(interaction.Config{Clock: clk, IDs: idGen})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := NewInteractionService(nil, nil, clk); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("want InvalidArgument for nil owner, got %v", err)
	}
	if _, err := NewInteractionService(owner, nil, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("want InvalidArgument for nil clock, got %v", err)
	}

	cache := newCommandDedupCache(clk, 0, 0)
	if cache.capacity <= 0 || cache.ttl <= 0 {
		t.Fatalf("expected positive default capacity and ttl, got cap=%d ttl=%v", cache.capacity, cache.ttl)
	}
}

func TestHostRuntimeInteractionMethods(t *testing.T) {
	t.Parallel()
	rt := &HostRuntime{}
	ctx := t.Context()

	if _, _, err := rt.ListPendingInteractions(ctx, string(KindCLI)); errcode.CodeOf(err) != errcode.Unavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
	if _, err := rt.AnswerInteraction(ctx, &AnswerInteractionCommand{}); errcode.CodeOf(err) != errcode.Unavailable {
		t.Fatalf("want Unavailable, got %v", err)
	}
	if rt.Interactions() != nil {
		t.Fatal("expected nil interactions")
	}

	svc, _ := newTestInteractionService(t, nil)
	rt.SetInteractions(svc)
	if rt.Interactions() != svc {
		t.Fatal("expected set interactions to match")
	}

	views, _, err := rt.ListPendingInteractions(ctx, string(KindCLI))
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 {
		t.Fatalf("views = %d, want 0", len(views))
	}
}

func TestInteractionEnsurePreservesErrorTypes(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)

	_, err := svc.Ensure(&interaction.Spec{})
	if err == nil {
		t.Fatal("expected error on empty spec")
	}
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("code = %v, want %v", errcode.CodeOf(err), errcode.InvalidArgument)
	}

	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected err to unwrap to *errcode.Error, got %T", err)
	}
}

// TestInteractionListPendingRequestInReadGapIsNotLost races an Ensure into the
// gap between ListPending's two reads. The client must still learn the new
// request: from the views, or from the stream after the returned sequence.
func TestInteractionListPendingRequestInReadGapIsNotLost(t *testing.T) {
	t.Parallel()
	svc, hub := newTestInteractionService(t, nil)

	var racedID string
	svc.betweenListReads = func() {
		svc.betweenListReads = nil
		racedID = ensureTestConfirm(t, svc, "op-gap", "raced into the read gap")
	}

	views, seq, err := svc.ListPending(t.Context(), string(KindCLI))
	if err != nil {
		t.Fatal(err)
	}
	if racedID == "" {
		t.Fatal("the read-gap hook did not run")
	}
	for _, v := range views {
		if v.RequestID == racedID {
			return
		}
	}

	sub, err := hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI),
		Topic:     TopicInteraction,
		Cursor:    &EventCursor{Topic: TopicInteraction, Incarnation: testAppIncarnation, Sequence: seq},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	// A cancelled context makes Next return only events already buffered.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	ev, ok := sub.Next(ctx)
	if !ok || ev.Type != EventInteractionRequested {
		t.Fatalf("request %s is not in the views (seq %d) and its requested event is not replayed after seq", racedID, seq)
	}
	var got PendingInteractionView
	if err := json.Unmarshal(ev.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestID != racedID {
		t.Fatalf("replayed request = %s, want %s", got.RequestID, racedID)
	}
}
