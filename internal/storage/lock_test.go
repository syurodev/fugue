package storage

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

func waitCmdQuietly(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if err := cmd.Wait(); err != nil {
		return
	}
}

func killProcessQuietly(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := cmd.Process.Kill(); err != nil {
		return
	}
}

func spawnHoldLockProcess(t *testing.T, dataDir string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), "go")
	cmd.Path = exe
	cmd.Args = []string{exe, holdLockArgFlag}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("Start helper process: %v", startErr)
	}
	if _, writeErr := io.WriteString(stdin, dataDir+"\n"); writeErr != nil {
		killProcessQuietly(cmd)
		waitCmdQuietly(cmd)
		t.Fatalf("write dataDir to helper stdin: %v", writeErr)
	}
	reader := bufio.NewReader(stdout)
	line, readErr := reader.ReadString('\n')
	if readErr != nil || strings.TrimSpace(line) != "LOCKED" {
		killProcessQuietly(cmd)
		waitCmdQuietly(cmd)
		t.Fatalf("helper failed to lock (%v): stdout=%q stderr=%q", readErr, line, stderr.String())
	}
	return cmd, stdin
}

func TestTwoProcessesCannotHoldTheDataDirectoryLock(t *testing.T) {
	t.Parallel()
	dataDir := filepath.Join(t.TempDir(), "Fugue")

	cmd, stdin := spawnHoldLockProcess(t, dataDir)
	t.Cleanup(func() {
		closeQuietly(stdin)
		waitCmdQuietly(cmd)
	})

	_, err := AcquireLock(dataDir)
	if !errors.Is(err, ErrDataDirLocked) || errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("AcquireLock while held by child = %v (code %v), want %v (%v)", err, errcode.CodeOf(err), ErrDataDirLocked, errcode.FailedPrecondition)
	}

	// Tell the child process to release cleanly and exit.
	if _, writeErr := stdin.Write([]byte("\n")); writeErr != nil {
		t.Fatalf("write to child stdin: %v", writeErr)
	}
	closeQuietly(stdin)
	if waitErr := cmd.Wait(); waitErr != nil {
		t.Fatalf("child exit: %v", waitErr)
	}

	lock, err := AcquireLock(dataDir)
	if err != nil {
		t.Fatalf("AcquireLock after child exited: %v", err)
	}
	if releaseErr := lock.Release(); releaseErr != nil {
		t.Fatalf("Release: %v", releaseErr)
	}
}

func TestPathAliasesAndSymlinksContendOnTheSameLock(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realDir := filepath.Join(base, "real", "Fugue")
	if err := os.MkdirAll(realDir, dataDirPerm); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	symlinkDir := filepath.Join(base, "alias-link")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	dotDotDir := filepath.Join(realDir, "..", "Fugue")

	lock, err := AcquireLock(realDir)
	if err != nil {
		t.Fatalf("AcquireLock(realDir): %v", err)
	}
	t.Cleanup(func() { releaseQuietly(lock) })

	if _, aliasErr := AcquireLock(symlinkDir); !errors.Is(aliasErr, ErrDataDirLocked) {
		t.Fatalf("AcquireLock(symlinkDir) = %v, want %v", aliasErr, ErrDataDirLocked)
	}
	if _, relErr := AcquireLock(dotDotDir); !errors.Is(relErr, ErrDataDirLocked) {
		t.Fatalf("AcquireLock(dotDotDir) = %v, want %v", relErr, ErrDataDirLocked)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(lock.Path())
		if statErr != nil {
			t.Fatalf("Stat(%s): %v", lock.Path(), statErr)
		}
		if got, want := info.Mode().Perm(), os.FileMode(dbFilePerm); got != want {
			t.Fatalf("lock file permissions = %o, want %o", got, want)
		}
	}
}

