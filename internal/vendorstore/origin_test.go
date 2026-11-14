package vendorstore

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/storage"
)

func TestVendorTurnOriginMigrationPreservesExistingRows(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	db, openErr := storage.Open(ctx, filepath.Join(t.TempDir(), "origin.db"))
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { requireNoError(t, db.Close()) })
	migrations, migrationErr := storage.Migrations()
	if migrationErr != nil {
		t.Fatal(migrationErr)
	}
	if err := storage.RunMigrations(ctx, db, clk, migrations[:6]); err != nil {
		t.Fatal(err)
	}
	// The rows are written with the schema of migration 6 directly: the
	// current store code writes tables that later migrations add.
	if _, err := db.ExecContext(ctx, `INSERT INTO vendor_sessions(session_id,vendor,account_id,role,state,created_at,updated_at,last_sequence)
		VALUES ('old','claude','account','assistant','starting','2026-11-08T00:00:00Z','2026-11-08T00:00:00Z',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO vendor_turns(session_id,turn_id,created_at) VALUES ('old','old-turn','2026-11-08T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := storage.Migrate(ctx, db, clk); err != nil {
		t.Fatal(err)
	}
	var origin string
	if err := db.QueryRowContext(ctx, `SELECT origin FROM vendor_turns WHERE session_id='old' AND turn_id='old-turn'`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != TurnOriginFugue {
		t.Fatalf("existing turn origin = %q, want %q", origin, TurnOriginFugue)
	}
}

func TestVendorTurnOriginIsProjectedAndUnknownOriginRejected(t *testing.T) {
	t.Parallel()
	store, db := testStore(t)
	if err := store.CreateSession(t.Context(), &Session{ID: "s", Vendor: journalTestVendor, AccountID: "a", Role: journalTestRole, State: StateStarting}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "vendor-123e4567-e89b-12d3-a456-426614174000", TurnOrigin: TurnOriginVendor}); err != nil {
		t.Fatal(err)
	}
	var origin string
	if err := db.QueryRowContext(t.Context(), `SELECT origin FROM vendor_turns WHERE session_id='s' AND turn_id='vendor-123e4567-e89b-12d3-a456-426614174000'`).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != TurnOriginVendor {
		t.Fatalf("turn origin = %q, want vendor", origin)
	}
	_, err := store.Append(t.Context(), "s", 2, &Event{Type: VendorTurnStarted, TurnID: "vendor-not-a-uuid", TurnOrigin: TurnOriginVendor})
	if err == nil || errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("malformed vendor turn ID error = %v, want invalid argument", err)
	}
	_, err = store.Append(t.Context(), "s", 2, &Event{Type: VendorTurnStarted, TurnID: "other", TurnOrigin: "untrusted"})
	if err == nil || errors.Is(err, ErrUnavailable) || errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("unknown origin error = %v, want invalid argument", err)
	}
}
