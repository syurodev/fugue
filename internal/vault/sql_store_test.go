package vault

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/storage"
)

func openMigratedDB(t *testing.T, dbPath string, clk clock.Clock) *sql.DB {
	t.Helper()
	db, err := storage.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if migErr := storage.Migrate(t.Context(), db, clk); migErr != nil {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("Close after Migrate failure: %v", closeErr)
		}
		t.Fatalf("Migrate: %v", migErr)
	}
	return db
}

func TestSQLKeyringStorePersistsAcrossReopen(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), storage.DBFileName)
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))

	db := openMigratedDB(t, dbPath, clk)
	store := NewSQLKeyringStore(db)
	exists, err := store.Exists(ctx)
	if err != nil || exists {
		t.Fatalf("Exists before save = %v, %v, want false, nil", exists, err)
	}
	wantCode(t, func() error { _, loadErr := store.Load(ctx); return loadErr }(), errcode.NotFound)
	wantCode(t, store.Save(ctx, nil), errcode.InvalidArgument)

	keyring := cheapKeyring(t)
	if saveErr := store.Save(ctx, keyring); saveErr != nil {
		t.Fatalf("Save: %v", saveErr)
	}
	exists, err = store.Exists(ctx)
	if err != nil || !exists {
		t.Fatalf("Exists after save = %v, %v, want true, nil", exists, err)
	}
	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close first handle: %v", closeErr)
	}

	// Reopen fugue.db and verify the persisted keyring unlocks a fresh Service.
	db2 := openMigratedDB(t, dbPath, clk)
	t.Cleanup(func() {
		if closeErr := db2.Close(); closeErr != nil {
			t.Errorf("Close second handle: %v", closeErr)
		}
	})
	store2 := NewSQLKeyringStore(db2)
	loaded, err := store2.Load(ctx)
	if err != nil {
		t.Fatalf("Load after reopen: %v", err)
	}
	if diff := cmp.Diff(keyring, loaded); diff != "" {
		t.Fatalf("loaded keyring mismatch (-want +got):\n%s", diff)
	}

	verifyKeyringUpdateAndUnlock(t, store2, keyring)
}

func verifyKeyringUpdateAndUnlock(t *testing.T, store *SQLKeyringStore, keyring *crypto.Keyring) {
	t.Helper()
	ctx := t.Context()
	updated := *keyring
	updated.Version = 2
	if saveErr := store.Save(ctx, &updated); saveErr != nil {
		t.Fatalf("Save update: %v", saveErr)
	}
	reloaded, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load updated: %v", err)
	}
	if reloaded.Version != 2 {
		t.Fatalf("reloaded Version = %d, want 2", reloaded.Version)
	}
	if saveErr := store.Save(ctx, keyring); saveErr != nil {
		t.Fatalf("Save restore: %v", saveErr)
	}

	svc, err := NewService(ctx, store, ids.NewSequence("inc"))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if got := svc.Snapshot().State; got != StateLocked {
		t.Fatalf("State after reopen = %q, want %q", got, StateLocked)
	}
	if unlockErr := svc.Unlock(ctx, redact.NewSecret(cheapPassword)); unlockErr != nil {
		t.Fatalf("Unlock after reopen: %v", unlockErr)
	}
}

func TestSQLKeyringStoreErrorsOnCorruptAndClosedDatabase(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), storage.DBFileName)
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))

	db := openMigratedDB(t, dbPath, clk)
	if _, execErr := db.ExecContext(ctx, "INSERT INTO vault_meta (id, data) VALUES ('keyring', 'not-json')"); execErr != nil {
		t.Fatalf("insert corrupt keyring: %v", execErr)
	}
	store := NewSQLKeyringStore(db)
	wantCode(t, func() error { _, loadErr := store.Load(ctx); return loadErr }(), errcode.Internal)

	if closeErr := db.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	wantCode(t, func() error { _, existsErr := store.Exists(ctx); return existsErr }(), errcode.Internal)
	wantCode(t, func() error { _, loadErr := store.Load(ctx); return loadErr }(), errcode.Internal)
	wantCode(t, store.Save(ctx, cheapKeyring(t)), errcode.Internal)
}
