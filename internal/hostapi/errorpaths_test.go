package hostapi

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func TestPeerCredentialsRejectNonSocketConn(t *testing.T) {
	t.Parallel()
	a, b := net.Pipe()
	t.Cleanup(func() {
		closeQuietly(a)
		closeQuietly(b)
	})
	closeQuietly(nil)
	if _, err := peerCredentials(a); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("peerCredentials(pipe) = %v, want invalid_argument", err)
	}
}

func TestDirDescriptorErrorsAreTyped(t *testing.T) {
	t.Parallel()
	// The parent of the path is missing, so even the os.Root open fails.
	if _, err := (osDirs{}).Open(filepath.Join(shortDir(t), "missing-parent", "child")); err == nil {
		t.Fatal("Open on a missing parent succeeded")
	}
	// A directory descriptor whose file is closed reports typed failures.
	d, err := (osDirs{}).Open(shortDir(t))
	if err != nil {
		t.Fatal(err)
	}
	od, ok := d.(osDir)
	if !ok {
		t.Fatal("Open returned a non-osDir descriptor")
	}
	if err := od.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, serr := od.Stat(); serr == nil || errcode.CodeOf(serr) != errcode.Internal {
		t.Fatalf("Stat on a closed descriptor = %v, want an internal error", serr)
	}
	if cerr := od.Chmod(dirPerm); cerr == nil || errcode.CodeOf(cerr) != errcode.Internal {
		t.Fatalf("Chmod on a closed descriptor = %v, want an internal error", cerr)
	}
	if cerr := od.Close(); cerr == nil || errcode.CodeOf(cerr) != errcode.Internal {
		t.Fatalf("Close on a closed descriptor = %v, want an internal error", cerr)
	}
}

func TestTrustCheckRejectsStatFailureAndNonDirectory(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	ops := newFakeDirOps()
	ops.add(dir, dirStat(0o700, int64(os.Getuid()), true)).failStat = true
	// A stat failing on our own open descriptor is an operational failure, not
	// a trust violation, but it still fails closed: nothing binds.
	if _, err := ListenSocket(t.Context(), SocketOptions{DataDir: dir, Dirs: ops}); errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("ListenSocket with a failing stat = %v, want an internal error", err)
	}

	dir2 := shortDir(t)
	ops2 := newFakeDirOps()
	ops2.add(dir2, DirStat{Mode: 0o600, UID: int64(os.Getuid()), UIDKnown: true})
	if _, err := ListenSocket(t.Context(), SocketOptions{DataDir: dir2, Dirs: ops2}); !errors.Is(err, ErrSocketDirectoryUntrusted) {
		t.Fatalf("ListenSocket on a non-directory = %v, want %v", err, ErrSocketDirectoryUntrusted)
	}
}

func TestMissingParentOfManagedBaseIsAnInternalError(t *testing.T) {
	t.Parallel()
	base := filepath.Join(t.TempDir(), "gone-parent", fmt.Sprintf(runtimeBaseName, os.Getuid()))
	_, err := ListenSocket(t.Context(), SocketOptions{DataDir: t.TempDir(), RuntimeDir: base, MaxPath: 8})
	if errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("ListenSocket = %v, want an internal error for the uncreatable base", err)
	}
}

func TestHostFileFailurePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Renaming over a directory fails; no temp file may survive.
	if err := os.Mkdir(HostFilePath(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	fresh := testHostFile(4242)
	if err := WriteHostFile(dir, &fresh); errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("WriteHostFile over a directory = %v, want an internal error", err)
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(entries) != 1 || entries[0].IsDir() != true {
		t.Fatalf("data dir has %d entries after the failed write, want only the directory placeholder", len(entries))
	}
	// Removing a non-empty directory fails; the placeholder must survive.
	if werr := os.WriteFile(filepath.Join(HostFilePath(dir), "keep"), []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if err := RemoveHostFile(dir); errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("RemoveHostFile on a non-empty directory = %v, want an internal error", err)
	}
	if _, serr := os.Stat(HostFilePath(dir)); serr != nil {
		t.Fatalf("the placeholder directory did not survive: %v", serr)
	}
}
