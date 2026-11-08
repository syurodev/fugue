package sealing

import (
	"bytes"
	"testing"
)

func TestCacheCopiesKeysAndShredsOnPurge(t *testing.T) {
	t.Parallel()
	c := NewKeyCache()
	raw := []byte("secret")
	if !c.Install("s", raw, &SessionKey{KeyID: "v1", WrappedKey: []byte("wrapped")}) {
		t.Fatal("install failed")
	}
	raw[0] = 'X'
	got, pending, ok := c.Prepared("s")
	if !ok || !bytes.Equal(got, []byte("secret")) || string(pending.WrappedKey) != "wrapped" {
		t.Fatal("cache did not preserve key")
	}
	c.BeginPurge("s")
	if _, _, ok := c.Prepared("s"); ok {
		t.Fatal("purging key remained readable")
	}
	c.EndPurge("s", true)
	if c.Install("s", raw, nil) {
		t.Fatal("purged key reinstalled")
	}
}

func TestCacheReconcilesCandidatesAndLock(t *testing.T) {
	t.Parallel()
	c := NewKeyCache()
	first := &SessionKey{KeyID: "v1", WrappedKey: []byte("first")}
	second := &SessionKey{KeyID: "v1", WrappedKey: []byte("second")}
	c.Install("s", []byte("secret"), first)
	c.Committed("s", first)
	_, pending, ok := c.Prepared("s")
	if !ok || pending != nil {
		t.Fatal("commit did not clear candidate")
	}
	c.Install("s", []byte("secret"), second)
	c.Committed("s", first)
	if _, ok := c.Get("s"); ok {
		t.Fatal("conflicting commit retained candidate")
	}
	c.Install("s", []byte("secret"), first)
	c.RejectCandidate("s", first)
	if _, ok := c.Get("s"); ok {
		t.Fatal("rejected candidate retained")
	}
	c.Install("s", []byte("secret"), nil)
	c.ClearAndZero()
	if _, ok := c.Get("s"); ok {
		t.Fatal("lock retained key")
	}
	c.BeginPurge("s")
	c.EndPurge("s", false)
	if c.IsPurged("s") || !c.Install("s", []byte("secret"), nil) {
		t.Fatal("rolled back purge remained blocked")
	}
	c.Purge("s")
	if !c.IsPurged("s") {
		t.Fatal("purge did not block session")
	}
}
