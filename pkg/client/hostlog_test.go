package client_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/syurodev/fugue/pkg/client"
)

func TestHostLogPrivateBoundedAndTruncated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	log, err := client.OpenHostLog(dir)
	mustClientTest(t, err)
	info, err := log.Stat()
	mustClientTest(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode: %o", info.Mode().Perm())
	}
	input := strings.NewReader("abcdefgh")
	mustClientTest(t, client.WriteBoundedLog(log, input, 4))
	if input.Len() != 0 {
		t.Fatal("host output was not drained")
	}
	mustClientTest(t, log.Close())
	path := filepath.Join(dir, "logs", "host.log")
	root, err := os.OpenRoot(dir)
	mustClientTest(t, err)
	defer closeQuietly(root)
	data, err := root.ReadFile("logs/host.log")
	mustClientTest(t, err)
	if string(data) != "abcd" {
		t.Fatalf("log = %q", data)
	}
	log, err = client.OpenHostLog(dir)
	mustClientTest(t, err)
	mustClientTest(t, log.Close())
	data, err = root.ReadFile("logs/host.log")
	mustClientTest(t, err)
	if len(data) != 0 {
		t.Fatalf("log was not truncated: %q", data)
	}
	testSetMode(t, path, 0o666)
	if _, err = client.OpenHostLog(dir); err == nil {
		t.Fatal("unsafe log accepted")
	}
	testSetMode(t, filepath.Join(dir, "logs"), 0o777)
	if _, err = client.OpenHostLog(dir); err == nil {
		t.Fatal("unsafe log directory accepted")
	}
}

type rejectLogWriter struct{}

func (rejectLogWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestHostLogDrainsAfterWriteFailure(t *testing.T) {
	t.Parallel()
	input := strings.NewReader("secret data")
	if err := client.WriteBoundedLog(rejectLogWriter{}, input, 5); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error: %v", err)
	}
	if input.Len() != 0 {
		t.Fatal("host output was not drained")
	}
	var output bytes.Buffer
	mustClientTest(t, client.WriteBoundedLog(&output, strings.NewReader("abc"), 9))
	if output.String() != "abc" {
		t.Fatal("short output changed")
	}
}

func TestConcurrentHostLogCreation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	for i := range 16 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			f, err := client.OpenHostLog(dir)
			if err != nil {
				t.Fatal(err)
			}
			mustClientTest(t, f.Close())
		})
	}
}

func TestHostLogRejectsNonDirectoriesAndFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	file := filepath.Join(dir, "file")
	mustClientTest(t, os.WriteFile(file, nil, 0o600))
	if _, err := client.OpenHostLog(file); err == nil {
		t.Fatal("file accepted as data directory")
	}
	logs := filepath.Join(dir, "logs")
	mustClientTest(t, os.WriteFile(logs, nil, 0o600))
	if _, err := client.OpenHostLog(dir); err == nil {
		t.Fatal("file accepted as log directory")
	}
	mustClientTest(t, os.Remove(logs))
	mustClientTest(t, os.Mkdir(logs, 0o700))
	mustClientTest(t, os.Mkdir(filepath.Join(logs, "host.log"), 0o700))
	if _, err := client.OpenHostLog(dir); err == nil {
		t.Fatal("directory accepted as log file")
	}
}
