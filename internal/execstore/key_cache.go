package execstore

import "github.com/syurodev/fugue/internal/sealing"

type sessionKeyCache struct{ *sealing.KeyCache }

func newSessionKeyCache() *sessionKeyCache              { return &sessionKeyCache{sealing.NewKeyCache()} }
func (c *sessionKeyCache) get(id string) ([]byte, bool) { return c.Get(id) }
func (c *sessionKeyCache) purge(id string)              { c.Purge(id) }
