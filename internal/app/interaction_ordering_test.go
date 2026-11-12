package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

type recordedEvent struct {
	Type      string
	RequestID string
	Outcome   string
}

type orderTracker struct {
	mu     sync.Mutex
	events []recordedEvent
	byReq  map[string][]string
}

func newOrderTracker() *orderTracker {
	return &orderTracker{
		byReq: make(map[string][]string),
	}
}

func (ot *orderTracker) record(evType, reqID, outcome string) {
	ot.mu.Lock()
	defer ot.mu.Unlock()
	ot.events = append(ot.events, recordedEvent{
		Type:      evType,
		RequestID: reqID,
		Outcome:   outcome,
	})
	ot.byReq[reqID] = append(ot.byReq[reqID], evType)
}

func (ot *orderTracker) verify(t *testing.T, pregrantedReqs map[string]bool) {
	t.Helper()
	ot.mu.Lock()
	defer ot.mu.Unlock()

	for reqID, evList := range ot.byReq {
		if pregrantedReqs[reqID] {
			verifyPregranted(t, reqID, evList)
		} else {
			verifyPendingOrder(t, reqID, evList)
		}
	}
}

func verifyPregranted(t *testing.T, reqID string, evList []string) {
	t.Helper()
	for _, ev := range evList {
		if ev == EventInteractionRequested {
			t.Fatalf("request %s was pre-granted but emitted requested event", reqID)
		}
	}
}

func verifyPendingOrder(t *testing.T, reqID string, evList []string) {
	t.Helper()
	requestedIdx, resolvedIdx := -1, -1
	for i, ev := range evList {
		if ev == EventInteractionRequested && requestedIdx == -1 {
			requestedIdx = i
		}
		if ev == EventInteractionResolved && resolvedIdx == -1 {
			resolvedIdx = i
		}
	}

	if requestedIdx != -1 && resolvedIdx != -1 && resolvedIdx < requestedIdx {
		t.Fatalf("request %s: resolved at %d before requested at %d", reqID, resolvedIdx, requestedIdx)
	}
	if resolvedIdx != -1 && requestedIdx == -1 {
		t.Fatalf("request %s: resolved without requested", reqID)
	}
}

func runEnsurerLoop(
	svc *InteractionService,
	clk *clock.Fake,
	startCh <-chan struct{},
	totalRequests int,
	pregrantedMu *sync.Mutex,
	pregranted map[string]bool,
) error {
	<-startCh
	for i := range totalRequests {
		sessID, expiresAt := "sess-normal", time.Time{}
		if i%4 == 0 {
			sessID = "sess-pregranted"
		} else if i%3 == 0 {
			expiresAt = clk.Now().Add(time.Second)
		}

		ticket, eerr := svc.Ensure(&interaction.Spec{
			OperationID: fmt.Sprintf("op-race-%d", i),
			Kind:        interaction.KindConfirm,
			Risk:        "read",
			Summary:     "race test prompt",
			Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession},
			ExpiresAt:   expiresAt,
			Binding: interaction.Binding{
				SessionID:       sessID,
				WorkspaceID:     "ws-1",
				InvocationID:    fmt.Sprintf("inv-%d", i),
				AttemptID:       testAttID,
				CheckGeneration: uint64(i + 1),
				ToolName:        "tool-race",
				GrantScopeKey:   "tool-race:scope",
				TargetRevision:  "rev-1",
				EnvRevision:     "env-1",
				EnvTag:          lifecycleEnvDev,
			},
		})
		if eerr == nil && ticket.Resolved {
			pregrantedMu.Lock()
			pregranted[ticket.RequestID] = true
			pregrantedMu.Unlock()
		}
	}
	return nil
}

func answerViews(ctx context.Context, svc *InteractionService, views []PendingInteractionView, answered map[string]bool) {
	for i := range views {
		v := &views[i]
		if answered[v.RequestID] {
			continue
		}
		answered[v.RequestID] = true
		outcome := OutcomeAllowedOnce
		if len(v.OfferedOutcomes) > 0 {
			outcome = v.OfferedOutcomes[0]
		}
		if _, aerr := svc.Answer(ctx, &AnswerInteractionCommand{
			CommandID:   "cmd-race-" + v.RequestID,
			Principal:   string(KindCLI),
			Incarnation: svc.Incarnation(),
			RequestID:   v.RequestID,
			Outcome:     outcome,
		}); aerr != nil {
			_ = aerr
		}
	}
}

func runPendingAnswerWorker(ctx context.Context, svc *InteractionService, startCh <-chan struct{}) error {
	<-startCh
	answered := make(map[string]bool)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		views, _, perr := svc.ListPending(ctx, string(KindCLI))
		if perr != nil {
			return fmt.Errorf("list pending: %w", perr)
		}
		answerViews(ctx, svc, views, answered)
		runtime.Gosched()
	}
}

func runExpirerWorker(ctx context.Context, clk *clock.Fake, svc *InteractionService, startCh <-chan struct{}) error {
	<-startCh
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			clk.Advance(2 * time.Second)
			svc.ExpireExpired()
			runtime.Gosched()
		}
	}
}

