package secretstore

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/internal/vault"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func testNow() time.Time { return time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC) }

type secretFixture struct {
	store *Store
	vault *vault.Service
	clk   *clock.Fake
}

func newSecretFixture(t *testing.T) *secretFixture {
	t.Helper()
	db, err := storage.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close db: %v", closeErr)
		}
	})

	clk := clock.NewFake(testNow())
	if migErr := storage.Migrate(t.Context(), db, clk); migErr != nil {
		t.Fatalf("migrate storage: %v", migErr)
	}
	v := cheapvault.Unlocked(t)
	return &secretFixture{
		store: New(db, v, clk, ids.NewUUIDv7(clk, ids.CryptoRandom{})),
		vault: v,
		clk:   clk,
	}
}

func TestSecretStoreRoundTripAndLockedVaultINV09(t *testing.T) {
	t.Parallel()
	f := newSecretFixture(t)
	ctx := t.Context()

	val := []byte("sk-live-secret-12345678")
	meta, seq, err := f.store.PutSecret(ctx, ScopeGlobal, "", "OPENAI_API_KEY", "Primary key", val)
	if err != nil || meta.SecretID == "" || meta.Name != "OPENAI_API_KEY" || seq <= 0 {
		t.Fatalf("PutSecret = (%+v, %d, %v)", meta, seq, err)
	}
	assertOpenedAndKnownValue(t, f, meta.SecretID, val)

	f.vault.Lock()
	assertLockedVaultBehavior(t, f, meta.SecretID)
}

func assertOpenedAndKnownValue(t *testing.T, f *secretFixture, secretID string, want []byte) {
	t.Helper()
	ctx := t.Context()
	opened, openErr := f.store.OpenSecretValue(ctx, secretID)
	if openErr != nil || !bytes.Equal(opened, want) {
		t.Fatalf("OpenSecretValue = (%q, %v), want %q", opened, openErr, want)
	}
	clear(opened)
	kv, kvErr := f.store.KnownValues(ctx)
	if kvErr != nil || len(kv) != 1 || kv[0].Kind != "vault" || kv[0].Value.Reveal() != string(want) {
		t.Fatalf("KnownValues = (%+v, %v)", kv, kvErr)
	}
}

func assertLockedVaultBehavior(t *testing.T, f *secretFixture, secretID string) {
	t.Helper()
	ctx := t.Context()
	list, listErr := f.store.ListSecrets(ctx, "", "")
	if listErr != nil || len(list) != 1 || list[0].SecretID != secretID {
		t.Fatalf("ListSecrets while locked = (%+v, %v)", list, listErr)
	}
	snap, snapErr := f.store.Snapshot(ctx)
	if snapErr != nil || len(snap.Secrets) != 1 {
		t.Fatalf("Snapshot while locked = (%+v, %v)", snap, snapErr)
	}
	if _, _, putErr := f.store.PutSecret(ctx, ScopeGlobal, "", "OTHER", "", []byte("12345678")); errcode.CodeOf(putErr) != errcode.FailedPrecondition {
		t.Fatalf("PutSecret while locked code = %v", errcode.CodeOf(putErr))
	}
	if _, _, delErr := f.store.DeleteSecret(ctx, secretID); errcode.CodeOf(delErr) != errcode.FailedPrecondition {
		t.Fatalf("DeleteSecret while locked code = %v", errcode.CodeOf(delErr))
	}
	if _, openErr := f.store.OpenSecretValue(ctx, secretID); errcode.CodeOf(openErr) != errcode.FailedPrecondition {
		t.Fatalf("OpenSecretValue while locked code = %v", errcode.CodeOf(openErr))
	}
	if kvLocked, kvErr := f.store.KnownValues(ctx); kvErr != nil || len(kvLocked) != 0 {
		t.Fatalf("KnownValues while locked = (%+v, %v)", kvLocked, kvErr)
	}
}

func TestSecretStoreValidationUpsertDeleteAndJournal(t *testing.T) {
	t.Parallel()
	f := newSecretFixture(t)
	assertPutValidationErrors(t, f)
	shortMeta := assertShortAndUpsert(t, f)
	assertDeleteAndJournal(t, f, shortMeta.SecretID)
}

