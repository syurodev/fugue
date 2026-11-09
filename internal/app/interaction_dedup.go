package app

import (
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

const (
	defaultDedupCapacity = 1024
	defaultDedupTTL      = 10 * time.Minute
)

type dedupRecord struct {
	commandID string
	result    AnswerInteractionResult
	cachedAt  time.Time
}

type commandDedupCache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	clock    clock.Clock
	entries  map[string]*dedupRecord
	records  []*dedupRecord
}

func newCommandDedupCache(clk clock.Clock, capacity int, ttl time.Duration) *commandDedupCache {
	if capacity <= 0 {
		capacity = defaultDedupCapacity
	}
	if ttl <= 0 {
		ttl = defaultDedupTTL
	}
	return &commandDedupCache{
		capacity: capacity,
		ttl:      ttl,
		clock:    clk,
		entries:  map[string]*dedupRecord{},
	}
}

func (c *commandDedupCache) Get(commandID string) (AnswerInteractionResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now().UTC()
	c.evictExpiredLocked(now)

	rec, ok := c.entries[commandID]
	if !ok {
		return AnswerInteractionResult{}, false
	}
	return rec.result, true
}

func (c *commandDedupCache) Put(commandID string, result AnswerInteractionResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.clock.Now().UTC()
	c.evictExpiredLocked(now)

	rec := &dedupRecord{
		commandID: commandID,
		result:    result,
		cachedAt:  now,
	}

	if _, exists := c.entries[commandID]; !exists {
		for len(c.entries) >= c.capacity && len(c.records) > 0 {
			oldest := c.records[0]
			c.records = c.records[1:]
			if current, ok := c.entries[oldest.commandID]; ok && current == oldest {
				delete(c.entries, oldest.commandID)
			}
		}
	}

	c.entries[commandID] = rec
	c.records = append(c.records, rec)
}

func (c *commandDedupCache) evictExpiredLocked(now time.Time) {
	cutoff := now.Add(-c.ttl)
	validIdx := len(c.records)
	for i, r := range c.records {
		if !r.cachedAt.Before(cutoff) {
			validIdx = i
			break
		}
		if current, ok := c.entries[r.commandID]; ok && current == r {
			delete(c.entries, r.commandID)
		}
	}
	if validIdx < len(c.records) {
		c.records = c.records[validIdx:]
	} else {
		c.records = nil
	}
}

type inflightCmd struct {
	done   chan struct{}
	result AnswerInteractionResult
	err    error
}
