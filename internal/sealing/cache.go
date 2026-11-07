package sealing

import (
	"bytes"
	"slices"
	"sync"
)

// SessionKey is a wrapped session key ready for persistence.
type SessionKey struct {
	KeyID      string
	WrappedKey []byte
}

// KeyCache manages in-memory keys and purge barriers.
type KeyCache struct {
	// prepare may span SQL/vault work; guards only take mu for in-memory work.
	Prepare sync.Mutex
	mu      sync.RWMutex
	keys    map[string][]byte
	pending map[string]*SessionKey
	purged  map[string]bool
	purging map[string]int
}

// NewKeyCache creates an empty key cache.
func NewKeyCache() *KeyCache {
	return &KeyCache{keys: make(map[string][]byte), pending: make(map[string]*SessionKey), purged: make(map[string]bool), purging: make(map[string]int)}
}

func cloneSessionKey(key *SessionKey) *SessionKey {
	if key == nil {
		return nil
	}
	return &SessionKey{KeyID: key.KeyID, WrappedKey: slices.Clone(key.WrappedKey)}
}

// Prepared returns copies of a cached key and its pending wrapper.
func (c *KeyCache) Prepared(sessionID string) ([]byte, *SessionKey, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.purged[sessionID] || c.purging[sessionID] > 0 {
		return nil, nil, false
	}
	raw, found := c.keys[sessionID]
	return slices.Clone(raw), cloneSessionKey(c.pending[sessionID]), found
}

// Get returns a copy of a cached key.
func (c *KeyCache) Get(sessionID string) ([]byte, bool) {
	raw, _, found := c.Prepared(sessionID)
	return raw, found
}

// Install caches a key unless its session is being purged.
func (c *KeyCache) Install(sessionID string, raw []byte, pending *SessionKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.purged[sessionID] || c.purging[sessionID] > 0 {
		return false
	}
	c.keys[sessionID] = slices.Clone(raw)
	c.pending[sessionID] = cloneSessionKey(pending)
	return true
}

// Committed reconciles a persisted key with the cached candidate.
func (c *KeyCache) Committed(sessionID string, key *SessionKey) {
	if key == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.pending[sessionID]
	if pending != nil && pending.KeyID == key.KeyID && bytes.Equal(pending.WrappedKey, key.WrappedKey) {
		delete(c.pending, sessionID)
	} else if pending != nil {
		// A previously Prepared batch may win after a vault epoch changed. Reload
		// its durable key instead of retaining a different, uncommitted candidate.
		c.dropKey(sessionID)
	}
}

// RejectCandidate discards a rejected pending key.
func (c *KeyCache) RejectCandidate(sessionID string, key *SessionKey) {
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

func (c *KeyCache) dropKey(sessionID string) {
	clear(c.keys[sessionID])
	delete(c.keys, sessionID)
	delete(c.pending, sessionID)
}

// Purge permanently revokes a session key.
func (c *KeyCache) Purge(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purged[sessionID] = true
	c.dropKey(sessionID)
}

// BeginPurge blocks access and discards the cached key.
func (c *KeyCache) BeginPurge(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purging[sessionID]++
	c.dropKey(sessionID)
}

// EndPurge completes a purge barrier.
func (c *KeyCache) EndPurge(sessionID string, permanent bool) {
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

// IsPurged reports whether access to the session is blocked.
func (c *KeyCache) IsPurged(sessionID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.purged[sessionID] || c.purging[sessionID] > 0
}

// ClearAndZero erases cached key material.
func (c *KeyCache) ClearAndZero() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.keys {
		c.dropKey(id)
	}
}
