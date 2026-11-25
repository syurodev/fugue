package cmdid

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestNewCommandIDsUniqueAcrossProcesses(t *testing.T) {
	t.Parallel()
	m1 := newWithIdentity("host-a:1001")
	m2 := newWithIdentity("host-a:1002")

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
