package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
)

// PrincipalSession is the principal of a connection that presented a session
// token (host-api §7). It is never a local principal.
const PrincipalSession = "session"

// sessionTokenBytes is the token's entropy: 256 bits.
const sessionTokenBytes = 32

// SessionBinding is what a session token opens: the Fugue MCP tools of one
// vendor session in one role, and nothing else (HA-06). The hash ties the
// binding to the token it came from, so a revoked or replaced token ends
// every connection that attached with it.
type SessionBinding struct {
	SessionID string
	Role      string
	hash      [sha256.Size]byte
}

// sessionTokens holds the live session tokens, by hash only: the clear token
// exists in the vendor process's environment and nowhere in the host after
// spawn. One token is live per session; minting again replaces it.
type sessionTokens struct {
	random ids.Random

	mu        sync.Mutex
	bySession map[string]SessionBinding
}

func newSessionTokens(random ids.Random) *sessionTokens {
	if random == nil {
		random = ids.CryptoRandom{}
	}
	return &sessionTokens{random: random, bySession: map[string]SessionBinding{}}
}

// mint creates the token of one session and role and returns it for the
// spawn environment. The caller never logs, stores or formats it.
func (t *sessionTokens) mint(sessionID, role string) (redact.Secret, error) {
	if sessionID == "" || role == "" {
		return redact.Secret{}, errcode.New(errcode.InvalidArgument, "a session token needs a session and a role")
	}
	raw := make([]byte, sessionTokenBytes)
	if err := t.random.Read(raw); err != nil {
		return redact.Secret{}, errcode.Wrap(errcode.Internal, "mint the session token", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	t.mu.Lock()
	t.bySession[sessionID] = SessionBinding{SessionID: sessionID, Role: role, hash: sha256.Sum256([]byte(token))}
	t.mu.Unlock()
	return redact.NewSecret(token), nil
}

// verify returns the binding of a presented token. Every live hash is
// compared in constant time and the loop never stops early, so timing tells
// a caller nothing about a near match.
func (t *sessionTokens) verify(token string) (SessionBinding, bool) {
	presented := sha256.Sum256([]byte(token))
	t.mu.Lock()
	defer t.mu.Unlock()
	var found SessionBinding
	matched := 0
	for id := range t.bySession {
		binding := t.bySession[id]
		if subtle.ConstantTimeCompare(presented[:], binding.hash[:]) == 1 {
			found = binding
			matched = 1
		}
	}
	return found, matched == 1
}

// live reports whether the binding's token is still the session's token.
func (t *sessionTokens) live(binding SessionBinding) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	current, ok := t.bySession[binding.SessionID]
	return ok && current.Role == binding.Role && subtle.ConstantTimeCompare(current.hash[:], binding.hash[:]) == 1
}

// revoke ends the session's token: on close, on purge and on a failed spawn.
func (t *sessionTokens) revoke(sessionID string) {
	t.mu.Lock()
	delete(t.bySession, sessionID)
	t.mu.Unlock()
}

// revokeAll ends every token when the host stops.
func (t *sessionTokens) revokeAll() {
	t.mu.Lock()
	clear(t.bySession)
	t.mu.Unlock()
}
