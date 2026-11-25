//go:build perf

package perf

import (
	"fmt"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/testutil"
)

const (
	interactionTotalCycles  = 10000
	interactionWindows      = 5
	interactionWindowCycles = 200
)

func runInteractionCycle(t *testing.T, svc *app.InteractionService, i uint64) {
	t.Helper()
	ticket, err := svc.Ensure(&interaction.Spec{
		OperationID: fmt.Sprintf("op-perf-%d", i),
		Kind:        interaction.KindConfirm,
		Risk:        "test",
		Summary:     "perf cycle summary",
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
		Binding: interaction.Binding{
			SessionID:       "sess-perf",
			InvocationID:    "inv-perf",
			AttemptID:       "att-perf",
			CheckGeneration: i + 1,
			ToolName:        "tester",
			EnvTag:          "dev",
		},
	})
	if err != nil {
		t.Fatalf("cycle %d Ensure failed: %v", i, err)
	}
	if _, aerr := svc.Answer(t.Context(), &app.AnswerInteractionCommand{
		CommandID:   fmt.Sprintf("cmd-perf-%d", i),
		Principal:   string(app.KindCLI),
		Incarnation: svc.Incarnation(),
		RequestID:   ticket.RequestID,
		Outcome:     app.OutcomeAllowedOnce,
	}); aerr != nil {
		t.Fatalf("cycle %d Answer failed: %v", i, aerr)
	}
}

func runInteractionCycles(t *testing.T, svc *app.InteractionService, start, end uint64) {
	t.Helper()
	for i := start; i < end; i++ {
		runInteractionCycle(t, svc, i)
	}
}

func fastestInteractionWindow(t *testing.T, realClock clock.Clock, svc *app.InteractionService, start uint64) time.Duration {
	t.Helper()
	var fastest time.Duration
	for w := range uint64(interactionWindows) {
		from := start + w*interactionWindowCycles
		began := realClock.Now()
		runInteractionCycles(t, svc, from, from+interactionWindowCycles)
		took := realClock.Now().Sub(began)
		if w == 0 || took < fastest {
			fastest = took
		}
	}
	return fastest
}

func newPerfInteractionService(t *testing.T) *app.InteractionService {
	t.Helper()
	clk := clock.NewFake(time.Unix(100, 0))
	idGen := ids.NewUUIDv7(clk, ids.NewDeterministic("perf-interaction"))
	hub := app.NewEventHub("inc-perf")
	owner, err := interaction.NewOwner(&interaction.Config{
		Clock: clk,
		IDs:   idGen,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := app.NewInteractionService(owner, hub, clk)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestInteractionServiceCycleLatencyBounded10k holds late Ensure+Answer cycle
// latency across 10k resolved tickets to at most 10x early cycle latency,
// catching an O(n) scan over held tickets via the median of several runs.
func TestInteractionServiceCycleLatencyBounded10k(t *testing.T) {
	const measured = interactionWindows * interactionWindowCycles
	const timeBound = 10
	realClock := testutil.OSClock()
	ratios := make([]int64, 0, measurements)
	for m := range measurements {
		svc := newPerfInteractionService(t)
		first := fastestInteractionWindow(t, realClock, svc, 0)
		runInteractionCycles(t, svc, measured, interactionTotalCycles-measured)
		last := fastestInteractionWindow(t, realClock, svc, interactionTotalCycles-measured)
		ratioMilli := last.Nanoseconds() * 1000 / max(first.Nanoseconds(), 1)
		t.Logf("run %d: fastest %d-cycle window first %s, last %s, ratio %.2fx",
			m+1, interactionWindowCycles, first, last, float64(ratioMilli)/1000)
		ratios = append(ratios, ratioMilli)
	}
	if med := float64(median(ratios)) / 1000; med > timeBound {
		t.Fatalf("median late/early %d-cycle window ratio %.2fx exceeds %dx budget",
			interactionWindowCycles, med, timeBound)
	}
}
