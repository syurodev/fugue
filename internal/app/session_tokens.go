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
// spawn. One token is live per session; pending tokens wait until spawn
// succeeds before replacing the session's binding.
type sessionTokens struct {
	random ids.Random

	mu        sync.Mutex
	bySession map[string]SessionBinding
	pending   map[[sha256.Size]byte]SessionBinding
}

func newSessionTokens(random ids.Random) *sessionTokens {
	if random == nil {
		random = ids.CryptoRandom{}
	}
	return &sessionTokens{
		random:    random,
		bySession: map[string]SessionBinding{},
		pending:   map[[sha256.Size]byte]SessionBinding{},
	}
}

func (t *sessionTokens) mintBinding(sessionID, role string) (redact.Secret, SessionBinding, error) {
	if sessionID == "" || role == "" {
		return redact.Secret{}, SessionBinding{}, errcode.New(errcode.InvalidArgument, "a session token needs a session and a role")
	}
	raw := make([]byte, sessionTokenBytes)
	if err := t.random.Read(raw); err != nil {
		return redact.Secret{}, SessionBinding{}, errcode.Wrap(errcode.Internal, "mint the session token", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return redact.NewSecret(token), SessionBinding{SessionID: sessionID, Role: role, hash: sha256.Sum256([]byte(token))}, nil
}

// mint creates the token of one session and role and returns it for the
// spawn environment. The caller never logs, stores or formats it.
func (t *sessionTokens) mint(sessionID, role string) (redact.Secret, error) {
	secret, binding, err := t.mintBinding(sessionID, role)
	if err != nil {
		return redact.Secret{}, err
	}
	t.mu.Lock()
	t.bySession[sessionID] = binding
	t.mu.Unlock()
	return secret, nil
}

// mintPending creates a token for an in-flight spawn without overwriting any
// existing live binding for sessionID until commit is called.
func (t *sessionTokens) mintPending(sessionID, role string) (redact.Secret, SessionBinding, error) {
	secret, binding, err := t.mintBinding(sessionID, role)
	if err != nil {
		return redact.Secret{}, SessionBinding{}, err
	}
	t.mu.Lock()
	t.pending[binding.hash] = binding
	t.mu.Unlock()
	return secret, binding, nil
}

// commit promotes a pending token to be the session's active binding, unless
// the session already ended or the token was discarded.
func (t *sessionTokens) commit(binding SessionBinding) {
	t.mu.Lock()
	if _, ok := t.pending[binding.hash]; ok {
		delete(t.pending, binding.hash)
		t.bySession[binding.SessionID] = binding
	}
	t.mu.Unlock()
}

// discard revokes only the specific token minted by a failed spawn, leaving any
// other live binding for the same session untouched.
func (t *sessionTokens) discard(binding SessionBinding) {
	t.mu.Lock()
	delete(t.pending, binding.hash)
	if current, ok := t.bySession[binding.SessionID]; ok && subtle.ConstantTimeCompare(current.hash[:], binding.hash[:]) == 1 {
		delete(t.bySession, binding.SessionID)
	}
	t.mu.Unlock()
}

// verify returns the binding of a presented token. Every live and pending hash
// is compared in constant time and the loop never stops early, so timing tells
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
	for hash := range t.pending {
		binding := t.pending[hash]
		if subtle.ConstantTimeCompare(presented[:], binding.hash[:]) == 1 {
			found = binding
			matched = 1
		}
	}
	return found, matched == 1
}

// live reports whether the binding's token is still the session's token.
func (t *sessionTokens) live(binding SessionBinding) bool {
	if binding.SessionID == "" || binding.Role == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if current, ok := t.bySession[binding.SessionID]; ok && current.Role == binding.Role &&
		subtle.ConstantTimeCompare(current.hash[:], binding.hash[:]) == 1 {
		return true
	}
	pend, ok := t.pending[binding.hash]
	return ok && pend.SessionID == binding.SessionID && pend.Role == binding.Role
}

// revoke ends the session's token: on close, on purge and on actor exit.
func (t *sessionTokens) revoke(sessionID string) {
	t.mu.Lock()
	delete(t.bySession, sessionID)
	for hash, binding := range t.pending {
		if binding.SessionID == sessionID {
			delete(t.pending, hash)
		}
	}
	t.mu.Unlock()
}

// revokeAll ends every token when the host stops.
func (t *sessionTokens) revokeAll() {
	t.mu.Lock()
	clear(t.bySession)
	clear(t.pending)
	t.mu.Unlock()
}
