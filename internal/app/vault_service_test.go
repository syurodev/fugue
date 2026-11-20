package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	testVaultPassphrase = "correct-horse-battery-staple"
	testVaultWrongPass  = "wrong-passphrase-secret"
	testVaultLocal      = "app"
)

func newTestVaultAppService(t *testing.T, clk clock.Clock) (*vault.Service, *VaultService, *EventHub) {
	t.Helper()
	store := vault.NewMemoryKeyringStore(nil)
	v, err := vault.NewService(t.Context(), store, ids.NewSequence("v-inc-"))
	if err != nil {
		t.Fatal(err)
	}
	hub := NewEventHub("h-inc-1")
	svc, err := newVaultService(v, hub, clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return v, svc, hub
}

func TestVaultRPCsDenySessionAndNonLocalPrincipalsAtService(t *testing.T) {
	t.Parallel()
	_, svc, _ := newTestVaultAppService(t, clock.NewFake(time.Unix(1_700_000_000, 0)))
	for _, principal := range []string{PrincipalSession, "remote", ""} {
		if _, err := svc.Status(t.Context(), principal); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("Status(%q) code = %v, want permission_denied", principal, errcode.CodeOf(err))
		}
		if _, err := svc.Setup(t.Context(), principal, []byte(testVaultPassphrase)); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("Setup(%q) code = %v, want permission_denied", principal, errcode.CodeOf(err))
		}
		if _, err := svc.Unlock(t.Context(), principal, []byte(testVaultPassphrase)); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("Unlock(%q) code = %v, want permission_denied", principal, errcode.CodeOf(err))
		}
		if _, err := svc.UnlockWithRecovery(t.Context(), principal, []byte("rec")); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("UnlockWithRecovery(%q) code = %v, want permission_denied", principal, errcode.CodeOf(err))
		}
		if _, err := svc.Lock(t.Context(), principal); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("Lock(%q) code = %v, want permission_denied", principal, errcode.CodeOf(err))
		}
	}
}