func drainRemainingPending(ctx context.Context, clk *clock.Fake, svc *InteractionService) {
	for range 100 {
		if len(svc.currentPendingViews()) == 0 {
			break
		}
		views, _, err := svc.ListPending(ctx, string(KindCLI))
		if err != nil {
			return
		}
		for i := range views {
			v := &views[i]
			if _, aerr := svc.Answer(ctx, &AnswerInteractionCommand{
				CommandID:   "cmd-drain-" + v.RequestID,
				Principal:   string(KindCLI),
				Incarnation: svc.Incarnation(),
				RequestID:   v.RequestID,
				Outcome:     OutcomeAllowedOnce,
			}); aerr != nil {
				_ = aerr
			}
		}
		clk.Advance(10 * time.Second)
		svc.ExpireExpired()
		runtime.Gosched()
	}
}

func recordSubEvent(tracker *orderTracker, ev Event) {
	switch ev.Type {
	case EventInteractionRequested:
		var view PendingInteractionView
		if jerr := json.Unmarshal(ev.Payload, &view); jerr == nil {
			tracker.record(ev.Type, view.RequestID, "")
		}
	case EventInteractionResolved:
		var res resolvedPayload
		if jerr := json.Unmarshal(ev.Payload, &res); jerr == nil {
			tracker.record(ev.Type, res.RequestID, res.Outcome)
		}
	}
}

func setupSubscriber(
	ctx context.Context,
	t *testing.T,
	hub *EventHub,
	tracker *orderTracker,
	subWg *sync.WaitGroup,
) *Subscription {
	t.Helper()
	sub, err := hub.Subscribe(ctx, SubscribeParams{
		Principal: string(KindCLI),
		Topic:     TopicInteraction,
	})
	if err != nil {
		t.Fatal(err)
	}

	subWg.Add(1)
	supervise.Go(ctx, "test.subscriber", func(taskCtx context.Context) error {
		defer subWg.Done()
		for {
			ev, ok := sub.Next(taskCtx)
			if !ok {
				return nil
			}
			recordSubEvent(tracker, ev)
		}
	})
	return sub
}

func setupOrderingService(t *testing.T) (*clock.Fake, *EventHub, *InteractionService) {
	t.Helper()
	clk := clock.NewFake(time.Unix(100, 0))
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("race-order"))
	hub := NewEventHub("inc-race-order")
	owner, err := interaction.NewOwner(&interaction.Config{
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
			t.Errorf("task wait: %v", werr)
		}
	})
	return clk, hub, svc
}

func spawnOrderingWorkers(
	ctx context.Context,
	svc *InteractionService,
	clk *clock.Fake,
	startCh <-chan struct{},
	totalRequests int,
	pregrantedMu *sync.Mutex,
	pregranted map[string]bool,
	ensurerWg, workersWg *sync.WaitGroup,
) {
	ensurerWg.Add(1)
	supervise.Go(ctx, "test.ensurer", func(_ context.Context) error {
		defer ensurerWg.Done()
		return runEnsurerLoop(svc, clk, startCh, totalRequests, pregrantedMu, pregranted)
	})

	const answererCount = 2
	for aIdx := range answererCount {
		workersWg.Add(1)
		supervise.Go(ctx, fmt.Sprintf("test.answerer-%d", aIdx), func(_ context.Context) error {
			defer workersWg.Done()
			return runPendingAnswerWorker(ctx, svc, startCh)
		})
	}

	workersWg.Add(1)
	supervise.Go(ctx, "test.expirer", func(_ context.Context) error {
		defer workersWg.Done()
		return runExpirerWorker(ctx, clk, svc, startCh)
	})
}

func TestInteractionOrderingRequestedBeforeResolvedUnderRace(t *testing.T) {
	t.Parallel()
	clk, hub, svc := setupOrderingService(t)

	subCtx, cancelSub := context.WithCancel(t.Context())
	defer cancelSub()

	tracker := newOrderTracker()
	var subWg sync.WaitGroup
	sub := setupSubscriber(subCtx, t, hub, tracker, &subWg)
	defer sub.Close()

	const totalRequests = 200
	pregranted := make(map[string]bool)
	var pregrantedMu sync.Mutex

	seedSessionGrant(t, svc, "sess-pregranted")

	var (
		ensurerWg sync.WaitGroup
		workersWg sync.WaitGroup
		startCh   = make(chan struct{})
	)

	workerCtx, cancelWorkers := context.WithCancel(t.Context())
	defer cancelWorkers()

	spawnOrderingWorkers(workerCtx, svc, clk, startCh, totalRequests, &pregrantedMu, pregranted, &ensurerWg, &workersWg)

	close(startCh)
	ensurerWg.Wait()

	drainRemainingPending(workerCtx, clk, svc)

	cancelWorkers()
	workersWg.Wait()

	cancelSub()
	subWg.Wait()

	tracker.verify(t, pregranted)
}

func seedSessionGrant(t *testing.T, svc *InteractionService, sessionID string) {
	t.Helper()
	ticket, err := svc.Ensure(&interaction.Spec{
		OperationID: "op-seed-grant",
		Kind:        interaction.KindConfirm,
		Risk:        "read",
		Summary:     "seed grant",
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce, interaction.OutcomeAllowedSession},
		Binding: interaction.Binding{
			SessionID:       sessionID,
			WorkspaceID:     "ws-1",
			InvocationID:    "inv-seed",
			AttemptID:       testAttID,
			CheckGeneration: 1,
			ToolName:        "tool-race",
			GrantScopeKey:   "tool-race:scope",
			TargetRevision:  "rev-1",
			EnvRevision:     "env-1",
			EnvTag:          lifecycleEnvDev,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, aerr := svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-seed-grant",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedSession,
	}); aerr != nil {
		t.Fatal(aerr)
	}
}
