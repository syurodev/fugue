package workspace

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

type testFixture struct {
	svc     *Service
	db      *sql.DB
	clock   *clock.Fake
	ids     ids.Generator
	paths   Paths
	tempDir string
}

func newTestFixture(t *testing.T, opts ...ServiceOption) *testFixture {
	t.Helper()
	ctx := t.Context()
	temp := t.TempDir()

	clk := clock.NewFake(time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC))
	gen := ids.NewUUIDv7(clk, ids.NewDeterministic("test-workspace-seed"))

	dbPath := filepath.Join(temp, "test.db")
	db, openErr := storage.Open(ctx, dbPath)
	if openErr != nil {
		t.Fatalf("open storage: %v", openErr)
	}
	t.Cleanup(func() {
		closeQuietly(db)
	})

	if migErr := storage.Migrate(ctx, db, clk); migErr != nil {
		t.Fatalf("migrate storage: %v", migErr)
	}

	fakeHome := filepath.Join(temp, "userhome")
	dataDir := filepath.Join(temp, "datadir")
	env := config.MapEnv{}

	paths, pathErr := ResolvePaths(env, func() (string, error) { return fakeHome, nil }, dataDir)
	if pathErr != nil {
		t.Fatalf("resolve paths: %v", pathErr)
	}

	svc := NewService(paths, db, clk, gen, opts...)

	return &testFixture{
		svc:     svc,
		db:      db,
		clock:   clk,
		ids:     gen,
		paths:   paths,
		tempDir: temp,
	}
}
