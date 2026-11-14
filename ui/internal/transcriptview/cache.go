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

// card is one item revision's cached view state: its parsed markdown when it
// is a message, and the height its last layout measured. The expansion lives
// per item_id in the State, so it survives a new revision.
type card struct {
	doc    *markdown.Doc
	docSrc string
	md     mdState
	click  widget.Clickable
	// height is the card's last full height in px; it feeds the scroll math
	// until the next layout replaces it. A new revision's entry is seeded
	// from the previous revision's height so the list does not jump.
	height int
}

// latestHeight is the remembered height of an item's newest cached revision;
// a new revision seeds from it, and it is dropped when that revision is
// evicted, so the memory stays within the cache bound.
type latestHeight struct {
	revision uint64
	height   int
}

// entry is one cached card and its place in the recency order.
type entry struct {
	card *card
	self *list.Element
}

// Cache is a window's shared layout cache, bounded at maxCards entries
// (desktop-app §7). item_id is globally unique, so every session's view
// shares one cache keyed by item and revision.
type Cache struct {
	cards cardCache
}

// NewCache builds the shared cache.
func NewCache() *Cache { return &Cache{cards: *newCardCache(maxCards)} }

// Len reports the number of cached cards.
func (c *Cache) Len() int { return c.cards.len() }

// cardCache is the layout cache: at most maxCards cards, evicting the least
// recently used together with the item heights remembered from them, so the
// bound of desktop-app §7 holds for the heights too.
type cardCache struct {
	max     int
	entries map[cardKey]*entry
	// heights remembers each item's newest cached revision's height, so a
	// new revision can seed its height from the old one.
	heights map[string]latestHeight
	order   *list.List
}

// newCardCache builds a cache with the given bound.
func newCardCache(max int) *cardCache {
	return &cardCache{max: max, entries: map[cardKey]*entry{}, heights: map[string]latestHeight{}, order: list.New()}
}

// recordHeight remembers the item's measured height for its revision, after
// a layout measured it.
func (c *cardCache) recordHeight(itemID string, revision uint64, height int) {
	c.heights[itemID] = latestHeight{revision: revision, height: height}
}

// heightOf returns the item's remembered height, or false when the cache
// holds none.
func (c *cardCache) heightOf(itemID string) (int, bool) {
	h, ok := c.heights[itemID]
	return h.height, ok
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
		key, ok := last.Value.(cardKey)
		if !ok {
			return
		}
		delete(c.entries, key)
		// The remembered height goes with the card: keep it only while the
		// evicted revision is still the item's newest cached one.
		if h, held := c.heights[key.itemID]; held && h.revision == key.revision {
			delete(c.heights, key.itemID)
		}
	}
}

// len reports the number of cached cards.
func (c *cardCache) len() int { return len(c.entries) }
