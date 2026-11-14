package transcriptview

import (
	"fmt"
	"testing"

	"github.com/syurodev/fugue/ui/internal/session"
)

// TestLayoutCacheStaysBoundedAcrossRevisions pins the whole bound: pushing
// more than maxCards items and revisions through the cache leaves the
// entries, the remembered heights and the recency list all within maxCards —
// no card, doc or height outlives the bound of desktop-app §7.
func TestLayoutCacheStaysBoundedAcrossRevisions(t *testing.T) {
	t.Parallel()
	c := NewCache()
	// Three revisions of each of 1,200 items: 3,600 cards through a
	// 2,000-card cache.
	for rev := uint64(1); rev <= 3; rev++ {
		for i := range 1200 {
			c.cards.put(cardKey{itemID: fmt.Sprintf("i%d", i), revision: rev}, &card{height: int(rev)})
		}
	}
	if got := c.Len(); got > maxCards {
		t.Fatalf("entries = %d, want at most %d", got, maxCards)
	}
	if got := len(c.cards.heights); got > maxCards {
		t.Fatalf("remembered heights = %d, want at most %d", got, maxCards)
	}
	if got := c.cards.order.Len(); got > maxCards {
		t.Fatalf("recency list = %d, want at most %d", got, maxCards)
	}
}

// newHarnessWith builds a harness over the given shared cache.
func newHarnessWith(t *testing.T, cache *Cache, width, height int, items []*session.Item) *harness {
	t.Helper()
	h := newHarness(t, width, height, items)
	h.cache = cache
	h.st = NewState(cache)
	return h
}

// TestLayoutCacheIsSharedAcrossSessions pins that the window's one cache
// serves every session: two views lay their items into the same cache, whose
// bound covers the sessions together.
func TestLayoutCacheIsSharedAcrossSessions(t *testing.T) {
	t.Parallel()
	cache := NewCache()
	ha := newHarnessWith(t, cache, 400, 800, uniformItems(3, "a"))
	hb := newHarnessWith(t, cache, 400, 800, uniformItems(2, "b"))
	ha.st.follow = false
	hb.st.follow = false
	ha.frame()
	hb.frame()

	// Both sessions' cards live in the one cache: five distinct cards, not
	// five per session.
	if got := cache.Len(); got != 5 {
		t.Fatalf("shared cache holds %d cards, want the 5 laid-out cards", got)
	}

	// And the bound holds with both sessions' items interleaved.
	for i := range 2500 {
		cache.cards.put(cardKey{itemID: fmt.Sprintf("s%d", i), revision: 1}, &card{height: 1})
	}
	if got := cache.Len(); got > maxCards {
		t.Fatalf("shared cache grew to %d cards, want at most %d", got, maxCards)
	}
}
