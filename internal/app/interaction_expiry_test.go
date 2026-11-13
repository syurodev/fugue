package app

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

const (
	expirySessionID = "sess-1"
	expiryRiskTest  = "test"
	expiryToolTest  = "tester"
)

func waitExpiryTimerArmed(t *testing.T, clk *clock.Fake) {
	t.Helper()
	for range 1000 {
		if clk.Pending() == 1 {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("expected 1 pending timer on fake clock, got %d", clk.Pending())
}

func ensureExpiringTicket(t *testing.T, svc *InteractionService, expiresAt time.Time) *interaction.Ticket {
	t.Helper()
	ticket, err := svc.Ensure(&interaction.Spec{
		OperationID: "op-auto-expire",
		Kind:        interaction.KindConfirm,
		Risk:        expiryRiskTest,
		Summary:     "auto expire test",
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
		ExpiresAt:   expiresAt,
		Binding: interaction.Binding{
			SessionID:       expirySessionID,
			InvocationID:    testInvID,
			AttemptID:       testAttID,
			CheckGeneration: 1,
			ToolName:        expiryToolTest,
			EnvTag:          lifecycleEnvDev,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func assertResolvedExpired(t *testing.T, svc *InteractionService, sub *Subscription, reqID string) {
	t.Helper()
	resEv, ok := sub.Next(t.Context())
	if !ok || resEv.Type != EventInteractionResolved {
		t.Fatalf("expected resolved event, got ok=%v ev=%+v", ok, resEv)
	}

	var payload resolvedPayload
	if uerr := json.Unmarshal(resEv.Payload, &payload); uerr != nil {
		t.Fatal(uerr)
	}
	if payload.RequestID != reqID || payload.Outcome != OutcomeExpired {
		t.Fatalf("payload = %+v, want reqID=%s outcome=%s", payload, reqID, OutcomeExpired)
	}

	found, err := svc.owner.LookupRequest(reqID)
	if err != nil {
		t.Fatal(err)
	}
	if !found.Resolved || found.Outcome != interaction.OutcomeExpired {
		t.Fatalf("ticket resolved=%v outcome=%v, want resolved=true outcome=expired", found.Resolved, found.Outcome)
	}
}

func TestInteractionAutomaticExpiryTimer(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("expiry-test"))
	hub := NewEventHub("inc-expiry")
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
			t.Errorf("task wait failed: %v", werr)
		}
	})

	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	ticket := ensureExpiringTicket(t, svc, clk.Now().Add(5*time.Minute))
	reqEv, ok := sub.Next(t.Context())
	if !ok || reqEv.Type != EventInteractionRequested {
		t.Fatalf("expected requested event, got ok=%v ev=%+v", ok, reqEv)
	}

	waitExpiryTimerArmed(t, clk)
	clk.Advance(5*time.Minute + time.Second)

	assertResolvedExpired(t, svc, sub, ticket.RequestID)
}

func TestInteractionExpiryLoopHandlesOverdueTicketAtStart(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("overdue-start-test"))
	hub := NewEventHub("inc-overdue")
	owner, err := interaction.NewOwner(&interaction.Config{Clock: clk, IDs: idGen})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewInteractionService(owner, hub, clk)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Principal: string(KindCLI), Topic: TopicInteraction})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	ticket := ensureExpiringTicket(t, svc, clk.Now().Add(time.Minute))
	if _, ok := sub.Next(t.Context()); !ok {
		t.Fatal("requested event was not published")
	}
	clk.Advance(2 * time.Minute)

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	task := svc.Start(ctx)
	defer func() {
		cancel()
		if err := task.Wait(); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("task wait failed: %v", err)
		}
	}()

	assertResolvedExpiredWithContext(t, svc, sub, ticket.RequestID, ctx)
}

func assertResolvedExpiredWithContext(t *testing.T, svc *InteractionService, sub *Subscription, reqID string, ctx context.Context) {
	t.Helper()
	resEv, ok := sub.Next(ctx)
	if !ok || resEv.Type != EventInteractionResolved {
		t.Fatalf("expected resolved event, got ok=%v ev=%+v", ok, resEv)
	}
	var payload resolvedPayload
	if err := json.Unmarshal(resEv.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RequestID != reqID || payload.Outcome != OutcomeExpired {
		t.Fatalf("payload = %+v, want reqID=%s outcome=%s", payload, reqID, OutcomeExpired)
	}
	found, err := svc.owner.LookupRequest(reqID)
	if err != nil {
		t.Fatal(err)
	}
	if !found.Resolved || found.Outcome != interaction.OutcomeExpired {
		t.Fatalf("ticket resolved=%v outcome=%v, want resolved=true outcome=expired", found.Resolved, found.Outcome)
	}
}

func TestInteractionExpiryTimerStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("stop-test"))
	hub := NewEventHub("inc-stop")
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

	ctx, cancel := context.WithCancel(t.Context())
	task := svc.Start(ctx)

	cancel()
	if err := task.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected nil or canceled error on cancel, got %v", err)
	}
}

func TestHostProcessStopsInteractionExpiryOnClose(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	hp, err := StartHost(t.Context(), testHostDeps(t.TempDir(), clk, newScriptedTransport()))
	if err != nil {
		t.Fatal(err)
	}

	if hp.Runtime().Interactions() == nil {
		t.Fatal("expected interactions service on runtime")
	}

	if err := hp.Close(); err != nil {
		t.Fatal(err)
	}
}
