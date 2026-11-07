package secrets

import (
	"runtime"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/redact"
)

func sparseText() string {
	prefix := "known-value-123\nAPI_KEY=abcd1234\n" + patternFixtures()[KindGitHubToken][0] + "\n"
	return prefix + strings.Repeat("ordinary text with no assignment\n", (1<<20)/32+1)[:(1<<20)-len(prefix)]
}

func TestFindSparseAllocationBound(t *testing.T) {
	d := New([]KnownValue{{Value: redact.NewSecret("known-value-123"), Kind: KindVault}}, Options{})
	text := sparseText()
	// Measure before joining the parallel phase: other tests are paused at
	// t.Parallel, so their allocations cannot contaminate TotalAlloc/Mallocs.
	allocations := testing.AllocsPerRun(3, func() { d.Find(text) })
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := d.Find(text)
	runtime.ReadMemStats(&after)
	allocatedBytes := after.TotalAlloc - before.TotalAlloc
	t.Parallel()
	t.Logf("1 MiB sparse scan: %d bytes, %.0f allocations", allocatedBytes, allocations)
	// Two bytes per input byte leaves headroom for regexp bookkeeping while
	// rejecting any return to input-sized Finding or interval-index arrays.
	if allocatedBytes > 2*uint64(len(text)) || allocations > 256 {
		t.Fatal("sparse allocation bound exceeded")
	}
	if len(got) != 3 {
		t.Fatal("sparse fixture findings missing")
	}
}
