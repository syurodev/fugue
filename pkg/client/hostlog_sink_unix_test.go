//go:build linux || darwin

package client_test

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/syurodev/fugue/pkg/client"
)

// This test runs before parallel tests because it temporarily redirects stdin and fd 3.
func TestInheritedHostLogSink(t *testing.T) {
	log, err := os.CreateTemp(t.TempDir(), "log")
	mustClientTest(t, err)
	defer closeQuietly(log)
	input, err := os.CreateTemp(t.TempDir(), "input")
	mustClientTest(t, err)
	defer closeQuietly(input)
	_, err = input.WriteString("inherited output")
	mustClientTest(t, err)
	_, err = input.Seek(0, 0)
	mustClientTest(t, err)
	previous, err := unix.Dup(3)
	if err != nil && err != unix.EBADF {
		t.Fatal(err)
	}
	if err == nil {
		defer func() { _ = unix.Dup2(previous, 3); _ = unix.Close(previous) }()
	}
	mustClientTest(t, unix.Dup2(int(log.Fd()), 3))
	stdin := os.Stdin
	os.Stdin = input
	defer func() { os.Stdin = stdin }()
	mustClientTest(t, client.RunHostLogSink())
	data, err := os.ReadFile(log.Name())
	mustClientTest(t, err)
	if string(data) != "inherited output" {
		t.Fatalf("log = %q", data)
	}
}
