package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	lifecycleSessionID = "sess-1"
	lifecycleEnvDev    = "dev"
	lifecycleRiskTest  = "test"
	lifecycleToolTest  = "tester"
)

func runMemoryCycle(ctx context.Context, svc *InteractionService, i uint64) error {
	opID := fmt.Sprintf("op-cycle-%d", i)
	ticket, eerr := svc.Ensure(&interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Risk:        lifecycleRiskTest,
		Summary:     "cycle test summary",
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
		Binding: interaction.Binding{
			SessionID:       lifecycleSessionID,
			InvocationID:    testInvID,
			AttemptID:       testAttID,
			CheckGeneration: i + 1,
			ToolName:        lifecycleToolTest,
			EnvTag:          lifecycleEnvDev,
		},
	})
	if eerr != nil {
		return eerr
	}

	cmdID := fmt.Sprintf("cmd-cycle-%d", i)
	_, aerr := svc.Answer(ctx, &AnswerInteractionCommand{
		CommandID:   cmdID,
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	return aerr
}

func runMemoryCycles(t *testing.T, ctx context.Context, svc *InteractionService, start, end uint64) {
	t.Helper()
	for i := start; i < end; i++ {
		if err := runMemoryCycle(ctx, svc, i); err != nil {
			t.Fatalf("cycle %d failed: %v", i, err)
		}
	}
}

func TestInteractionServiceMemoryBounded10kCycles(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("memtest"))
	hub := NewEventHub("inc-mem")
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

	const measuredCycles = 1000
	const totalCycles = 10000
	realClock := clock.Real{}
	firstStart := realClock.Now()
	runMemoryCycles(t, t.Context(), svc, 0, measuredCycles)
	firstDuration := realClock.Now().Sub(firstStart)
	runMemoryCycles(t, t.Context(), svc, measuredCycles, totalCycles-measuredCycles)
	lastStart := realClock.Now()
	runMemoryCycles(t, t.Context(), svc, totalCycles-measuredCycles, totalCycles)
	lastDuration := realClock.Now().Sub(lastStart)
	t.Logf("first 1,000 cycles: %s; last 1,000 cycles: %s", firstDuration, lastDuration)
	if lastDuration > firstDuration*3 {
		t.Fatalf("last 1,000 cycles took %s, more than 3x first 1,000 cycles %s", lastDuration, firstDuration)
	}

	// Advance clock past ResolvedRetentionTTL so all tickets and receipts become evictable
	clk.Advance(interaction.ResolvedRetentionTTL + time.Minute)
	svc.PruneResolved()

	reqCount, opCount, rCount := owner.EntryCounts()
	if reqCount != 0 || opCount != 0 || rCount != 0 {
		t.Fatalf("owner memory not bounded after retention TTL: byRequest=%d, byOperation=%d, receipts=%d, want all 0",
			reqCount, opCount, rCount)
	}

	// Answering an evicted request returns ErrInteractionNotFound
	_, aerr := svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-evicted-test",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   "non-existent-or-evicted-id",
		Outcome:     OutcomeAllowedOnce,
	})
	if errcode.CodeOf(aerr) != errcode.InteractionNotFound {
		t.Fatalf("Answer on evicted request = %v, want InteractionNotFound", aerr)
	}

	// ListPending shows 0 pending items
	views, _, perr := svc.ListPending(t.Context(), string(KindCLI))
	if perr != nil {
		t.Fatal(perr)
	}
	if len(views) != 0 {
		t.Fatalf("ListPending returned %d views, want 0", len(views))
	}
}

func runEnsurerWorker(svc *InteractionService, startCh <-chan struct{}, ticketsCh chan<- *interaction.Ticket, count int) error {
	<-startCh
	for i := range count {
		ticket, eerr := svc.Ensure(&interaction.Spec{
			OperationID: fmt.Sprintf("op-conc-%d", i),
			Kind:        interaction.KindConfirm,
			Risk:        "test",
			Summary:     "concurrent ensure",
			Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
			Binding: interaction.Binding{
				SessionID:       fmt.Sprintf("sess-conc-%d", i),
				InvocationID:    "inv-1",
				AttemptID:       "att-1",
				CheckGeneration: uint64(i + 1),
				ToolName:        "tester",
				EnvTag:          lifecycleEnvDev,
			},
		})
		if eerr != nil {
			return eerr
		}
		ticketsCh <- ticket
	}
	return nil
}

