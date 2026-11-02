package storage

import (
	"bufio"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

const holdLockArgFlag = "-fugue-test-hold-lock"

func TestMain(m *testing.M) {
	if slices.Contains(os.Args[1:], holdLockArgFlag) {
		runHoldLockHelper()
		return
	}
	goleak.VerifyTestMain(m)
}

func runHoldLockHelper() {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		writeQuietly(os.Stderr, err.Error()+"\n")
		os.Exit(2)
	}
	lock, err := AcquireLock(strings.TrimSpace(line))
	if err != nil {
		writeQuietly(os.Stderr, err.Error()+"\n")
		os.Exit(2)
	}
	writeQuietly(os.Stdout, "LOCKED\n")
	if _, waitErr := reader.ReadString('\n'); waitErr != nil {
		releaseQuietly(lock)
		os.Exit(0)
	}
	releaseQuietly(lock)
	os.Exit(0)
}

func writeQuietly(w io.Writer, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		return
	}
}

func closeTestDB(t *testing.T, closer io.Closer) {
	t.Helper()
	t.Cleanup(func() {
		closeQuietly(closer)
	})
}
