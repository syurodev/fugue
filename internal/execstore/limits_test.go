package execstore

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func TestPayloadUsageValidation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	var nilStore *Store
	if _, err := nilStore.PayloadUsage(ctx, "s"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable, got %v", err)
	}
	if _, err := nilStore.RunPayloadUsage(ctx, "s", "r"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable, got %v", err)
	}
	if _, err := s.PayloadUsage(ctx, ""); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if _, err := s.RunPayloadUsage(ctx, "", "r"); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if _, err := s.RunPayloadUsage(ctx, "s", ""); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	longID := strings.Repeat("x", 129)
	if _, err := s.PayloadUsage(ctx, longID); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if _, err := s.RunPayloadUsage(ctx, longID, "run-1"); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if _, err := s.RunPayloadUsage(ctx, "s", longID); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestPayloadUsageReporting(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-usage-test"
	p1, k1, err := s.SealPayload(ctx, sessionID, "pl-u1", testToolResultKind, []byte("usage payload"))
	if err != nil {
		t.Fatal(err)
	}
	req := makeAcceptedReq(s, sessionID, "run-u1", "cmd-u1", "pl-u1", []byte("usage payload"), []SealedPayload{p1}, k1)
	if _, appendErr := s.Append(ctx, req); appendErr != nil {
		t.Fatal(appendErr)
	}

	usage, err := s.PayloadUsage(ctx, sessionID)
	if err != nil || usage != int64(len("usage payload")) {
		t.Fatalf("expected %d, got %d (err: %v)", len("usage payload"), usage, err)
	}

	runUsage, err := s.RunPayloadUsage(ctx, sessionID, "run-u1")
	if err != nil || runUsage != int64(len("usage payload")) {
		t.Fatalf("expected %d, got %d (err: %v)", len("usage payload"), runUsage, err)
	}

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.PayloadUsage(canceledCtx, sessionID); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable for canceled context, got %v", err)
	}
	if _, err := s.RunPayloadUsage(canceledCtx, sessionID, "run-u1"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable for canceled context, got %v", err)
	}
}

func TestValidateBatchPayloadsLimits(t *testing.T) {
	t.Parallel()

	// Negative payload size in batch
	negPayload := SealedPayload{PayloadID: "pl-neg", Size: -1}
	if _, _, err := validateBatchPayloads([]SealedPayload{negPayload}); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("expected ErrPayloadTooLarge for negative size, got %v", err)
	}

	// Duplicate payload ID in batch is deduplicated
	dup1 := SealedPayload{PayloadID: "pl-dup", Size: 10}
	dup2 := SealedPayload{PayloadID: "pl-dup", Size: 10}
	total, seen, err := validateBatchPayloads([]SealedPayload{dup1, dup2})
	if err != nil || total != 10 || len(seen) != 1 {
		t.Fatalf("expected total 10 and 1 seen, got total %d, seen %d (err: %v)", total, len(seen), err)
	}
}

func TestProtectedInputAdditionEmptyAndInBatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	conn, err := s.acquireConfiguredConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	// Case 1: Empty ProtectedInputID
	req := &AppendRequest{SessionID: "sess-pi-empty", RunID: "run-empty-pi"}
	size, err := getProtectedInputAddition(ctx, conn, req, nil)
	if err != nil || size != 0 {
		t.Fatalf("expected 0, got %d (err: %v)", size, err)
	}

	// Case 2: ProtectedInputID present in seenPayloads
	req.ProtectedInputID = "pl-in-batch"
	size, err = getProtectedInputAddition(ctx, conn, req, map[string]struct{}{"pl-in-batch": {}})
	if err != nil || size != 0 {
		t.Fatalf("expected 0 for in-batch, got %d (err: %v)", size, err)
	}
}