func TestVaultUnlockBackoffAndTypedErrorOnFakeClock(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	_, svc, _ := newTestVaultAppService(t, clk)

	if _, err := svc.Unlock(t.Context(), testVaultLocal, []byte(testVaultPassphrase)); errcode.CodeOf(err) != errcode.VaultNotConfigured {
		t.Fatalf("Unlock before Setup code = %v, want %v", errcode.CodeOf(err), errcode.VaultNotConfigured)
	}
	res, err := svc.Setup(t.Context(), testVaultLocal, []byte(testVaultPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Clear()
	if _, lockErr := svc.Lock(t.Context(), testVaultLocal); lockErr != nil {
		t.Fatal(lockErr)
	}

	// First three failures carry zero/2s backoff.
	for i := range vaultFreeAttempts {
		assertWrongUnlockAttempt(t, svc, i+1)
	}

	// Immediately blocked with VaultRateLimited and RetryAfterSeconds=2.
	_, blockedErr := svc.Unlock(t.Context(), testVaultLocal, []byte(testVaultPassphrase))
	if errcode.CodeOf(blockedErr) != errcode.VaultRateLimited {
		t.Fatalf("blocked Unlock code = %v, want %v", errcode.CodeOf(blockedErr), errcode.VaultRateLimited)
	}
	st, err := svc.Status(t.Context(), testVaultLocal)
	if err != nil || st.RetryAfterSeconds != 2 {
		t.Fatalf("status while backed off = %+v, err=%v", st, err)
	}

	// Advance 2s; 4th failure doubles backoff to 4s.
	clk.Advance(2 * time.Second)
	_, uerr4 := svc.UnlockWithRecovery(t.Context(), testVaultLocal, []byte("bad-recovery"))
	if errcode.CodeOf(uerr4) != errcode.VaultInvalidPassphrase {
		t.Fatalf("attempt 4 code = %v, want %v", errcode.CodeOf(uerr4), errcode.VaultInvalidPassphrase)
	}
	assertVaultRetryAfter(t, svc, 4)

	// Advance 4s and unlock successfully; backoff resets completely.
	clk.Advance(4 * time.Second)
	if _, uerr := svc.Unlock(t.Context(), testVaultLocal, []byte(testVaultPassphrase)); uerr != nil {
		t.Fatalf("unlock after backoff: %v", uerr)
	}
	stOK := assertVaultRetryAfter(t, svc, 0)
	if stOK.State != string(vault.StateUnlocked) {
		t.Fatalf("status after unlock = %+v", stOK)
	}
}

func assertWrongUnlockAttempt(t *testing.T, svc *VaultService, attempt int) {
	t.Helper()
	_, uerr := svc.Unlock(t.Context(), testVaultLocal, []byte(testVaultWrongPass))
	if errcode.CodeOf(uerr) != errcode.VaultInvalidPassphrase {
		t.Fatalf("attempt %d code = %v, want %v", attempt, errcode.CodeOf(uerr), errcode.VaultInvalidPassphrase)
	}
	if strings.Contains(uerr.Error(), "remaining") || strings.Contains(uerr.Error(), "attempt") {
		t.Fatalf("invalid passphrase error leaked attempt count: %q", uerr.Error())
	}
}

func assertVaultRetryAfter(t *testing.T, svc *VaultService, want int64) VaultStatusView {
	t.Helper()
	st, err := svc.Status(t.Context(), testVaultLocal)
	if err != nil || st.RetryAfterSeconds != want {
		t.Fatalf("status = %+v (want RetryAfterSeconds=%d), err=%v", st, want, err)
	}
	return st
}

func TestVaultSetupRefusesAlreadyConfiguredAndNeverOverwrites(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	v, svc, _ := newTestVaultAppService(t, clk)

	res, err := svc.Setup(t.Context(), testVaultLocal, []byte(testVaultPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	res.Clear()

	sealed, err := v.SealRecord(vault.RecordBinding{RecordType: "vault-check", RecordID: "1", KeyID: "k"}, []byte("keep-me"))
	if err != nil {
		t.Fatal(err)
	}

	// Attempting Setup again while unlocked or locked fails typed and never overwrites.
	for range 2 {
		if _, serr := svc.Setup(t.Context(), testVaultLocal, []byte("overwrite-passphrase")); errcode.CodeOf(serr) != errcode.VaultAlreadyConfigured {
			t.Fatalf("repeat Setup code = %v, want %v", errcode.CodeOf(serr), errcode.VaultAlreadyConfigured)
		}
		if _, lerr := svc.Lock(t.Context(), testVaultLocal); lerr != nil {
			t.Fatal(lerr)
		}
	}

	if _, uerr := svc.Unlock(t.Context(), testVaultLocal, []byte(testVaultPassphrase)); uerr != nil {
		t.Fatalf("original passphrase failed after rejected repeat Setup: %v", uerr)
	}
	opened, err := v.OpenRecord(vault.RecordBinding{RecordType: "vault-check", RecordID: "1", KeyID: "k"}, sealed)
	if err != nil || !bytes.Equal(opened, []byte("keep-me")) {
		t.Fatalf("sealed record corrupted after rejected Setup: %q, err=%v", opened, err)
	}
}

func TestVaultLockClearsKeyAndTriggersVaultLocked(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	vSvc, err := newVaultService(f.vault, f.hub, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	defer vSvc.Close()

	adapterSession := newActorTestSession()
	adapterSession.interruptStarted = make(chan struct{})
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: adapterSession}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	defer mustCloseActorSessions(t, manager)
	mustSendActorInput(t, manager, "hello")

	sub, err := f.hub.Subscribe(t.Context(), SubscribeParams{Topic: TopicVault, Principal: testVaultLocal})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	st, err := vSvc.Lock(t.Context(), testVaultLocal)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != string(vault.StateLocked) {
		t.Fatalf("lock state = %q, want locked", st.State)
	}
	if _, serr := f.vault.SealRecord(vault.RecordBinding{RecordType: "t", RecordID: "1", KeyID: "k"}, []byte("x")); !errors.Is(serr, vault.ErrLocked) {
		t.Fatalf("SealRecord after Lock err = %v, want ErrLocked", serr)
	}
	<-adapterSession.interruptStarted
	adapterSession.observations <- claude.Observation{TurnEnd: &claude.TurnEnd{TurnID: testFugueTurnID, Outcome: claude.OutcomeCompleted}}

	// Vault topic publishes the lock transition and active session enters waiting(vault_locked).
	ev, ok := sub.Next(t.Context())
	if !ok {
		t.Fatal("vault topic closed before delivering lock event")
	}
	var view VaultStatusView
	if err := json.Unmarshal(ev.Payload, &view); err != nil || view.State != string(vault.StateLocked) {
		t.Fatalf("vault topic event = %s, err=%v", ev.Payload, err)
	}

	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), testActorSessionID)
		return snapErr == nil && snap.State == vendorstore.StateWaiting && snap.WaitingReason == string(vendor.WaitingVaultLocked)
	})
}

