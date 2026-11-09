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
)

func runMemoryCycle(ctx context.Context, svc *InteractionService, i uint64) error {
	opID := fmt.Sprintf("op-cycle-%d", i)
	ticket, eerr := svc.Ensure(&interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Risk:        "test",
		Summary:     "cycle test summary",
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
		Binding: interaction.Binding{
			SessionID:       lifecycleSessionID,
			InvocationID:    "inv-1",
			AttemptID:       "att-1",
			CheckGeneration: i + 1,
			ToolName:        "tester",
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

func TestInteractionServiceMemoryBounded10kCycles(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(100, 0))
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("memtest"))
	hub := NewEventHub("inc-mem")
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

	const totalCycles = 10000
	for i := range uint64(totalCycles) {
		if cerr := runMemoryCycle(t.Context(), svc, i); cerr != nil {
			t.Fatalf("cycle %d failed: %v", i, cerr)
		}
	}

	svc.seenMu.Lock()
	seenCount := len(svc.seenRequests)
	svc.seenMu.Unlock()
	if seenCount != 0 {
		t.Fatalf("seenRequests leaked resolved entries: count = %d, want 0", seenCount)
	}

	svc.topic.mu.Lock()
	eventsCount := len(svc.topic.events)
	topicCap := svc.topic.capacity
	svc.topic.mu.Unlock()
	if eventsCount > topicCap {
		t.Fatalf("topic.events exceeded capacity: count = %d, capacity = %d", eventsCount, topicCap)
	}

	// Cursors older than the ring buffer report ErrEventSeqTooOld
	_, err = svc.topic.Since(t.Context(), 1)
	if !errors.Is(err, ErrEventSeqTooOld) {
		t.Fatalf("topic.Since with old seq = %v, want ErrEventSeqTooOld", err)
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