func runAnswerWorker(ctx context.Context, svc *InteractionService, startCh <-chan struct{}, ticketsCh <-chan *interaction.Ticket) error {
	<-startCh
	for ticket := range ticketsCh {
		_, aerr := svc.Answer(ctx, &AnswerInteractionCommand{
			CommandID:   "cmd-ans-" + ticket.RequestID,
			Principal:   string(KindCLI),
			Incarnation: svc.Incarnation(),
			RequestID:   ticket.RequestID,
			Outcome:     OutcomeAllowedOnce,
		})
		if aerr != nil && !errors.Is(aerr, interaction.ErrAlreadyAnswered) {
			return aerr
		}
	}
	return nil
}

func readSubEvents(ctx context.Context, t *testing.T, sub *Subscription, initialPending map[string]struct{}) {
	t.Helper()
	for {
		ev, ok := sub.Next(ctx)
		if !ok {
			if sub.ResyncRequired() {
				t.Error("subscriber received unexpected resync_required")
			}
			return
		}
		if ev.Type == EventInteractionRequested {
			var view PendingInteractionView
			if err := json.Unmarshal(ev.Payload, &view); err == nil {
				if _, wasInSnap := initialPending[view.RequestID]; wasInSnap {
					t.Errorf("subscriber received requested event for request %q already present in snapshot", view.RequestID)
				}
			}
		}
	}
}

func subscribeAndSnapshot(ctx context.Context, t *testing.T, hub *EventHub) (*Subscription, map[string]struct{}) {
	t.Helper()
	sub, serr := hub.Subscribe(ctx, SubscribeParams{
		Topic:     TopicInteraction,
		Principal: string(KindCLI),
	})
	if serr != nil {
		t.Fatal(serr)
	}
	snapBytes, _, _ := sub.Snapshot()
	initialPending := map[string]struct{}{}
	if len(snapBytes) > 0 {
		var views []PendingInteractionView
		if err := json.Unmarshal(snapBytes, &views); err == nil {
			for i := range views {
				initialPending[views[i].RequestID] = struct{}{}
			}
		}
	}
	return sub, initialPending
}

func TestInteractionConcurrentEnsureAnswerSubscribeConsistency(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("conctest"))
	hub := NewEventHub("inc-race")
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

	const workCount = 40
	var (
		workWg  sync.WaitGroup
		subWg   sync.WaitGroup
		startCh = make(chan struct{})
	)

	subCtx, cancelSub := context.WithCancel(t.Context())
	defer cancelSub()

	sub, initialPending := subscribeAndSnapshot(subCtx, t, hub)
	defer sub.Close()

	subWg.Add(1)
	supervise.Go(t.Context(), "test.subscriber", func(_ context.Context) error {
		defer subWg.Done()
		readSubEvents(subCtx, t, sub, initialPending)
		return nil
	})

	ticketsCh := make(chan *interaction.Ticket, workCount)

	workWg.Add(1)
	supervise.Go(t.Context(), "test.ensurer", func(_ context.Context) error {
		defer workWg.Done()
		defer close(ticketsCh)
		return runEnsurerWorker(svc, startCh, ticketsCh, workCount)
	})

	const workerCount = 4
	for range workerCount {
		workWg.Add(1)
		supervise.Go(t.Context(), "test.worker", func(ctx context.Context) error {
			defer workWg.Done()
			return runAnswerWorker(ctx, svc, startCh, ticketsCh)
		})
	}

	close(startCh)
	workWg.Wait()
	cancelSub()
	subWg.Wait()
}

func TestCommandDedupCacheAllRecordsExpiredClearsAll(t *testing.T) {
	t.Parallel()
	fakeClk := clock.NewFake(time.Unix(1000, 0))
	c := newCommandDedupCache(fakeClk, 10, 5*time.Second)

	c.Put("cmd-1", AnswerInteractionResult{RequestID: "req-1", Outcome: "ok"})
	c.Put("cmd-2", AnswerInteractionResult{RequestID: "req-2", Outcome: "ok"})

	if _, ok := c.Get("cmd-1"); !ok {
		t.Fatal("expected cmd-1 to be present")
	}

	// Advance time past TTL so all records are expired
	fakeClk.Advance(10 * time.Second)

	// Get triggers evictExpiredLocked
	if _, ok := c.Get("cmd-1"); ok {
		t.Fatal("cmd-1 should have been evicted")
	}
	if _, ok := c.Get("cmd-2"); ok {
		t.Fatal("cmd-2 should have been evicted")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.records) != 0 {
		t.Fatalf("records slice not empty after all expired: len = %d", len(c.records))
	}
	if len(c.entries) != 0 {
		t.Fatalf("entries map not empty after all expired: len = %d", len(c.entries))
	}
}

