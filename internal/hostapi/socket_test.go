package hostapi

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// shortDir creates a directory whose path is short enough to bind a unix
// socket, which t.TempDir() cannot promise on macOS: sun_path holds 104 bytes
// and macOS test temp dirs already exceed 100. The per-test name is hashed so
// parallel tests never share a directory.
func shortDir(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("fugue-%x", sum[:4]))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeDirOps is a DirOps seam whose directories record every open and chmod,
// so the tests can pin that the trust check and the chmod act on one open
// descriptor and that an untrusted level stops the walk before the next one.
type fakeDirOps struct {
	dirs  map[string]*fakeDir
	opens []string
}

type fakeDir struct {
	path   string
	stat   DirStat
	chmods int
}

func newFakeDirOps() *fakeDirOps {
	return &fakeDirOps{dirs: map[string]*fakeDir{}}
}

func (f *fakeDirOps) add(path string, st DirStat) *fakeDir {
	d := &fakeDir{path: path, stat: st}
	f.dirs[path] = d
	return d
}

func (f *fakeDirOps) Open(path string) (Dir, error) {
	f.opens = append(f.opens, path)
	d, ok := f.dirs[path]
	if !ok {
		return nil, errcode.New(errcode.NotFound, "no fake dir registered for "+path)
	}
	return d, nil
}

func (f *fakeDirOps) opened(path string) bool {
	for _, p := range f.opens {
		if p == path {
			return true
		}
	}
	return false
}

func (f *fakeDirOps) chmodCount() int {
	n := 0
	for _, d := range f.dirs {
		n += d.chmods
	}
	return n
}

func (d *fakeDir) Stat() (DirStat, error) { return d.stat, nil }

func (d *fakeDir) Chmod(mode fs.FileMode) error {
	d.chmods++
	d.stat.Mode = (d.stat.Mode &^ fs.FileMode(0o777)) | mode.Perm()
	return nil
}

func (d *fakeDir) Close() error { return nil }

func dirStat(perm fs.FileMode, uid uint32, known bool) DirStat {
	return DirStat{Mode: fs.ModeDir | perm, UID: uid, UIDKnown: known}
}

// listenFor closes the listener when the test ends and fails on a bind error.
func listenFor(t *testing.T, opts SocketOptions) *Socket {
	t.Helper()
	s, err := ListenSocket(opts)
	if err != nil {
		t.Fatalf("ListenSocket = %v, want nil", err)
	}
	t.Cleanup(func() { _ = s.Listener.Close() })
	return s
}

func TestSocketDirectoryAndSocketAreOwnerOnly(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	s := listenFor(t, SocketOptions{DataDir: dir})
	if got, err := os.Stat(dir); err != nil || got.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode = %v (%v), want 0700", got.Mode(), err)
	}
	fi, err := os.Lstat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 || fi.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("socket mode = %v, want 0600 socket", fi.Mode())
	}
}

func TestShortSocketPathStaysInTheDataDirectory(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	s := listenFor(t, SocketOptions{DataDir: dir})
	if want := filepath.Join(dir, SocketFileName); s.Path != want {
		t.Fatalf("socket path = %q, want %q", s.Path, want)
	}
}

func TestLongSocketPathFallsBackToTheRuntimeDirectory(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	base := filepath.Join(shortDir(t), "rt")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	s := listenFor(t, SocketOptions{DataDir: dir, RuntimeDir: base, MaxPath: 8})
	want := filepath.Join(base, runtimeSocketDirName, hashedSocketName(dir))
	if s.Path != want {
		t.Fatalf("socket path = %q, want %q", s.Path, want)
	}
}

func TestLongSocketPathNeedsARuntimeDir(t *testing.T) {
	t.Parallel()
	_, err := ListenSocket(SocketOptions{DataDir: shortDir(t), MaxPath: 8})
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("ListenSocket = %v, want invalid_argument", err)
	}
}

func TestSocketDirectorySymlinkIsRejected(t *testing.T) {
	t.Parallel()
	real := shortDir(t)
	dir := shortDir(t)
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	_, err := ListenSocket(SocketOptions{DataDir: link})
	if !errors.Is(err, ErrSocketDirectoryUntrusted) || errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("ListenSocket = %v, want %v with permission_denied", err, ErrSocketDirectoryUntrusted)
	}
}

