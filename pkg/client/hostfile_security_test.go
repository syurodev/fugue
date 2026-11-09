package client_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/pkg/client"
)

func TestReadHostFileRejectsWritableDiscovery(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	path := filepath.Join(dir, "host.json")
	if err := os.WriteFile(path, []byte(`{"socket_path":"/tmp/socket"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	testSetMode(t, path, 0o622)
	if _, err := client.ReadTrustedHostFile(dir); err == nil || errors.Is(err, client.ErrHostNotRunning) {
		t.Fatalf("writable discovery accepted or classified as absent: %v", err)
	}
}

func TestDialTrustedRejectsWritableSocket(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	socket := filepath.Join(dir, "socket")
	listener, err := new(net.ListenConfig).Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeQuietly(listener) })
	testSetMode(t, socket, 0o777)
	_, err = client.DialTrusted(t.Context(), socket, client.Params{Kind: client.KindApp})
	if !errors.Is(err, client.ErrUntrustedHost) {
		t.Fatalf("writable socket: %v", err)
	}
}

func TestDialTrustedAcceptsSameUserHost(t *testing.T) {
	t.Parallel()
	socket := serveHost(t, &fakeHandler{})
	testSetMode(t, filepath.Dir(socket), 0o700)
	testSetMode(t, socket, 0o600)
	conn, err := client.DialTrusted(t.Context(), socket, appParams())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
}

func testSetMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestReadTrustedHostFileRejectsSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(`{"socket_path":"/tmp/socket"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(dir, "host.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadTrustedHostFile(dir); err == nil {
		t.Fatal("symlinked discovery accepted")
	}
}

func TestTrustedDiscoveryParsesAndRejectsInvalidContents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	path := filepath.Join(dir, "host.json")
	if _, err := client.ReadTrustedHostFile(dir); !errors.Is(err, client.ErrHostNotRunning) {
		t.Fatalf("missing discovery: %v", err)
	}
	for _, tc := range []struct {
		content string
		valid   bool
	}{
		{`{"socket_path":"/some/socket"}`, true},
		{`{`, false},
		{`{}`, false},
		{strings.Repeat("a", 65537), false},
	} {
		if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
			t.Fatal(err)
		}
		hf, err := client.ReadTrustedHostFile(dir)
		if tc.valid && (err != nil || hf.SocketPath != "/some/socket") {
			t.Fatalf("valid discovery: %v, %v", hf, err)
		}
		if !tc.valid && err == nil {
			t.Fatal("invalid discovery accepted")
		}
	}
}

func TestTrustedDialRejectsUnsafeOrMissingSocketPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	socket := filepath.Join(dir, "socket")
	_, err := client.DialTrusted(t.Context(), socket, appParams())
	if !errors.Is(err, client.ErrHostNotRunning) {
		t.Fatalf("missing socket: %v", err)
	}
	_, err = client.DialTrusted(t.Context(), filepath.Join(dir, "missing", "socket"), appParams())
	if !errors.Is(err, client.ErrHostNotRunning) {
		t.Fatalf("missing socket directory: %v", err)
	}
	testSetMode(t, dir, 0o777)
	_, err = client.DialTrusted(t.Context(), socket, appParams())
	if !errors.Is(err, client.ErrUntrustedHost) {
		t.Fatalf("unsafe socket directory: %v", err)
	}
	testSetMode(t, dir, 0o700)
	listener, err := new(net.ListenConfig).Listen(t.Context(), "unix", socket)
	mustClientTest(t, err)
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	mustClientTest(t, listener.Close())
	testSetMode(t, socket, 0o600)
	_, err = client.DialTrusted(t.Context(), socket, appParams())
	if !errors.Is(err, client.ErrHostNotRunning) {
		t.Fatalf("stale socket: %v", err)
	}
}