func TestCrashReleasesTheLockAndAdvancesHostIncarnation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "Fugue")
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	idGen := ids.NewSequence("host")

	store1, err := OpenStore(ctx, config.Config{DataDir: dataDir, LogLevel: slog.LevelInfo}, clk, idGen)
	if err != nil {
		t.Fatalf("OpenStore #1: %v", err)
	}
	if got := store1.Incarnation().LockGeneration; got != 1 {
		closeQuietly(store1)
		t.Fatalf("store1 LockGeneration = %d, want 1", got)
	}
	if closeErr := store1.Close(); closeErr != nil {
		t.Fatalf("store1.Close: %v", closeErr)
	}

	// Spawn a child process that acquires fugue.lock, then kill it abruptly (simulating a crash).
	cmd, stdin := spawnHoldLockProcess(t, dataDir)
	t.Cleanup(func() { closeQuietly(stdin) })
	if killErr := cmd.Process.Kill(); killErr != nil {
		t.Fatalf("Kill child process: %v", killErr)
	}
	waitCmdQuietly(cmd)

	if _, statErr := os.Stat(LockPath(dataDir)); statErr != nil {
		t.Fatalf("fugue.lock should remain on disk after crash: %v", statErr)
	}
	clk.Advance(time.Minute)
	store2, err := OpenStore(ctx, config.Config{DataDir: dataDir, LogLevel: slog.LevelInfo}, clk, idGen)
	if err != nil {
		t.Fatalf("OpenStore #2 after crash: %v", err)
	}
	closeTestDB(t, store2)

	if got := store2.Incarnation().LockGeneration; got != 2 {
		t.Fatalf("store2 LockGeneration = %d, want 2", got)
	}
	if store2.Incarnation().IncarnationID == store1.Incarnation().IncarnationID {
		t.Fatalf("store2 IncarnationID = %q, must differ from store1", store2.Incarnation().IncarnationID)
	}
	current, err := CurrentHostIncarnation(ctx, store2.DB())
	if err != nil {
		t.Fatalf("CurrentHostIncarnation: %v", err)
	}
	if current != store2.Incarnation() {
		t.Fatalf("CurrentHostIncarnation = %+v, want %+v", current, store2.Incarnation())
	}
}

func TestNetworkFilesystemIsRejected(t *testing.T) {
	t.Parallel()
	dataDir := filepath.Join(t.TempDir(), "Fugue")

	_, err := AcquireLockWith(dataDir, &LockOptions{
		CheckFilesystem: func(canonicalDir string) error {
			return errcode.New(errcode.FailedPrecondition, "network filesystem nfs at "+canonicalDir)
		},
	})
	if errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("AcquireLockWith(network FS) code = %v, want %v", errcode.CodeOf(err), errcode.FailedPrecondition)
	}

	for _, fsName := range []string{"nfs", "nfs4", "smbfs", "cifs", "smb2", "afpfs", "webdav", "ceph", "lustre", "gpfs"} {
		if !isNetworkFSTypeName(fsName) {
			t.Fatalf("isNetworkFSTypeName(%q) = false, want true", fsName)
		}
	}
	if isNetworkFSTypeName("ext4") || isNetworkFSTypeName("apfs") {
		t.Fatal("local filesystems must not be classified as network filesystems")
	}

	for _, magic := range []int64{0x6969, 0x6e667364, 0x517b, 0xff534d42, 0xfe534d42, 0x5346414f, 0x00c36400, 0x47504653, 0x0bd00bd0, 0x73757245, 0x19830326} {
		if name, ok := isLinuxNetworkFSMagic(magic); !ok || name == "" {
			t.Fatalf("isLinuxNetworkFSMagic(0x%x) = (%q, %v), want non-empty, true", magic, name, ok)
		}
	}
	if _, ok := isLinuxNetworkFSMagic(0xef53); ok {
		t.Fatal("ext4 magic 0xef53 must not be classified as network filesystem")
	}
}

func wantErrCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if got := errcode.CodeOf(err); got != want {
		t.Fatalf("error code = %v (%v), want %v", got, err, want)
	}
}

