package hostapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/syurodev/fugue/internal/platform/supervise"
)

// TestRequestAfterDrainStartIsRejected pins the admission gate: once the
// drain started (drained set under the server mutex), a request is answered
// with 503 and is not registered in-flight, so it cannot race
// inflight.Wait. A request admitted before the drain is counted as usual.
func TestRequestAfterDrainStartIsRejected(t *testing.T) {
	t.Parallel()
	srv := NewServer(ServerConfig{
		DataDir: shortDir(t),
	})

	// An admitted request finishes normally and the counter returns to zero.
	rec := httptest.NewRecorder()
	if !srv.admit(rec) {
		t.Fatal("a request before the drain start was rejected")
	}
	srv.inflight.Done()

	srv.mu.Lock()
	srv.drained = true
	srv.mu.Unlock()

	// A request after the drain start is rejected with 503 and must never be
	// registered in-flight: the drain's inflight.Wait runs while the counter
	// is zero, so a buggy rejection that counted the request would hang here
	// — the channel is the guard, there is no sleep.
	waited := make(chan struct{})
	supervise.Go(t.Context(), "test.wait", func(context.Context) error {
		srv.inflight.Wait()
		close(waited)
		return nil
	})

	rec = httptest.NewRecorder()
	if srv.admit(rec) {
		t.Fatal("a request after the drain start was admitted")
	}
	if rec.Code != 503 {
		t.Fatalf("rejected request status = %d, want 503", rec.Code)
	}
	// Wait must return with the counter at zero; a rejection that counted the
	// request would block here and fail the package timeout instead.
	<-waited

	// Admission resumes when the drain state is lifted.
	srv.mu.Lock()
	srv.drained = false
	srv.mu.Unlock()
	rec = httptest.NewRecorder()
	if !srv.admit(rec) {
		t.Fatal("admission did not resume when the drain ended")
	}
	srv.inflight.Done()
}
