package kit

import "container/list"

// The height cache of one VirtualList (§6.26): a bounded LRU keyed by
// (key, revision) that also remembers each key's latest measured height, so
// a new revision seeds from the old one, and the callers' per-key payloads.

type virtualHeightKey struct {
	key      string
	revision uint64
}

type virtualLatestHeight struct {
	revision uint64
	height   int
}

type virtualHeightEntry struct {
	key    virtualHeightKey
	height int
	self   *list.Element
}

type virtualHeightCache struct {
	max     int
	entries map[virtualHeightKey]*virtualHeightEntry
	heights map[string]virtualLatestHeight
	// payloads are the callers' per-key view state (a parsed document, a
	// pressed state): heavy objects the row func needs across frames, kept
	// within the list's bound by dropping each one when its key's latest
	// height leaves the cache.
	payloads map[string]any
	order    *list.List
}

func newVirtualHeightCache(maxEntries int) *virtualHeightCache {
	return &virtualHeightCache{
		max:      maxEntries,
		entries:  map[virtualHeightKey]*virtualHeightEntry{},
		heights:  map[string]virtualLatestHeight{},
		payloads: map[string]any{},
		order:    list.New(),
	}
}

// payload returns the key's caller payload, building it with build when the
// cache holds none. The payload lives as long as the key's latest height:
// when the LRU evicts the key's newest revision, the payload goes with it,
// so the caller's view state stays bounded with the heights (§6.26).
func (c *virtualHeightCache) payload(key string, build func() any) any {
	if p, ok := c.payloads[key]; ok {
		return p
	}
	p := build()
	c.payloads[key] = p
	return p
}

func (c *virtualHeightCache) get(k virtualHeightKey) (int, bool) {
	e, ok := c.entries[k]
	if !ok {
		return 0, false
	}
	c.order.MoveToFront(e.self)
	return e.height, true
}

func (c *virtualHeightCache) latest(key string) (int, bool) {
	h, ok := c.heights[key]
	return h.height, ok
}

func (c *virtualHeightCache) put(k virtualHeightKey, height int) {
	c.heights[k.key] = virtualLatestHeight{revision: k.revision, height: height}
	if e, ok := c.entries[k]; ok {
		e.height = height
		c.order.MoveToFront(e.self)
		return
	}
	el := c.order.PushFront(k)
	c.entries[k] = &virtualHeightEntry{key: k, height: height, self: el}
	c.evict()
}

func (c *virtualHeightCache) evict() {
	for c.order.Len() > c.max {
		last := c.order.Back()
		if last == nil {
			return
		}
		c.order.Remove(last)
		k, ok := last.Value.(virtualHeightKey)
		if !ok {
			return
		}
		delete(c.entries, k)
		if h, held := c.heights[k.key]; held && h.revision == k.revision {
			delete(c.heights, k.key)
			// The key holds no measured height anymore, so the caller's
			// payload has nothing left to stay alive for.
			delete(c.payloads, k.key)
		}
	}
}