func TestAcquireHostIncarnationRequiresHeldLockAndMonotonicallyAdvances(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "Fugue")
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	idGen := ids.NewSequence("host")

	lock, err := AcquireLock(dataDir)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	t.Cleanup(func() { releaseQuietly(lock) })

	db, err := OpenDataDir(ctx, lock.DataDir())
	if err != nil {
		t.Fatalf("OpenDataDir: %v", err)
	}
	closeTestDB(t, db)
	if migErr := Migrate(ctx, db, clk); migErr != nil {
		t.Fatalf("Migrate: %v", migErr)
	}

	_, curErr := CurrentHostIncarnation(ctx, nil)
	wantErrCode(t, curErr, errcode.InvalidArgument)
	_, curErr = CurrentHostIncarnation(ctx, db)
	wantErrCode(t, curErr, errcode.NotFound)

	wantErrCode(t, validateIncarnationArgs(nil, lock, clk, idGen), errcode.InvalidArgument)
	wantErrCode(t, validateIncarnationArgs(db, lock, nil, idGen), errcode.InvalidArgument)
	wantErrCode(t, validateIncarnationArgs(db, lock, clk, nil), errcode.InvalidArgument)
	wantErrCode(t, validateIncarnationArgs(db, nil, clk, idGen), errcode.FailedPrecondition)

	inc1, err := AcquireHostIncarnation(ctx, db, lock, clk, idGen)
	if err != nil || inc1.LockGeneration != 1 {
		t.Fatalf("AcquireHostIncarnation #1 = %+v, %v, want gen 1", inc1, err)
	}

	if relErr := lock.Release(); relErr != nil {
		t.Fatalf("lock.Release: %v", relErr)
	}
	_, acqErr := AcquireHostIncarnation(ctx, db, lock, clk, idGen)
	wantErrCode(t, acqErr, errcode.FailedPrecondition)
}

type failingIDGen struct{}

func (failingIDGen) NewID() (string, error) {
	return "", errChmodInjected
}

func TestOpenStoreLifecycleAndEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dataDir := filepath.Join(t.TempDir(), "Fugue")
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	idGen := ids.NewSequence("host")

	_, blankErr := AcquireLock("   ")
	wantErrCode(t, blankErr, errcode.InvalidArgument)
	assertNilLockAndStoreZeroValues(t)
	releaseQuietly(nil)
	closeQuietly(nil)
	rollbackQuietly(nil)

	_, failGenErr := OpenStore(ctx, config.Config{DataDir: dataDir, LogLevel: slog.LevelInfo}, clk, failingIDGen{})
	wantErrCode(t, failGenErr, errcode.Internal)

	store, err := OpenStore(ctx, config.Config{DataDir: dataDir, LogLevel: slog.LevelInfo}, clk, idGen)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if store.DB() == nil || store.Lock() == nil || !store.Lock().Held() || store.DataDir() == "" {
		closeQuietly(store)
		t.Fatal("OpenStore returned incomplete Store")
	}
	if _, execErr := store.DB().ExecContext(ctx, "UPDATE exec_host SET acquired_at = 'not-a-time' WHERE singleton = 1"); execErr != nil {
		closeQuietly(store)
		t.Fatalf("corrupt acquired_at: %v", execErr)
	}
	_, badTimeErr := CurrentHostIncarnation(ctx, store.DB())
	wantErrCode(t, badTimeErr, errcode.Internal)

	if closeErr := store.Close(); closeErr != nil {
		t.Fatalf("store.Close: %v", closeErr)
	}
	if closeErr := store.Close(); closeErr != nil {
		t.Fatalf("store.Close (second call): %v", closeErr)
	}
}

func assertNilLockAndStoreZeroValues(t *testing.T) {
	t.Helper()
	var nilLock *DirLock
	if nilLock.DataDir() != "" || nilLock.Path() != "" || nilLock.Held() {
		t.Fatal("nil DirLock accessors must return zero values")
	}
	if err := nilLock.Release(); err != nil {
		t.Fatalf("nilLock.Release: %v", err)
	}

	var nilStore *Store
	if nilStore.DB() != nil || nilStore.Lock() != nil || nilStore.DataDir() != "" {
		t.Fatal("nil Store accessors must return zero values")
	}
	if nilStore.Incarnation() != (HostIncarnation{}) {
		t.Fatal("nil Store.Incarnation must return zero value")
	}
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nilStore.Close: %v", err)
	}
}
