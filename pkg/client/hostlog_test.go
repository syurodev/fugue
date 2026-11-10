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

func TestHostLogPreservesPreviousLaunch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	first, err := client.OpenHostLog(dir)
	mustClientTest(t, err)
	info, err := first.Stat()
	mustClientTest(t, err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode: %o", info.Mode().Perm())
	}
	input := strings.NewReader("abcdefgh")
	mustClientTest(t, client.WriteBoundedLog(first, input, 4))
	if input.Len() != 0 {
		t.Fatal("host output was not drained")
	}
	mustClientTest(t, first.Close())
	second, err := client.OpenHostLog(dir)
	mustClientTest(t, err)
	mustClientTest(t, second.Close())
	data, err := os.ReadFile(first.Name())
	mustClientTest(t, err)
	if string(data) != "abcd" {
		t.Fatalf("first log = %q", data)
	}
	if first.Name() == second.Name() {
		t.Fatal("launches share a log file")
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
	names := make(chan string, 8)
	t.Cleanup(func() {
		close(names)
		seen := map[string]bool{}
		for name := range names {
			if seen[name] {
				t.Errorf("duplicate log %s", name)
			}
			seen[name] = true
		}
		if len(seen) != 8 {
			t.Errorf("got %d logs", len(seen))
		}
	})
	for i := range 8 {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			f, err := client.OpenHostLog(dir)
			mustClientTest(t, err)
			names <- f.Name()
			mustClientTest(t, f.Close())
		})
	}
}

func TestHostLogPrunesOldRegularFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	logs := filepath.Join(dir, "logs")
	mustClientTest(t, os.Mkdir(logs, 0o700))
	mustClientTest(t, os.WriteFile(filepath.Join(logs, "notes.txt"), []byte("keep"), 0o600))
	mustClientTest(t, os.Symlink("notes.txt", filepath.Join(logs, "host-99.log")))
	for range 7 {
		f, err := client.OpenHostLog(dir)
		mustClientTest(t, err)
		mustClientTest(t, f.Close())
	}
	entries, err := os.ReadDir(logs)
	mustClientTest(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := "host-3.log,host-4.log,host-5.log,host-6.log,host-7.log,host-99.log,notes.txt"
	if strings.Join(names, ",") != want {
		t.Fatalf("entries = %v", names)
	}
}

func TestHostLogLeavesLegacyLogUntouched(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	logs := filepath.Join(dir, "logs")
	mustClientTest(t, os.Mkdir(logs, 0o700))
	path := filepath.Join(logs, "host.log")
	mustClientTest(t, os.WriteFile(path, []byte("legacy"), 0o600))
	f, err := client.OpenHostLog(dir)
	mustClientTest(t, err)
	mustClientTest(t, f.Close())
	root, err := os.OpenRoot(dir)
	mustClientTest(t, err)
	defer closeQuietly(root)
	data, err := root.ReadFile(filepath.Join("logs", filepath.Base(path)))
	mustClientTest(t, err)
	if string(data) != "legacy" {
		t.Fatalf("legacy log changed: %q", data)
	}
}

func TestHostLogDoesNotReuseInsecureFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	logs := filepath.Join(dir, "logs")
	mustClientTest(t, os.Mkdir(logs, 0o700))
	path := filepath.Join(logs, "host-1.log")
	mustClientTest(t, os.WriteFile(path, []byte("old"), 0o600))
	testSetMode(t, path, 0o644)
	f, err := client.OpenHostLog(dir)
	mustClientTest(t, err)
	mustClientTest(t, f.Close())
	if f.Name() == path {
		t.Fatal("reused insecure file")
	}
	root, err := os.OpenRoot(dir)
	mustClientTest(t, err)
	defer closeQuietly(root)
	data, err := root.ReadFile(filepath.Join("logs", filepath.Base(path)))
	mustClientTest(t, err)
	if string(data) != "old" {
		t.Fatalf("existing file changed: %q", data)
	}
}

func TestHostLogRejectsNonDirectories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	file := filepath.Join(dir, "file")
	mustClientTest(t, os.WriteFile(file, nil, 0o600))
	if _, err := client.OpenHostLog(file); err == nil {
		t.Fatal("file accepted as data directory")
	}
	mustClientTest(t, os.WriteFile(filepath.Join(dir, "logs"), nil, 0o600))
	if _, err := client.OpenHostLog(dir); err == nil {
		t.Fatal("file accepted as log directory")
	}
}

func TestHostLogRetriesOccupiedNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	logs := filepath.Join(dir, "logs")
	mustClientTest(t, os.Mkdir(logs, 0o700))
	for _, name := range []string{"host-1.log", "host-2.log"} {
		mustClientTest(t, os.Symlink("missing", filepath.Join(logs, name)))
	}
	for _, name := range []string{"host-.log", "host-a.log", "host-3.txt", "host-1x.log"} {
		mustClientTest(t, os.WriteFile(filepath.Join(logs, name), nil, 0o600))
	}
	f, err := client.OpenHostLog(dir)
	mustClientTest(t, err)
	mustClientTest(t, f.Close())
	if filepath.Base(f.Name()) != "host-3.log" {
		t.Fatalf("allocated %s", f.Name())
	}
}

func TestHostLogReportsExhaustedSequence(t *testing.T) {
	t.Parallel()
	for _, number := range []string{"18446744073709551615"} {
		t.Run(number, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			testSetMode(t, dir, 0o700)
			logs := filepath.Join(dir, "logs")
			mustClientTest(t, os.Mkdir(logs, 0o700))
			mustClientTest(t, os.WriteFile(filepath.Join(logs, "host-"+number+".log"), nil, 0o600))
			if _, err := client.OpenHostLog(dir); err == nil {
				t.Fatal("exhausted sequence accepted")
			}
		})
	}
}

func TestHostLogReportsRetryLimit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	logs := filepath.Join(dir, "logs")
	mustClientTest(t, os.Mkdir(logs, 0o700))
	for i := 1; i <= 1024; i++ {
		mustClientTest(t, os.Symlink("missing", filepath.Join(logs, "host-"+strconv.Itoa(i)+".log")))
	}
	if _, err := client.OpenHostLog(dir); err == nil || !strings.Contains(err.Error(), "retry limit") {
		t.Fatalf("retry limit error = %v", err)
	}
}

func TestHostLogRejectsNonPrivateMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o500)
	if _, err := client.OpenHostLog(dir); err == nil {
		t.Fatal("directory with incorrect owner permissions accepted")
	}
}

func TestHostLogRetainsNumericallyNewestWithLeadingZeros(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	logs := filepath.Join(dir, "logs")
	mustClientTest(t, os.Mkdir(logs, 0o700))
	for _, name := range []string{"host-1.log", "host-01.log"} {
		mustClientTest(t, os.WriteFile(filepath.Join(logs, name), nil, 0o600))
	}
	f, err := client.OpenHostLog(dir)
	mustClientTest(t, err)
	mustClientTest(t, f.Close())
	if filepath.Base(f.Name()) != "host-2.log" {
		t.Fatalf("allocated %s", f.Name())
	}
}

func TestOpenHostLogIgnoresOverflowingSequenceNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	logs := filepath.Join(dir, "logs")
	mustClientTest(t, os.Mkdir(logs, 0o700))
	for _, name := range []string{"host-10.log", "host-20.log", "host-30.log", "host-40.log"} {
		mustClientTest(t, os.WriteFile(filepath.Join(logs, name), nil, 0o600))
	}
	junk := filepath.Join(logs, "host-99999999999999999999.log")
	mustClientTest(t, os.WriteFile(junk, []byte("keep"), 0o600))
	f, err := client.OpenHostLog(dir)
	mustClientTest(t, err)
	mustClientTest(t, f.Close())
	if filepath.Base(f.Name()) != "host-41.log" {
		t.Fatalf("allocated %s", f.Name())
	}
	root, err := os.OpenRoot(logs)
	mustClientTest(t, err)
	defer closeQuietly(root)
	data, err := root.ReadFile(filepath.Base(junk))
	mustClientTest(t, err)
	if string(data) != "keep" {
		t.Fatalf("junk changed: %q", data)
	}
	entries, err := os.ReadDir(logs)
	mustClientTest(t, err)
	if len(entries) != 6 {
		t.Fatalf("got %d entries, want five logs and junk", len(entries))
	}
}