func TestCommandDedupCacheOldRecordDoesNotEvictNewerSameID(t *testing.T) {
	t.Parallel()
	fakeClk := clock.NewFake(time.Unix(1000, 0))
	c := newCommandDedupCache(fakeClk, 10, 5*time.Second)

	// Insert cmd-1 at T=1000
	c.Put("cmd-1", AnswerInteractionResult{RequestID: "req-old", Outcome: "old"})

	// Advance time by 3 seconds (T=1003) and re-insert cmd-1 with newer result
	fakeClk.Advance(3 * time.Second)
	c.Put("cmd-1", AnswerInteractionResult{RequestID: "req-new", Outcome: "new"})

	// Advance time by 3 seconds (T=1006). Old record at T=1000 is now expired (cutoff = 1001),
	// but newer record at T=1003 is NOT expired (expires at 1008).
	fakeClk.Advance(3 * time.Second)

	res, ok := c.Get("cmd-1")
	if !ok {
		t.Fatal("cmd-1 should still be present via the newer record")
	}
	if res.Outcome != "new" {
		t.Fatalf("outcome = %q, want %q", res.Outcome, "new")
	}
}

func TestCommandDedupCacheStrictCapacityBound(t *testing.T) {
	t.Parallel()
	fakeClk := clock.NewFake(time.Unix(1000, 0))
	const capLimit = 5
	c := newCommandDedupCache(fakeClk, capLimit, 1*time.Hour)

	for i := range 20 {
		c.Put(fmt.Sprintf("cmd-%d", i), AnswerInteractionResult{
			RequestID: fmt.Sprintf("req-%d", i),
			Outcome:   "ok",
		})
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) > capLimit {
		t.Fatalf("entries map length %d exceeds capacity %d", len(c.entries), capLimit)
	}
}

func TestInteractionConcurrentAnswerDedupSingleflight(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	ticket, err := svc.Ensure(testSpec("op-singleflight", "singleflight test"))
	if err != nil {
		t.Fatal(err)
	}

	const callerCount = 8
	var (
		wg      sync.WaitGroup
		results = make([]AnswerInteractionResult, callerCount)
		errs    = make([]error, callerCount)
		startCh = make(chan struct{})
	)

	for i := range callerCount {
		idx := i
		wg.Add(1)
		supervise.Go(t.Context(), fmt.Sprintf("test.singleflight-%d", idx), func(ctx context.Context) error {
			defer wg.Done()
			<-startCh
			res, aerr := svc.Answer(ctx, &AnswerInteractionCommand{
				CommandID:   "cmd-shared-id",
				Principal:   string(KindCLI),
				Incarnation: svc.Incarnation(),
				RequestID:   ticket.RequestID,
				Outcome:     OutcomeAllowedOnce,
			})
			results[idx] = res
			errs[idx] = aerr
			return nil
		})
	}

	close(startCh)
	wg.Wait()

	for i := range callerCount {
		if errs[i] != nil {
			t.Fatalf("caller %d failed: %v", i, errs[i])
		}
		if results[i].RequestID != ticket.RequestID || results[i].Outcome != OutcomeAllowedOnce {
			t.Fatalf("caller %d got unexpected result: %+v", i, results[i])
		}
	}
}

func TestInteractionEmptyIncarnationInvalidArgument(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)
	ticket, err := svc.Ensure(testSpec("op-empty-inc", "empty incarnation test"))
	if err != nil {
		t.Fatal(err)
	}

	_, aerr := svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-empty-inc",
		Principal:   string(KindCLI),
		Incarnation: "",
		RequestID:   ticket.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	if errcode.CodeOf(aerr) != errcode.InvalidArgument {
		t.Fatalf("Answer with empty incarnation = %v, want InvalidArgument", aerr)
	}
}

func TestInteractionAuthoritativeLimitsEnforcedInApp(t *testing.T) {
	t.Parallel()
	svc, _ := newTestInteractionService(t, nil)

	// Risk > 32 bytes
	longRiskSpec := testSpec("op-risk-limit", "risk limit test")
	longRiskSpec.Risk = strings.Repeat("r", 33)
	_, rErr := svc.Ensure(longRiskSpec)
	if errcode.CodeOf(rErr) != errcode.InvalidArgument {
		t.Fatalf("Ensure with risk > 32 = %v, want InvalidArgument", rErr)
	}

	// GrantScopeLabel > 256 bytes
	longGrantSpec := testSpec("op-grant-limit", "grant limit test")
	longGrantSpec.GrantScopeLabel = strings.Repeat("g", 257)
	_, gErr := svc.Ensure(longGrantSpec)
	if errcode.CodeOf(gErr) != errcode.InvalidArgument {
		t.Fatalf("Ensure with GrantScopeLabel > 256 = %v, want InvalidArgument", gErr)
	}
}