func TestSocketDirectoryForeignOwnerIsRejected(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	ops := newFakeDirOps()
	ops.add(dir, dirStat(0o700, uint32(os.Getuid())+4242, true))
	_, err := ListenSocket(SocketOptions{DataDir: dir, Dirs: ops})
	if !errors.Is(err, ErrSocketDirectoryUntrusted) {
		t.Fatalf("ListenSocket = %v, want %v", err, ErrSocketDirectoryUntrusted)
	}
	if n := ops.chmodCount(); n != 0 {
		t.Fatalf("a foreign-owned directory was chmod'ed %d times", n)
	}
}

func TestSocketDirectoryUnknownOwnerFailsClosed(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	ops := newFakeDirOps()
	ops.add(dir, dirStat(0o700, uint32(os.Getuid()), false))
	_, err := ListenSocket(SocketOptions{DataDir: dir, Dirs: ops})
	if !errors.Is(err, ErrSocketDirectoryUntrusted) {
		t.Fatalf("ListenSocket = %v, want %v", err, ErrSocketDirectoryUntrusted)
	}
	if n := ops.chmodCount(); n != 0 {
		t.Fatalf("a directory with an unknown owner was chmod'ed %d times", n)
	}
}

func TestRuntimeBaseDirectoryForeignOwnerIsRejected(t *testing.T) {
	t.Parallel()
	base := filepath.Join(t.TempDir(), fmt.Sprintf("fugue-%d", os.Getuid()))
	ops := newFakeDirOps()
	ops.add(base, dirStat(0o700, uint32(os.Getuid())+4242, true))
	child := filepath.Join(base, runtimeSocketDirName)
	ops.add(child, dirStat(0o700, uint32(os.Getuid()), true))
	_, err := ListenSocket(SocketOptions{DataDir: t.TempDir(), RuntimeDir: base, MaxPath: 8, Dirs: ops})
	if !errors.Is(err, ErrSocketDirectoryUntrusted) {
		t.Fatalf("ListenSocket = %v, want %v", err, ErrSocketDirectoryUntrusted)
	}
	if ops.opened(child) {
		t.Fatal("the fugue/ child was opened although the runtime base is untrusted")
	}
}

func TestTrustCheckChmodsTheOpenedDescriptor(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	ops := newFakeDirOps()
	d := ops.add(dir, dirStat(0o755, uint32(os.Getuid()), true))
	listenFor(t, SocketOptions{DataDir: dir, Dirs: ops})
	if d.chmods != 1 {
		t.Fatalf("chmod ran %d times on the opened descriptor, want 1", d.chmods)
	}
	if got := d.stat.Mode.Perm(); got != 0o700 {
		t.Fatalf("descriptor mode after chmod = %v, want 0700", got)
	}
}

func TestSocketDirectoryWrongModeIsTightened(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	listenFor(t, SocketOptions{DataDir: dir})
	if got, err := os.Stat(dir); err != nil || got.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode = %v (%v), want tightened 0700", got.Mode(), err)
	}
}

func TestStaleSocketIsReplacedWhenNoOneAnswers(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	path := filepath.Join(dir, SocketFileName)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	s := listenFor(t, SocketOptions{DataDir: dir})
	if s.Path != path {
		t.Fatalf("socket path = %q, want %q", s.Path, path)
	}
	if fi, lerr := os.Lstat(path); lerr != nil || fi.Mode()&fs.ModeSocket == 0 {
		t.Fatalf("rebound path = %v (%v), want a socket", fi, lerr)
	}
}

func TestLiveSocketIsNeverUnlinked(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	path := filepath.Join(dir, SocketFileName)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	_, err = ListenSocket(SocketOptions{DataDir: dir})
	if !errors.Is(err, ErrSocketInUse) || errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("ListenSocket = %v, want %v with failed_precondition", err, ErrSocketInUse)
	}
	if _, lerr := os.Lstat(path); lerr != nil {
		t.Fatalf("the live socket was unlinked: %v", lerr)
	}
	conn, derr := net.Dial("unix", path)
	if derr != nil {
		t.Fatalf("the original listener no longer answers: %v", derr)
	}
	_ = conn.Close()
}

func TestRegularFileAtSocketPathIsNeverUnlinked(t *testing.T) {
	t.Parallel()
	dir := shortDir(t)
	path := filepath.Join(dir, SocketFileName)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ListenSocket(SocketOptions{DataDir: dir})
	if !errors.Is(err, ErrSocketPathNotSocket) {
		t.Fatalf("ListenSocket = %v, want %v", err, ErrSocketPathNotSocket)
	}
	b, rerr := os.ReadFile(path)
	if rerr != nil || string(b) != "keep" {
		t.Fatalf("socket path file = %q (%v), want it untouched", b, rerr)
	}
}
