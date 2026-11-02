package credentials

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fixture is a Book over a migrated database and a vault that is set up and
// unlocked.
type fixture struct {
	book   *Book
	vault  *vault.Service
	clock  *clock.Fake
	db     *sql.DB
	dbPath string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
	dbPath := filepath.Join(t.TempDir(), storage.DBFileName)
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close the database: %v", closeErr)
		}
	})
	if err = storage.Migrate(ctx, db, clk); err != nil {
		t.Fatal(err)
	}
	v, err := vault.NewService(ctx, vault.NewSQLKeyringStore(db), ids.NewSequence("inc-"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v.Setup(ctx, redact.NewSecret("a long master password")); err != nil {
		t.Fatal(err)
	}
	return &fixture{book: NewBook(db, v, clk, ids.NewSequence("acct")), vault: v, clock: clk, db: db, dbPath: dbPath}
}

func wantCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want code %q", want)
	}
	if got := errcode.CodeOf(err); got != want {
		t.Fatalf("code = %q, want %q (%v)", got, want, err)
	}
}
