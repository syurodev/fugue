package vendorstore

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/storage"
)

func TestPayloadTotalMigrationBackfillsExistingSessions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, err := storage.Open(ctx, filepath.Join(t.TempDir(), "old.db"))
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, db.Close()) })
	clk := clock.NewFake(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
	migrations, err := storage.Migrations()
	requireNoError(t, err)
	if len(migrations) < 6 {
		t.Fatal("payload total migration missing")
	}
	requireNoError(t, storage.RunMigrations(ctx, db, clk, migrations[:5]))
	for _, query := range []string{
		`INSERT INTO vendor_sessions(session_id,vendor,account_id,role,state,stop_proof,created_at,updated_at) VALUES ('s','v','a','r','closed','proved','t','t'),('empty','v','a','r','starting','','t','t')`,
		`INSERT INTO vendor_session_keys(session_id,key_id,wrapped_key,created_at) VALUES ('s','v1',x'01','t')`,
		`INSERT INTO vendor_payloads(payload_id,session_id,kind,key_id,size,envelope) VALUES ('a','s','event','v1',3,x'01'),('b','s','event','v1',7,x'01')`,
	} {
		_, err = db.ExecContext(ctx, query)
		requireNoError(t, err)
	}
	requireNoError(t, storage.Migrate(ctx, db, clk))
	for id, want := range map[string]int64{"s": 10, "empty": 0} {
		var got int64
		requireNoError(t, db.QueryRowContext(ctx, `SELECT payload_bytes FROM vendor_sessions WHERE session_id=?`, id).Scan(&got))
		if got != want {
			t.Fatalf("session %s total = %d, want %d", id, got, want)
		}
	}
}
