package execstore

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/crypto"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/vault"
)

type lockDuringNonceRead struct{ v *vault.Service }

func (r lockDuringNonceRead) Read(p []byte) error {
	r.v.Lock()
	for i := range p {
		p[i] = byte(i + 1)
	}
	return nil
}

func TestRegressionSealMustNotSucceedAfterLock(t *testing.T) {
	t.Parallel()
	s, v, _, _ := newVaultAndStore(t)
	if _, _, err := s.SealPayload(t.Context(), "review-session", "prime", "command_input", []byte("fixture")); err != nil {
		t.Fatal(err)
	}
	s.randomReader = randomReader{r: lockDuringNonceRead{v: v}}
	_, _, err := s.SealPayload(t.Context(), "review-session", "after-lock", "command_input", []byte("fixture"))
	if !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("SealPayload returned success after nonce source completed vault.Lock: %v", err)
	}
}

type lockDuringWriteClock struct {
	clock.Clock
	v *vault.Service
}

func (c lockDuringWriteClock) Now() time.Time { c.v.Lock(); return c.Clock.Now() }

func TestRegressionProtectedAppendMustNotCommitAfterLock(t *testing.T) {
	t.Parallel()
	s, v, _, _ := newVaultAndStore(t)
	plain := []byte("fixture")
	p, k, err := s.SealPayload(t.Context(), "review-session", "review-payload", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	req := makeAcceptedReq(s, "review-session", "review-run", "review-command", p.PayloadID, plain, []SealedPayload{p}, k)
	s.clk = lockDuringWriteClock{Clock: s.clk, v: v}
	out, err := s.Append(t.Context(), req)
	if !errors.Is(err, vault.ErrLocked) || out.Committed {
		t.Fatalf("protected Append committed after vault.Lock completed during writes: committed=%v err=%v", out.Committed, err)
	}
}

func TestRegressionPayloadSizeCannotBeUnderstated(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newVaultAndStore(t)
	plain := bytes.Repeat([]byte("x"), 1024)
	p, k, err := s.SealPayload(t.Context(), "review-session", "review-payload", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	p.Size = 0
	req := makeAcceptedReq(s, "review-session", "review-run", "review-command", p.PayloadID, plain, []SealedPayload{p}, k)
	if _, appendErr := s.Append(t.Context(), req); !errors.Is(appendErr, ErrInvalidBatch) {
		t.Fatalf("forged size must reject the batch: %v", appendErr)
	}
	var count int
	if queryErr := s.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM exec_payloads").Scan(&count); queryErr != nil {
		t.Fatal(queryErr)
	}
	if count != 0 {
		t.Fatal("forged payload size committed data")
	}
}

func TestRegressionFirstKeyRollbackCanPrepareAgain(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newVaultAndStore(t)
	plain := []byte("fixture")
	p, k, err := s.SealPayload(t.Context(), "review-session", "first-payload", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	s.SetFault(FaultBeforeCommit, errSimulatedRollback)
	if _, err = s.Append(t.Context(), makeAcceptedReq(s, "review-session", "first-run", "first-command", p.PayloadID, plain, []SealedPayload{p}, k)); !errors.Is(err, errSimulatedRollback) {
		t.Fatal(err)
	}
	s.SetFault(FaultBeforeCommit, nil)
	p, k, err = s.SealPayload(t.Context(), "review-session", "second-payload", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	appendPersisted(t.Context(), t, s, makeAcceptedReq(s, "review-session", "second-run", "second-command", p.PayloadID, plain, []SealedPayload{p}, k))
}

func TestRegressionFailedPurgeKeepsSessionUsable(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newVaultAndStore(t)
	plain := []byte("fixture")
	p, k, err := s.SealPayload(t.Context(), "review-session", "review-payload", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	appendPersisted(t.Context(), t, s, makeAcceptedReq(s, "review-session", "review-run", "review-command", p.PayloadID, plain, []SealedPayload{p}, k))
	s.SetFault(FaultBeforePurgeCommit, errSimulatedPurgeCrash)
	if err := s.PurgeSession(t.Context(), "review-session"); !errors.Is(err, errSimulatedPurgeCrash) {
		t.Fatal(err)
	}
	s.SetFault(FaultBeforePurgeCommit, nil)
	if _, err := s.OpenPayload(t.Context(), "review-session", p.PayloadID); err != nil {
		t.Fatalf("rolled-back purge permanently poisoned the live session: %v", err)
	}
}

var errSecurityRandom = errors.New("injected random source failure")

type failingKeyRandom struct{ observed []byte }

func (r *failingKeyRandom) Read(p []byte) error {
	r.observed = p
	for i := range p {
		p[i] = 7
	}
	return errSecurityRandom
}

func TestSessionKeyRandomFailureIsZeroedAndRetryable(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newVaultAndStore(t)
	original := s.random
	failed := &failingKeyRandom{}
	s.random = failed
	if _, _, err := s.SealPayload(t.Context(), "random-fail", "p", "command_input", []byte("fixture")); !errors.Is(err, errSecurityRandom) {
		t.Fatal("random source error lost")
	}
	for _, b := range failed.observed {
		if b != 0 {
			t.Fatal("failed key generation retained key bytes")
		}
	}
	s.random = original
	if _, key, err := s.SealPayload(t.Context(), "random-fail", "p", "command_input", []byte("fixture")); err != nil || key == nil {
		t.Fatalf("fresh preparation after random failure: %v", err)
	}
}

func TestSessionKeyWrapRevocationDoesNotPopulateCache(t *testing.T) {
	t.Parallel()
	s, v, _, pass := newVaultAndStore(t)
	original := s.random
	s.random = lockDuringNonceRead{v: v}
	if _, _, err := s.SealPayload(t.Context(), "wrap-lock", "p", "command_input", []byte("fixture")); !errors.Is(err, vault.ErrLocked) {
		t.Fatal("key wrapping ignored lock")
	}
	if _, found := s.keyCache.get("wrap-lock"); found {
		t.Fatal("failed wrap installed a key")
	}
	s.random = original
	if err := v.Unlock(t.Context(), pass); err != nil {
		t.Fatal(err)
	}
	if _, key, err := s.SealPayload(t.Context(), "wrap-lock", "p", "command_input", []byte("fixture")); err != nil || key == nil {
		t.Fatalf("failed wrap was not retryable: %v", err)
	}
}

func TestPurgeDeleteFailureRestoresAccess(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newVaultAndStore(t)
	plain := []byte("fixture")
	p, k, err := s.SealPayload(t.Context(), "delete-fail", "p", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	appendPersisted(t.Context(), t, s, makeAcceptedReq(s, "delete-fail", "r", "c", p.PayloadID, plain, []SealedPayload{p}, k))
	if _, err = s.db.ExecContext(t.Context(), `CREATE TRIGGER fail_purge_delete BEFORE DELETE ON exec_payloads BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err = s.PurgeSession(t.Context(), "delete-fail"); err == nil {
		t.Fatal("delete failure was ignored")
	}
	if _, err = s.OpenPayload(t.Context(), "delete-fail", p.PayloadID); err != nil {
		t.Fatalf("failed delete made session unavailable: %v", err)
	}
}

func TestPayloadAccountingRejectsMalformedEnvelope(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newVaultAndStore(t)
	original, _, err := s.SealPayload(t.Context(), "accounting", "p", "command_input", []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*SealedPayload)
	}{
		{"invalid_json", func(p *SealedPayload) { p.Envelope = []byte("invalid-json") }},
		{"missing_envelope_fields", func(p *SealedPayload) { p.Envelope = []byte("{}") }},
		{"oversized_encoding", func(p *SealedPayload) { p.Envelope = make([]byte, 2*crypto.MaxCiphertextBytes+1) }},
		{"wrong_key_id", func(p *SealedPayload) { p.KeyID = "other" }},
		{"overstated_size", func(p *SealedPayload) { p.Size++ }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := original
			p.Envelope = slices.Clone(original.Envelope)
			tc.mutate(&p)
			if _, _, err := validateBatchPayloads([]SealedPayload{p}); !errors.Is(err, ErrInvalidBatch) {
				t.Fatalf("invalid envelope accepted: %v", err)
			}
		})
	}
}

func TestCanceledPurgeDoesNotPoisonSession(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newVaultAndStore(t)
	plain := []byte("fixture")
	p, k, err := s.SealPayload(t.Context(), "cancel-purge", "p", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	appendPersisted(t.Context(), t, s, makeAcceptedReq(s, "cancel-purge", "r", "c", p.PayloadID, plain, []SealedPayload{p}, k))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = s.PurgeSession(ctx, "cancel-purge"); err == nil {
		t.Fatal("canceled purge succeeded")
	}
	if _, err = s.OpenPayload(t.Context(), "cancel-purge", p.PayloadID); err != nil {
		t.Fatalf("canceled purge made session unavailable: %v", err)
	}
}

func TestPreparedKeyFromEarlierEpochWinsSafely(t *testing.T) {
	t.Parallel()
	s, v, _, pass := newVaultAndStore(t)
	plain := []byte("fixture")
	old, key, err := s.SealPayload(t.Context(), "epoch-key", "old", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	v.Lock()
	if err = v.Unlock(t.Context(), pass); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.SealPayload(t.Context(), "epoch-key", "candidate", "command_input", plain); err != nil {
		t.Fatal(err)
	}
	appendPersisted(t.Context(), t, s, makeAcceptedReq(s, "epoch-key", "old-run", "old-command", old.PayloadID, plain, []SealedPayload{old}, key))
	fresh, pending, err := s.SealPayload(t.Context(), "epoch-key", "fresh", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	if pending != nil {
		t.Fatal("committed winner did not replace stale candidate")
	}
	appendPersisted(t.Context(), t, s, makeAcceptedReq(s, "epoch-key", "fresh-run", "fresh-command", fresh.PayloadID, plain, []SealedPayload{fresh}, pending))
	if _, err = s.OpenPayload(t.Context(), "epoch-key", fresh.PayloadID); err != nil {
		t.Fatalf("fresh payload sealed with wrong key: %v", err)
	}
}

func TestConflictingKeyCandidateCanReprepare(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newVaultAndStore(t)
	other, err := New(t.Context(), s.db, s.vault, s.host, s.clk, s.ids, s.random, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeStore(t, other) })
	plain := []byte("fixture")
	a, ka, err := s.SealPayload(t.Context(), "key-race", "a", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	b, kb, err := other.SealPayload(t.Context(), "key-race", "b", "command_input", plain)
	if err != nil {
		t.Fatal(err)
	}
	appendPersisted(t.Context(), t, s, makeAcceptedReq(s, "key-race", "a-run", "a-command", a.PayloadID, plain, []SealedPayload{a}, ka))
	if _, err = other.Append(t.Context(), makeAcceptedReq(other, "key-race", "b-run", "b-command", b.PayloadID, plain, []SealedPayload{b}, kb)); !errors.Is(err, ErrSessionKeyConflict) {
		t.Fatalf("expected candidate conflict: %v", err)
	}
	fresh, pending, err := other.SealPayload(t.Context(), "key-race", "retry", "command_input", plain)
	if err != nil || pending != nil {
		t.Fatalf("could not reload committed key: %v", err)
	}
	appendPersisted(t.Context(), t, other, makeAcceptedReq(other, "key-race", "retry-run", "retry-command", fresh.PayloadID, plain, []SealedPayload{fresh}, pending))
	if _, err = s.OpenPayload(t.Context(), "key-race", fresh.PayloadID); err != nil {
		t.Fatalf("retry did not use the durable key: %v", err)
	}
}