func TestProtectedInputAdditionCalculation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-pi-add"
	sealed, key, err := s.SealPayload(ctx, sessionID, "pl-seeded-1", testToolResultKind, []byte("12345"))
	if err != nil {
		t.Fatal(err)
	}
	seedReq := makeAcceptedReq(s, sessionID, "run-1", "cmd-init", "pl-seeded-1", []byte("12345"), []SealedPayload{sealed}, key)
	if _, appendErr := s.Append(ctx, seedReq); appendErr != nil {
		t.Fatal(appendErr)
	}

	conn, err := s.acquireConfiguredConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	// Case 3: ProtectedInputID not in DB
	req := &AppendRequest{SessionID: sessionID, RunID: "run-1", ProtectedInputID: "pl-not-found"}
	size, err := getProtectedInputAddition(ctx, conn, req, nil)
	if err != nil || size != 0 {
		t.Fatalf("expected 0 for not found, got %d (err: %v)", size, err)
	}

	// Case 4a: ProtectedInputID already referenced in the same run (deduped within run)
	reqSameRun := &AppendRequest{SessionID: sessionID, RunID: "run-1", ProtectedInputID: "pl-seeded-1"}
	size, err = getProtectedInputAddition(ctx, conn, reqSameRun, nil)
	if err != nil || size != 0 {
		t.Fatalf("expected 0 for payload already in same run, got %d (err: %v)", size, err)
	}
}

func TestProtectedInputAdditionNewRun(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-pi-add-new"
	sealed, key, err := s.SealPayload(ctx, sessionID, "pl-seeded-1", testToolResultKind, []byte("12345"))
	if err != nil {
		t.Fatal(err)
	}
	seedReq := makeAcceptedReq(s, sessionID, "run-1", "cmd-init", "pl-seeded-1", []byte("12345"), []SealedPayload{sealed}, key)
	if _, appendErr := s.Append(ctx, seedReq); appendErr != nil {
		t.Fatal(appendErr)
	}

	conn, err := s.acquireConfiguredConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	// Case 4b: ProtectedInputID present in DB but new to run-second-pi
	reqNewRun := &AppendRequest{SessionID: sessionID, RunID: "run-second-pi", ProtectedInputID: "pl-seeded-1"}
	size, err := getProtectedInputAddition(ctx, conn, reqNewRun, nil)
	if err != nil || size != 5 {
		t.Fatalf("expected 5 for new run referencing existing payload, got %d (err: %v)", size, err)
	}

	// Test checkRunTotalLimit with input addition
	if err := checkRunTotalLimit(ctx, conn, reqNewRun, 10, nil); err != nil {
		t.Fatalf("expected checkRunTotalLimit to pass, got %v", err)
	}
	// Test checkRunTotalLimit input addition overflow
	if err := checkRunTotalLimit(ctx, conn, reqNewRun, math.MaxInt64-1, nil); !errors.Is(err, ErrRunPayloadLimitExceeded) {
		t.Fatalf("expected ErrRunPayloadLimitExceeded, got %v", err)
	}
}

func TestRunAndSessionOverflowLimits(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-overflow"
	conn, err := s.acquireConfiguredConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	req := &AppendRequest{SessionID: sessionID, RunID: "run-overflow"}
	if err := checkRunTotalLimit(ctx, conn, req, 100, nil); err != nil {
		t.Fatalf("expected run total check to pass, got %v", err)
	}
	// Check run addition integer overflow
	if err := checkRunTotalLimit(ctx, conn, req, math.MaxInt64, nil); !errors.Is(err, ErrRunPayloadLimitExceeded) {
		t.Fatalf("expected ErrRunPayloadLimitExceeded, got %v", err)
	}
	// Check run addition exceeding MaxRunPayloadTotal without overflow
	if err := checkRunTotalLimit(ctx, conn, req, MaxRunPayloadTotal+1, nil); !errors.Is(err, ErrRunPayloadLimitExceeded) {
		t.Fatalf("expected ErrRunPayloadLimitExceeded, got %v", err)
	}
	// Check session total limit integer overflow
	if err := checkSessionTotalLimit(ctx, conn, sessionID, math.MaxInt64); !errors.Is(err, ErrSessionPayloadLimitExceeded) {
		t.Fatalf("expected ErrSessionPayloadLimitExceeded, got %v", err)
	}
	// Check session total exceeding MaxSessionPayloadTotal without overflow
	if err := checkSessionTotalLimit(ctx, conn, sessionID, MaxSessionPayloadTotal+1); !errors.Is(err, ErrSessionPayloadLimitExceeded) {
		t.Fatalf("expected ErrSessionPayloadLimitExceeded, got %v", err)
	}
}