func TestInteractionDedupMismatchedRequestOrOutcomeReturnsInvalidArgument(t *testing.T) {
	t.Parallel()
	const cmdDedupID = "cmd-dedup-mismatch"
	svc, _ := newTestInteractionService(t, nil)
	spec1 := testSpec("op-dedup-1", "dedup mismatch 1")
	spec1.Binding.SessionID = "sess-dedup-1"
	t1 := mustEnsure(t, svc, spec1)

	spec2 := testSpec("op-dedup-2", "dedup mismatch 2")
	spec2.Binding.SessionID = "sess-dedup-2"
	t2 := mustEnsure(t, svc, spec2)

	// First answer succeeds
	_, err := svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   cmdDedupID,
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   t1.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	if err != nil {
		t.Fatalf("first answer failed: %v", err)
	}

	// Same command_id, different request_id -> InvalidArgument
	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   cmdDedupID,
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   t2.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("Answer with different request_id = %v, want InvalidArgument", err)
	}

	// Same command_id, same request_id, different outcome -> InvalidArgument
	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   cmdDedupID,
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   t1.RequestID,
		Outcome:     OutcomeDenied,
	})
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("Answer with different outcome = %v, want InvalidArgument", err)
	}
}

func nextSubEvent(t *testing.T, sub *Subscription) Event {
	t.Helper()
	ev, ok := sub.Next(t.Context())
	if !ok {
		t.Fatal("expected subscription event")
	}
	return ev
}

func TestInteractionEnsureResolvedDoesNotEmitDuplicateEvent(t *testing.T) {
	t.Parallel()
	svc, hub := newTestInteractionService(t, nil)
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI),
		Topic:     TopicInteraction,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)

	spec := testSpec("op-reensure", "reensure test")
	t1 := mustEnsure(t, svc, spec)

	evReq := nextSubEvent(t, sub)
	if evReq.Type != EventInteractionRequested {
		t.Fatalf("expected requested event, got %+v", evReq)
	}

	_, err = svc.Answer(t.Context(), &AnswerInteractionCommand{
		CommandID:   "cmd-reensure",
		Principal:   string(KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   t1.RequestID,
		Outcome:     OutcomeAllowedOnce,
	})
	if err != nil {
		t.Fatal(err)
	}

	evRes := nextSubEvent(t, sub)
	assertResolvedEventPayload(t, evRes, t1.RequestID, OutcomeAllowedOnce)

	t2, err := svc.Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	if t2.RequestID != t1.RequestID {
		t.Fatalf("re-ensured ticket request ID = %s, want %s", t2.RequestID, t1.RequestID)
	}

	tNew := mustEnsure(t, svc, testSpec("op-new-after-reensure", "new spec"))
	evNext := nextSubEvent(t, sub)
	if evNext.Type != EventInteractionRequested {
		t.Fatalf("expected requested event for new spec, got %+v", evNext)
	}
	var view PendingInteractionView
	if jerr := json.Unmarshal(evNext.Payload, &view); jerr != nil {
		t.Fatal(jerr)
	}
	if view.RequestID != tNew.RequestID {
		t.Fatalf("expected event for new request %s, got %s", tNew.RequestID, view.RequestID)
	}
}

func setupResolveTestService(t *testing.T, clk clock.Clock) (*InteractionService, *interaction.Owner, *Subscription) {
	t.Helper()
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("resolvetest"))
	hub := NewEventHub("inc-resolve-all")
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
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI),
		Topic:     TopicInteraction,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	return svc, owner, sub
}

func TestInteractionResolvedEventEmittedOnExpire(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	svc, _, sub := setupResolveTestService(t, clk)

	specExp := testSpec("op-expire", "expire test")
	specExp.ExpiresAt = clk.Now().Add(5 * time.Minute)
	tExp := mustEnsure(t, svc, specExp)
	ev := nextSubEvent(t, sub)
	if ev.Type != EventInteractionRequested {
		t.Fatalf("expected requested, got %+v", ev)
	}
	clk.Advance(10 * time.Minute)
	expired := svc.ExpireExpired()
	if len(expired) != 1 || expired[0].RequestID != tExp.RequestID {
		t.Fatalf("ExpireExpired = %+v, want %s", expired, tExp.RequestID)
	}
	evExp := nextSubEvent(t, sub)
	assertResolvedEventPayload(t, evExp, tExp.RequestID, OutcomeExpired)
}

