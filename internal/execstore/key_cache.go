package execstore

import (
	"bytes"
	"slices"
	"sync"
)

type sessionKeyCache struct {
	// prepare may span SQL/vault work; guards only take mu for in-memory work.
	prepare sync.Mutex
	mu      sync.RWMutex
	keys    map[string][]byte
	pending map[string]*SealedSessionKey
	purged  map[string]bool
	purging map[string]int
}

func newSessionKeyCache() *sessionKeyCache {
	return &sessionKeyCache{keys: make(map[string][]byte), pending: make(map[string]*SealedSessionKey), purged: make(map[string]bool), purging: make(map[string]int)}
}

func cloneSealedSessionKey(key *SealedSessionKey) *SealedSessionKey {
	if key == nil {
		return nil
	}
	return &SealedSessionKey{KeyID: key.KeyID, WrappedKey: slices.Clone(key.WrappedKey)}
}

func (c *sessionKeyCache) prepared(sessionID string) ([]byte, *SealedSessionKey, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.purged[sessionID] || c.purging[sessionID] > 0 {
		return nil, nil, false
	}
	raw, found := c.keys[sessionID]
	return slices.Clone(raw), cloneSealedSessionKey(c.pending[sessionID]), found
}

func (c *sessionKeyCache) get(sessionID string) ([]byte, bool) {
	raw, _, found := c.prepared(sessionID)
	return raw, found
}

func (c *sessionKeyCache) install(sessionID string, raw []byte, pending *SealedSessionKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.purged[sessionID] || c.purging[sessionID] > 0 {
		return ErrSessionPurging
	}
	c.keys[sessionID] = slices.Clone(raw)
	c.pending[sessionID] = cloneSealedSessionKey(pending)
	return nil
}

func (c *sessionKeyCache) committed(sessionID string, key *SealedSessionKey) {
	if key == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.pending[sessionID]
	if pending != nil && pending.KeyID == key.KeyID && bytes.Equal(pending.WrappedKey, key.WrappedKey) {
		delete(c.pending, sessionID)
	} else if pending != nil {
		// A previously prepared batch may win after a vault epoch changed. Reload
		// its durable key instead of retaining a different, uncommitted candidate.
		c.dropKey(sessionID)
	}
}

func (c *sessionKeyCache) rejectCandidate(sessionID string, key *SealedSessionKey) {
	if key == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.pending[sessionID]
	if pending != nil && bytes.Equal(pending.WrappedKey, key.WrappedKey) {
		c.dropKey(sessionID)
	}
}

func (c *sessionKeyCache) dropKey(sessionID string) {
	clear(c.keys[sessionID])
	delete(c.keys, sessionID)
	delete(c.pending, sessionID)
}

func (c *sessionKeyCache) purge(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purged[sessionID] = true
	c.dropKey(sessionID)
}

func (c *sessionKeyCache) beginPurge(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purging[sessionID]++
	c.dropKey(sessionID)
}

func (c *sessionKeyCache) endPurge(sessionID string, permanent bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purging[sessionID]--
	if c.purging[sessionID] == 0 {
		delete(c.purging, sessionID)
	}
	if permanent {
		c.purged[sessionID] = true
	}
}

func (c *sessionKeyCache) isPurged(sessionID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.purged[sessionID] || c.purging[sessionID] > 0
}

func (c *sessionKeyCache) clearAndZero() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.keys {
		c.dropKey(id)
	}
}
