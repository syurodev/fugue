package execstore

import (
	"bufio"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/storage"
)

const crashHelperFlag = "-fugue-test-execstore-crash"

func TestMain(m *testing.M) {
	if slices.Contains(os.Args[1:], crashHelperFlag) {
		runCrashHelper()
		return
	}
	goleak.VerifyTestMain(m)
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