func TestInteractionResolvedEventEmittedOnInvalidate(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	svc, owner, sub := setupResolveTestService(t, clk)

	tInv := mustEnsure(t, svc, testSpec("op-inv", "invalidate test"))
	ev := nextSubEvent(t, sub)
	if ev.Type != EventInteractionRequested {
		t.Fatalf("expected requested, got %+v", ev)
	}
	if _, ierr := owner.InvalidateOperation("op-inv"); ierr != nil {
		t.Fatal(ierr)
	}
	evInv := nextSubEvent(t, sub)
	assertResolvedEventPayload(t, evInv, tInv.RequestID, OutcomeCancelled)
}

func TestInteractionResolvedEventEmittedOnResetSession(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	svc, owner, sub := setupResolveTestService(t, clk)

	specReset := testSpec("op-sess-reset", "reset test")
	specReset.Binding.SessionID = "sess-to-reset"
	tReset := mustEnsure(t, svc, specReset)
	ev := nextSubEvent(t, sub)
	if ev.Type != EventInteractionRequested {
		t.Fatalf("expected requested, got %+v", ev)
	}
	owner.ResetSession("sess-to-reset")
	evReset := nextSubEvent(t, sub)
	assertResolvedEventPayload(t, evReset, tReset.RequestID, OutcomeCancelled)
}

func TestInteractionResolvedEventEmittedOnVaultLock(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	svc, owner, sub := setupResolveTestService(t, clk)

	tVault := mustEnsure(t, svc, testSpec("op-vault-lock", "vault lock test"))
	ev := nextSubEvent(t, sub)
	if ev.Type != EventInteractionRequested {
		t.Fatalf("expected requested, got %+v", ev)
	}
	owner.SetVaultLocked(true)
	evVault := nextSubEvent(t, sub)
	assertResolvedEventPayload(t, evVault, tVault.RequestID, OutcomeCancelled)
}

func assertEphemeralCursorSnapshot(t *testing.T, hub *EventHub, cursorSeq EventSeq, wantReqID string) {
	t.Helper()
	sub, err := hub.Subscribe(t.Context(), SubscribeParams{
		Principal: string(KindCLI),
		Topic:     TopicInteraction,
		Cursor:    &EventCursor{Topic: TopicInteraction, Incarnation: "inc-host-1", Sequence: cursorSeq},
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	defer sub.Close()

	snapBytes, snapSeq, hasSnap := sub.Snapshot()
	if !hasSnap || snapSeq != 1 {
		t.Fatalf("snapshot = (hasSnap:%v, seq:%d), want (true, 1)", hasSnap, snapSeq)
	}
	var views []PendingInteractionView
	if err := json.Unmarshal(snapBytes, &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].RequestID != wantReqID {
		t.Fatalf("snapshot views = %+v, want 1 request %s", views, wantReqID)
	}
}

func TestInteractionEphemeralReconnectDifferentIncarnationSnapshot(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))

	hub1 := NewEventHub("inc-host-1")
	owner1, err := interaction.NewOwner(&interaction.Config{
		Clock: clk,
		IDs:   ids.NewUUIDv7(clk, ids.NewDeterministic("h1")),
	})
	if err != nil {
		t.Fatal(err)
	}
	svc1, err := NewInteractionService(owner1, hub1, clk)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		spec := testSpec(fmt.Sprintf("op-h1-%d", i), "host1 spec")
		spec.Binding.SessionID = fmt.Sprintf("sess-h1-%d", i)
		mustEnsure(t, svc1, spec)
	}

	hub2 := NewEventHub("inc-host-2")
	owner2, err := interaction.NewOwner(&interaction.Config{
		Clock: clk,
		IDs:   ids.NewUUIDv7(clk, ids.NewDeterministic("h2")),
	})
	if err != nil {
		t.Fatal(err)
	}
	svc2, err := NewInteractionService(owner2, hub2, clk)
	if err != nil {
		t.Fatal(err)
	}

	ticket2 := mustEnsure(t, svc2, testSpec("op-h2-1", "pending in inc2"))
	currSeq := hub2.TopicSequence(TopicInteraction)
	if currSeq != 1 {
		t.Fatalf("hub2 TopicSequence = %d, want 1", currSeq)
	}

	// Case A: Client reconnects with old cursor incarnation "inc-host-1" and seq > current seq (seq = 10)
	assertEphemeralCursorSnapshot(t, hub2, 10, ticket2.RequestID)

	// Case B: Client reconnects with old cursor incarnation "inc-host-1" and seq < current seq (seq = 0)
	assertEphemeralCursorSnapshot(t, hub2, 0, ticket2.RequestID)
}