func assertPutValidationErrors(t *testing.T, f *secretFixture) {
	t.Helper()
	ctx := t.Context()
	if _, _, err := f.store.PutSecret(ctx, ScopeGlobal, "", "-bad-name", "", []byte("12345678")); errcode.CodeOf(err) != errcode.SecretsInvalidName {
		t.Fatalf("invalid name code = %v", errcode.CodeOf(err))
	}
	if _, _, err := f.store.PutSecret(ctx, ScopeGlobal, "", "SHORT", "", []byte("")); errcode.CodeOf(err) != errcode.SecretsValueTooLarge {
		t.Fatalf("empty value code = %v", errcode.CodeOf(err))
	}
	if _, _, err := f.store.PutSecret(ctx, ScopeGlobal, "", "TOO_BIG", "", bytes.Repeat([]byte("a"), MaxValueBytes+1)); errcode.CodeOf(err) != errcode.SecretsValueTooLarge {
		t.Fatalf("oversized value code = %v", errcode.CodeOf(err))
	}
}

func assertShortAndUpsert(t *testing.T, f *secretFixture) Meta {
	t.Helper()
	ctx := t.Context()
	shortMeta, _, err := f.store.PutSecret(ctx, ScopeWorkspace, "ws-1", "PIN_CODE", "", []byte("1234"))
	if err != nil {
		t.Fatalf("PutSecret short: %v", err)
	}
	if kv, kvErr := f.store.KnownValues(ctx); kvErr != nil || len(kv) != 0 {
		t.Fatalf("KnownValues with short secret = (%+v, %v)", kv, kvErr)
	}

	f.clk.Advance(time.Minute)
	updatedMeta, _, upErr := f.store.PutSecret(ctx, ScopeWorkspace, "ws-1", "PIN_CODE", "Updated", []byte("long-enough-secret"))
	if upErr != nil {
		t.Fatalf("PutSecret upsert: %v", upErr)
	}
	if updatedMeta.SecretID != shortMeta.SecretID || !updatedMeta.CreatedAt.Equal(shortMeta.CreatedAt) || !updatedMeta.UpdatedAt.After(shortMeta.UpdatedAt) {
		t.Fatalf("upsert meta mismatch: old=%+v new=%+v", shortMeta, updatedMeta)
	}
	if kv, kvErr := f.store.KnownValues(ctx); kvErr != nil || len(kv) != 1 || kv[0].Value.Reveal() != "long-enough-secret" {
		t.Fatalf("KnownValues after upsert = (%+v, %v)", kv, kvErr)
	}
	return shortMeta
}

func assertDeleteAndJournal(t *testing.T, f *secretFixture, secretID string) {
	t.Helper()
	ctx := t.Context()
	delMeta, seq, delErr := f.store.DeleteSecret(ctx, secretID)
	if delErr != nil || delMeta.SecretID != secretID || seq <= 0 {
		t.Fatalf("DeleteSecret = (%+v, %d, %v)", delMeta, seq, delErr)
	}
	if _, openErr := f.store.OpenSecretValue(ctx, secretID); errcode.CodeOf(openErr) != errcode.StoreRecordNotFound {
		t.Fatalf("OpenSecretValue after delete code = %v", errcode.CodeOf(openErr))
	}
	if _, _, delErr2 := f.store.DeleteSecret(ctx, secretID); errcode.CodeOf(delErr2) != errcode.StoreRecordNotFound {
		t.Fatalf("second DeleteSecret code = %v", errcode.CodeOf(delErr2))
	}
	changes, chErr := f.store.ChangesSince(ctx, 0)
	if chErr != nil || len(changes) != 3 {
		t.Fatalf("ChangesSince(0) = (%+v, %v), want 3", changes, chErr)
	}
	if changes[2].Kind != changeSecretDeleted || strings.Contains(changes[2].Name, "long-enough") {
		t.Fatalf("unexpected change[2]: %+v", changes[2])
	}
}

