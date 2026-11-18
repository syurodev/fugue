package transcriptview

import (
	"testing"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
)

// maxCacheEntries is the shared cache's bound (desktop-app §7): heights,
// payloads and the recency order all live within it.
const maxCacheEntries = 2000

// TestLayoutCacheStaysBoundedAcrossRevisions pins the whole bound: pushing
// more than the bound's worth of items and revisions through the shared
// cache leaves the entries within it — no height, doc or payload outlives
// the bound of desktop-app §7 (F10.4a round 3, now over the kit's cache).
func TestLayoutCacheStaysBoundedAcrossRevisions(t *testing.T) {
	t.Parallel()
	// Three revisions of each of 1,200 items: 3,600 cards through a
	// 2,000-entry cache. Only the laid-out rows enter the cache, so the
	// pane scrolls across the whole history once per revision.
	const items = 1200
	h := newHarness(t, 600, 800, uniformItems(items, "i"))
	h.st.FollowOff()
	h.frame()
	scrollAcross(t, h, items)
	for rev := uint64(2); rev <= 3; rev++ {
		for _, item := range h.items {
			item.Revision = rev
		}
		scrollAcross(t, h, items)
	}
	if got := h.cache.Len(); got > maxCacheEntries {
		t.Fatalf("entries = %d, want at most %d", got, maxCacheEntries)
	}
}

// scrollAcross walks the reader over the whole history one step at a time,
// so every item is laid out once and enters the cache.
func scrollAcross(t *testing.T, h *harness, items int) {
	t.Helper()
	const step = 400
	for offset := 0; offset <= items*estimateH; offset += step {
		h.st.ScrollTo(offset)
		h.frame()
	}
}

// newHarnessWith builds a harness over the given shared cache.
func newHarnessWith(t *testing.T, cache *kit.VirtualHeightCache, width, height int, items []*session.Item) *harness {
	t.Helper()
	h := newHarness(t, width, height, items)
	h.cache = cache
	h.st = NewState(cache)
	return h
}

// TestLayoutCacheIsSharedAcrossSessions pins that the window's one cache
// serves every session: two views lay their items into the same cache, whose
// bound covers the sessions together (F10.4a round 3, now over the kit's
// cache).
func TestLayoutCacheIsSharedAcrossSessions(t *testing.T) {
	t.Parallel()
	cache := kit.NewVirtualHeightCache()
	ha := newHarnessWith(t, cache, 400, 800, uniformItems(3, "a"))
	hb := newHarnessWith(t, cache, 400, 800, uniformItems(2, "b"))
	ha.st.FollowOff()
	hb.st.FollowOff()
	ha.frame()
	hb.frame()

	// Both sessions' cards live in the one cache: five distinct cards, not
	// five per session.
	if got := cache.Len(); got != 5 {
		t.Fatalf("shared cache holds %d cards, want the 5 laid-out cards", got)
	}

	// And the bound holds with both sessions' items interleaved: one
	// session scrolling through 1,800 more items, three revisions' worth of
	// pushes through the shared bound.
	hc := newHarnessWith(t, cache, 400, 800, uniformItems(600, "c"))
	hc.st.FollowOff()
	hc.frame()
	for rev := uint64(2); rev <= 3; rev++ {
		for _, item := range hc.items {
			item.Revision = rev
		}
		scrollAcross(t, hc, 600)
	}
	if got := cache.Len(); got > maxCacheEntries {
		t.Fatalf("shared cache grew to %d entries, want at most %d", got, maxCacheEntries)
	}
}
