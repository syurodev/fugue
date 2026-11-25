package cmdid

import (
	"sync"
	"sync/atomic"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestNewCommandIDsUniqueAcrossProcesses(t *testing.T) {
	t.Parallel()
	var seq1, seq2 atomic.Uint64
	m1 := newWithIdentity("host-a:1001", &seq1)
	m2 := newWithIdentity("host-a:1002", &seq2)

	seen := make(map[string]string, 200)
	for range 100 {
		id1 := m1()
		if len(id1) != 32 {
			t.Fatalf("len(id1) = %d, want 32", len(id1))
		}
		if prev, dup := seen[id1]; dup {
			t.Fatalf("duplicate command id %q from m1 (previously %s)", id1, prev)
		}
		seen[id1] = "m1"

		id2 := m2()
		if len(id2) != 32 {
			t.Fatalf("len(id2) = %d, want 32", len(id2))
		}
		if prev, dup := seen[id2]; dup {
			t.Fatalf("duplicate command id %q from m2 (previously %s)", id2, prev)
		}
		seen[id2] = "m2"
	}
}

func TestNewCommandIDsUniqueWithinSameProcessParallel(t *testing.T) {
	t.Parallel()
	m1 := New()
	m2 := New()

	const perMinter = 100
	results := make(chan string, perMinter*2)
	var wg sync.WaitGroup
	for _, minter := range []func() string{m1, m2} {
		wg.Add(1)
		go func(fn func() string) {
			defer wg.Done()
			for range perMinter {
				results <- fn()
			}
		}(minter)
	}
	wg.Wait()
	close(results)

	seen := make(map[string]struct{}, perMinter*2)
	for id := range results {
		if len(id) != 32 {
			t.Fatalf("len(id) = %d, want 32", len(id))
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate command id %q from concurrent New() minters", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != perMinter*2 {
		t.Fatalf("unique ids = %d, want %d", len(seen), perMinter*2)
	}
}
