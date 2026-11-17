//go:build perf

package app

import "testing"

// heavyPagedItems is the TR-E05 item count.
const heavyPagedItems = 10000

// TR-E05 with its full item count. The PR run pages over pagedItems items under
// -race; the nightly perf job runs this without the race detector.
func TestTranscriptPageOver10000ItemsIsStableWithoutDuplicatesOrGaps(t *testing.T) {
	t.Parallel()
	assertStablePagingOver(t, heavyPagedItems, heavyPagedItems+2)
}
