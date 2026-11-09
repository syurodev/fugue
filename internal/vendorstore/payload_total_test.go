package vendorstore

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/sealing"
)

func TestConcurrentPayloadAppendsRespectSessionLimit(t *testing.T) {
	t.Parallel()
	s, v, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, Payload: []byte("a")})
	requireNoError(t, err)
	_, err = s.db.ExecContext(t.Context(), `INSERT INTO vendor_payloads(payload_id,session_id,kind,key_id,size,envelope) SELECT 'seed',session_id,kind,key_id,?,envelope FROM vendor_payloads LIMIT 1`, sealing.MaxSessionPayloadTotal-2)
	requireNoError(t, err)
	_, err = s.db.ExecContext(t.Context(), `UPDATE vendor_sessions SET payload_bytes=? WHERE session_id='s'`, sealing.MaxSessionPayloadTotal-1)
	requireNoError(t, err)
	second, err := New(t.Context(), s.db, v, s.clock, nil)
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, second.Close()) })
	start := make(chan struct{})
	results := make(chan error, 2)
	tasks := make([]*supervise.Task, 0, 2)
	for _, store := range []*Store{s, second} {
		tasks = append(tasks, supervise.Go(t.Context(), "payload-append", func(ctx context.Context) error {
			select {
			case <-start:
			case <-ctx.Done():
				return ctx.Err()
			}
			results <- appendAtCurrentSequence(ctx, store)
			return nil
		}))
	}
	close(start)
	for _, task := range tasks {
		requireNoError(t, task.Wait())
	}
	success, limited := 0, 0
	for range 2 {
		switch e := <-results; {
		case e == nil:
			success++
		case errcode.CodeOf(e) == errcode.FailedPrecondition:
			limited++
		default:
			t.Fatal(e)
		}
	}
	if success != 1 || limited != 1 {
		t.Fatalf("success=%d limited=%d", success, limited)
	}
	assertPayloadTotal(t, s)
}

func appendAtCurrentSequence(ctx context.Context, store *Store) error {
	for {
		snap, err := store.Snapshot(ctx, "s")
		if err != nil {
			return err
		}
		_, err = store.Append(ctx, "s", snap.LastSequence, &Event{Type: UsageObserved, Payload: []byte("x")})
		if !errors.Is(err, ErrSequenceConflict) {
			return err
		}
	}
}

func TestPayloadTotalSurvivesRecovery(t *testing.T) {
	t.Parallel()
	s, v, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, Payload: []byte("data")})
	requireNoError(t, err)
	assertPayloadTotal(t, s)
	reopened, err := New(t.Context(), s.db, v, s.clock, nil)
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, reopened.Close()) })
	_, err = reopened.Recover(t.Context())
	requireNoError(t, err)
	assertPayloadTotal(t, reopened)
}

func TestAppend10000PayloadsWithinBudget(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	for i := int64(1); i <= 10000; i++ {
		_, err := s.Append(ctx, "s", i, &Event{Type: UsageObserved, Payload: []byte("x")})
		requireNoError(t, err)
	}

	assertPayloadTotal(t, s)
}

func BenchmarkAppendPayloads(b *testing.B) {
	for _, count := range []int{10000, 50000} {
		b.Run(strconv.Itoa(count), func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				s, _, _ := protectedStore(b)
				b.StartTimer()
				for i := 1; i <= count; i++ {
					_, err := s.Append(b.Context(), "s", int64(i), &Event{Type: UsageObserved, Payload: []byte("x")})
					requireNoError(b, err)
				}
			}
		})
	}
}