func TestSecretStoreEdgeCasesAndJournalCursor(t *testing.T) {
	t.Parallel()
	f := newSecretFixture(t)
	assertScopeAndInputErrors(t, f)
	assertScopeFilterAndNilIDs(t, f)
	assertClosedDBErrors(t, f)
}

func assertScopeFilterAndNilIDs(t *testing.T, f *secretFixture) {
	t.Helper()
	ctx := t.Context()
	if _, seqErr := ChangeSequence(-1); errcode.CodeOf(seqErr) != errcode.Internal {
		t.Fatalf("ChangeSequence(-1) code = %v, want internal", errcode.CodeOf(seqErr))
	}
	if seq, seqErr := ChangeSequence(5); seqErr != nil || seq != 5 {
		t.Fatalf("ChangeSequence(5) = (%d, %v)", seq, seqErr)
	}
	if _, _, err := f.store.PutSecret(ctx, ScopeGlobal, "", "G_KEY", "", []byte("12345678")); err != nil {
		t.Fatalf("PutSecret global: %v", err)
	}
	if _, _, err := f.store.PutSecret(ctx, ScopeWorkspace, "ws-9", "W_KEY", "", []byte("12345678")); err != nil {
		t.Fatalf("PutSecret workspace: %v", err)
	}
	wsList, err := f.store.ListSecrets(ctx, ScopeWorkspace, "ws-9")
	if err != nil || len(wsList) != 1 || wsList[0].Name != "W_KEY" {
		t.Fatalf("ListSecrets(workspace, ws-9) = (%+v, %v)", wsList, err)
	}
	nilIDsStore := New(f.store.db, f.vault, nil, nil)
	if _, _, idErr := nilIDsStore.PutSecret(ctx, ScopeGlobal, "", "NO_IDS", "", []byte("12345678")); errcode.CodeOf(idErr) != errcode.Internal {
		t.Fatalf("PutSecret with nil ids code = %v, want internal", errcode.CodeOf(idErr))
	}
}

func assertClosedDBErrors(t *testing.T, f *secretFixture) {
	t.Helper()
	ctx := t.Context()
	if closeErr := f.store.db.Close(); closeErr != nil {
		t.Fatalf("close db: %v", closeErr)
	}
	if _, listErr := f.store.ListSecrets(ctx, "", ""); errcode.CodeOf(listErr) != errcode.Internal {
		t.Fatalf("ListSecrets on closed DB code = %v, want internal", errcode.CodeOf(listErr))
	}
	if _, _, putErr := f.store.PutSecret(ctx, ScopeGlobal, "", "CLOSED", "", []byte("12345678")); errcode.CodeOf(putErr) != errcode.Internal {
		t.Fatalf("PutSecret on closed DB code = %v, want internal", errcode.CodeOf(putErr))
	}
}

func assertScopeAndInputErrors(t *testing.T, f *secretFixture) {
	t.Helper()
	ctx := t.Context()
	if err := ValidateScope(ScopeGlobal, "ws-1"); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("global with scope_id code = %v", errcode.CodeOf(err))
	}
	if err := ValidateScope(ScopeWorkspace, ""); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("workspace without scope_id code = %v", errcode.CodeOf(err))
	}
	if err := ValidateScope("other", ""); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("unknown scope code = %v", errcode.CodeOf(err))
	}
	if _, err := f.store.ListSecrets(ctx, "", "ws-1"); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("ListSecrets scope_id without scope_kind code = %v", errcode.CodeOf(err))
	}
	if _, _, err := f.store.PutSecret(ctx, ScopeGlobal, "", "BAD_DESC", strings.Repeat("d", MaxDescriptionBytes+1), []byte("12345678")); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("long description code = %v", errcode.CodeOf(err))
	}
	if _, _, err := f.store.DeleteSecret(ctx, ""); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("DeleteSecret empty id code = %v", errcode.CodeOf(err))
	}
	if _, err := f.store.OpenSecretValue(ctx, ""); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("OpenSecretValue empty id code = %v", errcode.CodeOf(err))
	}
}
