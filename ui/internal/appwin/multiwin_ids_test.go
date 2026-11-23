package appwin

import (
	"context"
	"sync"
	"testing"

	"github.com/syurodev/fugue/pkg/client"
)

func TestMultiWindowIDsDifferAcrossLaunches(t *testing.T) {
	t.Parallel()
	a, b := newMultiWindow(nil, nil), newMultiWindow(nil, nil)
	x, y := a.nextWindowID(), b.nextWindowID()
	if x == y {
		t.Fatalf("two launches produced the same window id %q", x)
	}
	if len(x) != 36 || x[14] != '4' || x == "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("window id %q is not a random uuid v4", x)
	}
}

func TestMultiWindowIDNeverEqualsRestoredWindow(t *testing.T) {
	t.Parallel()
	const restored = "11111111-1111-4111-8111-111111111111"
	coord := newMultiWindow(nil, nil)
	coord.known[restored] = true
	calls := 0
	coord.newID = func() string {
		calls++
		if calls == 1 {
			return restored
		}
		return "22222222-2222-4222-8222-222222222222"
	}
	if got := coord.nextWindowID(); got == restored || calls != 2 {
		t.Fatalf("id = %q after %d calls, want a regenerated id", got, calls)
	}
}

type cmdRecordingClient struct {
	mu  sync.Mutex
	ids []string
}

func (c *cmdRecordingClient) PutAppStateWindow(_ context.Context, in *client.PutAppStateWindowInput) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, in.CommandID)
	return 1, nil
}

func (c *cmdRecordingClient) DeleteAppStateWindow(context.Context, string, string, uint64) error {
	return nil
}

func TestLayoutWorkerCommandIDsDifferAcrossWorkers(t *testing.T) {
	t.Parallel()
	rec := &cmdRecordingClient{}
	for range 2 {
		w := newLayoutWorker(func() layoutClient { return rec }, nil)
		res := w.handle(t.Context(), &layoutRequest{kind: opPutWindow, windowID: "w", cmdSeq: 1, doc: DefaultLayout()})
		if res.err != nil {
			t.Fatal(res.err)
		}
	}
	if len(rec.ids) != 2 || rec.ids[0] == rec.ids[1] {
		t.Fatalf("command ids = %v, want two distinct ids", rec.ids)
	}
}