func TestVaultSecretAbsentFromLogsErrorsTopicsAndFormatting(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(redact.NewJSONHandler(&logs, slog.LevelDebug)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	_, svc, hub := newTestVaultAppService(t, clk)

	res := mustSetupCleared(t, svc)
	recPlain := string(res.RecoveryKey)
	defer res.Clear()
	assertSetupResultRedacted(t, res, recPlain, &logs)

	sub, err := hub.Subscribe(t.Context(), SubscribeParams{Topic: TopicVault, Principal: testVaultLocal})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	topicOut, badErr := exerciseVaultUnlockAndCollectTopic(t, svc, sub, res.RecoveryKey)
	assertNoSecretsInOutputs(t, []string{testVaultPassphrase, testVaultWrongPass, recPlain},
		[]string{logs.String(), topicOut, fmt.Sprint(badErr)})
}

func mustSetupCleared(t *testing.T, svc *VaultService) VaultSetupResult {
	t.Helper()
	pass := []byte(testVaultPassphrase)
	res, err := svc.Setup(t.Context(), testVaultLocal, pass)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pass, make([]byte, len(pass))) {
		t.Fatal("Setup did not zero caller passphrase slice")
	}
	return res
}

func assertSetupResultRedacted(t *testing.T, res VaultSetupResult, recPlain string, logs *bytes.Buffer) {
	t.Helper()
	formatted := fmt.Sprintf("%v|%#v|%+v|%s", res, res, res, res)
	slog.Info("vault setup test", "res", res)
	if strings.Contains(formatted, recPlain) || strings.Contains(logs.String(), recPlain) {
		t.Fatalf("recovery key leaked in formatting or logs: %q / %q", formatted, logs.String())
	}
}

func exerciseVaultUnlockAndCollectTopic(t *testing.T, svc *VaultService, sub *Subscription, rec []byte) (string, error) {
	t.Helper()
	if _, err := svc.Lock(t.Context(), testVaultLocal); err != nil {
		t.Fatal(err)
	}
	wrong := []byte(testVaultWrongPass)
	_, badErr := svc.Unlock(t.Context(), testVaultLocal, wrong)
	if !bytes.Equal(wrong, make([]byte, len(wrong))) {
		t.Fatal("Unlock did not zero caller passphrase slice on error")
	}
	recArg := bytes.Clone(rec)
	if _, recErr := svc.UnlockWithRecovery(t.Context(), testVaultLocal, recArg); recErr != nil {
		t.Fatal(recErr)
	}
	if !bytes.Equal(recArg, make([]byte, len(recArg))) {
		t.Fatal("UnlockWithRecovery did not zero caller recovery slice")
	}
	var topicOut strings.Builder
	snapState, _, ok := sub.Snapshot()
	if !ok {
		t.Fatal("missing TopicVault snapshot")
	}
	topicOut.Write(snapState)
	for range 2 {
		if ev, evOK := sub.Next(t.Context()); evOK {
			topicOut.Write(ev.Payload)
		}
	}
	return topicOut.String(), badErr
}

func assertNoSecretsInOutputs(t *testing.T, secretsList, outputs []string) {
	t.Helper()
	for _, s := range secretsList {
		for _, hay := range outputs {
			if strings.Contains(hay, s) {
				t.Fatalf("secret %q leaked in output %q", s, hay)
			}
		}
	}
}

func TestVaultConcurrentUnlockAttemptsCannotBypassBackoff(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	_, svc, _ := newTestVaultAppService(t, clk)
	res, err := svc.Setup(t.Context(), testVaultLocal, []byte(testVaultPassphrase))
	if err != nil {
		t.Fatal(err)
	}
	res.Clear()
	if _, lerr := svc.Lock(t.Context(), testVaultLocal); lerr != nil {
		t.Fatal(lerr)
	}

	const attempts = 20
	var kdfCalls atomic.Int32
	start := make(chan struct{})
	errs := make(chan error, attempts)
	var wg sync.WaitGroup
	countingFn := func(context.Context, []byte) error {
		kdfCalls.Add(1)
		return errcode.New(errcode.PermissionDenied, "wrong")
	}
	for range attempts {
		wg.Go(func() {
			<-start
			_, uerr := svc.unlockWith(t.Context(), testVaultLocal, []byte(testVaultWrongPass), countingFn)
			errs <- uerr
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	assertConcurrentUnlockOutcomes(t, errs, int(kdfCalls.Load()), attempts)
}

func assertConcurrentUnlockOutcomes(t *testing.T, errs <-chan error, kdfCalls, attempts int) {
	t.Helper()
	if kdfCalls < 1 || kdfCalls > vaultFreeAttempts {
		t.Fatalf("concurrent KDF calls = %d, want in [1..%d]", kdfCalls, vaultFreeAttempts)
	}
	var invalidCount, rateCount int
	for uerr := range errs {
		code := errcode.CodeOf(uerr)
		if code == errcode.VaultInvalidPassphrase {
			invalidCount++
			continue
		}
		if code == errcode.VaultRateLimited {
			rateCount++
			continue
		}
		t.Fatalf("unexpected concurrent unlock error: %v", uerr)
	}
	if invalidCount != kdfCalls || invalidCount+rateCount != attempts {
		t.Fatalf("results invalid=%d rateLimited=%d kdfCalls=%d", invalidCount, rateCount, kdfCalls)
	}
}
