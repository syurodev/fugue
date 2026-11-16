package gitops

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// TestFakeBinariesWrittenAndExecedInParallelNeverHitTextBusy proves
// writeScript is safe while other goroutines fork: without the fork lock a
// child inherits another test's open write descriptor and exec fails with
// ETXTBSY ("text file busy") on Linux.
func TestFakeBinariesWrittenAndExecedInParallelNeverHitTextBusy(t *testing.T) {
	t.Parallel()
	const binaries = 96
	for i := range binaries {
		t.Run(fmt.Sprintf("binary-%d", i), func(t *testing.T) {
			t.Parallel()
			want := strconv.Itoa(i)
			m, _, _ := fakeGitManager(t, "echo "+want)
			res, err := m.exec(t.Context(), t.TempDir(), "status")
			if err != nil {
				t.Fatalf("exec fake binary: %v", err)
			}
			if got := strings.TrimSpace(res.Stdout); got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
		})
	}
}
