package execstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vault"
)

func TestVaultLockedLifecycleAndCacheZeroing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, v, _, masterPass := newVaultAndStore(t)

	sessionID := "sess-vault-cycle"
	payloadID := "pl-cycle-1"
	plaintext := []byte("secret-vault-lifecycle-data")

	// 1. Initial state: unlocked
	sealed, key, err := s.SealPayload(ctx, sessionID, payloadID, "tool_result", plaintext)
	if err != nil {
		t.Fatalf("SealPayload failed: %v", err)
	}

	req := makeAcceptedReq(s, sessionID, "run-vc-1", "cmd-vc-1", payloadID, plaintext, []SealedPayload{sealed}, key)

	if _, appendErr := s.Append(ctx, req); appendErr != nil {
		t.Fatalf("Append failed: %v", appendErr)
	}

	// Read while unlocked succeeds
	opened, err := s.OpenPayload(ctx, sessionID, payloadID)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("OpenPayload failed while unlocked: %v", err)
	}

	// 2. Lock vault: synchronous guard must zero and clear cache
	v.Lock()

	// While locked, SealPayload must fail closed
	_, _, err = s.SealPayload(ctx, sessionID, "pl-2", "tool_result", []byte("locked-data"))
	if !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("expected vault.ErrLocked from SealPayload, got %v", err)
	}

	// While locked, OpenPayload must fail closed
	_, err = s.OpenPayload(ctx, sessionID, payloadID)
	if !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("expected vault.ErrLocked from OpenPayload, got %v", err)
	}

	// While locked, protected Append must fail closed
	protReq := makeAcceptedReq(s, sessionID, "run-vc-1", "cmd-vc-locked", payloadID, plaintext, nil, nil)
	protReq.ExpectedRunRevision = 1

	_, err = s.Append(ctx, protReq)
	if !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("expected vault.ErrLocked from Append, got %v", err)
	}

	// 3. Unlock vault again
	if unlockErr := v.Unlock(ctx, masterPass); unlockErr != nil {
		t.Fatalf("Unlock failed: %v", unlockErr)
	}

	// After unlock, OpenPayload succeeds again
	openedAgain, err := s.OpenPayload(ctx, sessionID, payloadID)
	if err != nil || !bytes.Equal(openedAgain, plaintext) {
		t.Fatalf("OpenPayload failed after re-unlock: %v", err)
	}
}

func startOpenWorkers(ctx context.Context, s *Store, sessionID, payloadID string, workers int) []*supervise.Task {
	tasks := make([]*supervise.Task, 0, workers)
	for i := range workers {
		workerID := strconv.Itoa(i)
		tasks = append(tasks, supervise.Go(ctx, "open-worker-"+workerID, func(context.Context) error {
			for range 50 {
				if _, openErr := s.OpenPayload(ctx, sessionID, payloadID); openErr != nil && !errors.Is(openErr, vault.ErrLocked) {
					return openErr
				}
			}
			return nil
		}))
	}
	return tasks
}

func runConcurrentLockUnlockRace(
	ctx context.Context,
	t *testing.T,
	s *Store,
	v *vault.Service,
	masterPass redact.Secret,
	sessionID, payloadID string,
) {
	t.Helper()
	const workers = 10
	tasks := startOpenWorkers(ctx, s, sessionID, payloadID, workers)

	tasks = append(tasks, supervise.Go(ctx, "lock-unlock-worker", func(context.Context) error {
		for range 5 {
			v.Lock()
			if unlockErr := v.Unlock(ctx, masterPass); unlockErr != nil {
				return fmt.Errorf("unlock: %w", unlockErr)
			}
		}
		v.Lock() // end in locked state
		return nil
	}))

	for _, task := range tasks {
		if waitErr := task.Wait(); waitErr != nil {
			t.Fatal(waitErr)
		}
	}
}

func TestVaultLockRaceNoDeadlockAndNoFailOpen(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, v, _, masterPass := newVaultAndStore(t)

	sessionID := "sess-vault-race"
	payloadID := "pl-race-1"
	plaintext := []byte("race-test-payload")

	sealed, key, err := s.SealPayload(ctx, sessionID, payloadID, "tool_result", plaintext)
	if err != nil {
		t.Fatal(err)
	}

	req := makeAcceptedReq(s, sessionID, "run-race", "cmd-race-init", payloadID, plaintext, []SealedPayload{sealed}, key)
	if _, appendErr := s.Append(ctx, req); appendErr != nil {
		t.Fatal(appendErr)
	}

	runConcurrentLockUnlockRace(ctx, t, s, v, masterPass, sessionID, payloadID)

	// Prove: after Lock returns, no protected operation succeeds (no fail-open)
	if v.Snapshot().State != vault.StateLocked {
		t.Fatalf("expected vault to be locked, got %v", v.Snapshot().State)
	}

	_, err = s.OpenPayload(ctx, sessionID, payloadID)
	if !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("after Lock returned, expected vault.ErrLocked, got %v", err)
	}

	_, _, err = s.SealPayload(ctx, sessionID, "pl-after", "tool_result", []byte("after-lock"))
	if !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("after Lock returned, expected vault.ErrLocked, got %v", err)
	}
}

func TestMetadataOnlyPathWorksWhenVaultLocked(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, v, _, _ := newVaultAndStore(t)

	// Lock the vault
	v.Lock()
	if v.Snapshot().State != vault.StateLocked {
		t.Fatalf("expected vault locked, got %v", v.Snapshot().State)
	}

	sessionID := "sess-meta-locked"
	runID := "run-meta-1"
	cmdID := "cmd-meta-locked-1"

	metaJSON := []byte(`{"InputRef":"meta-input-ref"}`)
	evt := makeRunAccepted(makeEnvelope(runID, 1, cmdID, 0, s.clk.Now()), sessionID, "meta-input-ref")

	req := &AppendRequest{
		SessionID:                 sessionID,
		RunID:                     runID,
		CommandID:                 cmdID,
		Kind:                      cmdAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		MetadataInput:             metaJSON,
		CanonicalInput:            metaJSON,
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1},
	}

	// Must succeed even though vault is locked
	outcome, err := s.Append(ctx, req)
	if err != nil {
		t.Fatalf("metadata-only append failed while vault locked: %v", err)
	}
	if !outcome.Committed {
		t.Fatal("expected metadata-only append to be committed")
	}

	// Deduplication on metadata-only command also succeeds while vault is locked
	dedupOutcome, err := s.Append(ctx, req)
	if err != nil {
		t.Fatalf("metadata-only dedup failed while vault locked: %v", err)
	}
	if !dedupOutcome.Duplicate {
		t.Fatal("expected deduplication to succeed while vault is locked")
	}
}
