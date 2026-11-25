package execstore

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	crashHelperFlag      = "-fugue-test-execstore-crash"
	migratedTemplateFile = "migrated.db"
	seededTemplateFile   = "seeded.db"
)

var errTemplateSetup = errors.New("execstore test template setup failed")

func TestMain(m *testing.M) {
	if slices.Contains(os.Args[1:], crashHelperFlag) {
		runCrashHelper()
		return
	}
	cleanup, err := buildTestTemplates()
	if err != nil {
		os.Exit(1)
	}
	goleak.VerifyTestMain(m, goleak.Cleanup(cleanup))
}

type templateTB struct {
	dir string
	err error
}

func (tb *templateTB) Helper()                  {}
func (tb *templateTB) Context() context.Context { return context.Background() }
func (tb *templateTB) TempDir() string          { return tb.dir }
func (tb *templateTB) Fatal(args ...any)        { tb.err = fmt.Errorf("%w: %v", errTemplateSetup, args) }

func execstoreTemplateDir(pid int) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("fugue-execstore-template-%d", pid))
}

func buildTestTemplates() (func(int), error) {
	dir := execstoreTemplateDir(os.Getpid())
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("reset execstore template dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create execstore template dir: %w", err)
	}
	cleanup := func(int) {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return
		}
	}
	if err := buildMigratedTemplateFile(dir); err != nil {
		cleanup(0)
		return nil, err
	}
	if err := buildSeededTemplateFile(dir); err != nil {
		cleanup(0)
		return nil, err
	}
	return cleanup, nil
}

func buildMigratedTemplateFile(dir string) error {
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	db, err := storage.Open(ctx, filepath.Join(dir, migratedTemplateFile))
	if err != nil {
		return fmt.Errorf("open migrated template: %w", err)
	}
	if migErr := storage.Migrate(ctx, db, clk); migErr != nil {
		return fmt.Errorf("migrate template: %w", errors.Join(migErr, db.Close()))
	}
	if cerr := db.Close(); cerr != nil {
		return fmt.Errorf("close migrated template: %w", cerr)
	}
	return nil
}

func buildSeededTemplateFile(dir string) error {
	dstPath := filepath.Join(dir, seededTemplateFile)
	if err := copyTemplateFile(dir, migratedTemplateFile, dstPath); err != nil {
		return err
	}
	ctx := context.Background()
	seedDB, err := storage.Open(ctx, dstPath)
	if err != nil {
		return fmt.Errorf("open seeded template: %w", err)
	}
	tb := &templateTB{dir: dir}
	cheapvault.Save(tb, vault.NewSQLKeyringStore(seedDB))
	if tb.err != nil {
		return errors.Join(tb.err, seedDB.Close())
	}
	if cerr := seedDB.Close(); cerr != nil {
		return fmt.Errorf("close seeded template: %w", cerr)
	}
	return nil
}

func copyTemplateFile(srcDir, srcName, dstPath string) error {
	raw, err := fs.ReadFile(os.DirFS(srcDir), srcName)
	if err != nil {
		return fmt.Errorf("read template %s: %w", srcName, err)
	}
	root, err := os.OpenRoot(filepath.Dir(dstPath))
	if err != nil {
		return fmt.Errorf("open destination dir: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			return
		}
	}()
	if writeErr := root.WriteFile(filepath.Base(dstPath), raw, 0o600); writeErr != nil {
		return fmt.Errorf("write template %s: %w", dstPath, writeErr)
	}
	return nil
}

func runCrashHelper() {
	reader := bufio.NewReader(os.Stdin)
	dbPath, err := reader.ReadString('\n')
	if err != nil {
		os.Exit(1)
	}
	dbPath = strings.TrimSpace(dbPath)

	ctx := context.Background()
	db, err := storage.Open(ctx, dbPath)
	if err != nil {
		os.Exit(2)
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		if closeErr := db.Close(); closeErr != nil {
			os.Exit(6)
		}
		os.Exit(3)
	}

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		os.Exit(4)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO exec_purges (session_id, requested_at) VALUES ('crash-session', 'now')"); err != nil {
		os.Exit(5)
	}

	// Real subprocess termination before COMMIT
	os.Exit(42)
}
