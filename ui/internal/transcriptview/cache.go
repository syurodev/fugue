package transcriptview

import (
	"container/list"

	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/markdown"
)

// maxCards is the layout cache's bound (desktop-app §7).
const maxCards = 2000

// cardKey identifies one card's laid-out state: an item at one revision.
type cardKey struct {
	itemID   string
	revision uint64
}

// card is one item's cached view state: its parsed markdown when it is a
// message, its expansion, and the height its last layout measured.
type card struct {
	doc      *markdown.Doc
	md       mdState
	click    widget.Clickable
	expanded bool
	// height is the card's last full height in px; it feeds the scroll math
	// until the next layout replaces it.
	height int
}

// entry is one cached card and its place in the recency order.
type entry struct {
	card *card
	self *list.Element
}

// cardCache is the layout cache: at most maxCards cards, evicting the least
// recently used.
type cardCache struct {
	max     int
	entries map[cardKey]*entry
	order   *list.List
}

// newCardCache builds a cache with the given bound.
func newCardCache(max int) *cardCache {
	return &cardCache{max: max, entries: map[cardKey]*entry{}, order: list.New()}
}

// get returns the card of the key and marks it the most recently used.
func (c *cardCache) get(k cardKey) (*card, bool) {
	e, ok := c.entries[k]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(e.self)
	return e.card, true
}

// put stores the card of the key as the most recently used and evicts the
// least recently used beyond the bound.
func (c *cardCache) put(k cardKey, card *card) {
	if e, ok := c.entries[k]; ok {
		e.card = card
		c.order.MoveToFront(e.self)
		return
	}
	el := c.order.PushFront(k)
	c.entries[k] = &entry{card: card, self: el}
	c.evict()
}

// evict drops the least recently used entries beyond the bound.
func (c *cardCache) evict() {
	for c.order.Len() > c.max {
		last := c.order.Back()
		if last == nil {
			return
		}
		c.order.Remove(last)
		delete(c.entries, last.Value.(cardKey))
	}
}

// len reports the number of cached cards.
func (c *cardCache) len() int { return len(c.entries) }
