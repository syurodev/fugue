package hostapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func testHostFile(pid int) HostFile {
	return HostFile{
		PID:         pid,
		StartedAt:   time.Date(2026, 11, 6, 9, 30, 0, 0, time.UTC),
		SocketPath:  filepath.Join(string(filepath.Separator), "run", "user", "1000", "fugue", "fugue-1a2b3c4d5e6f.sock"),
		APIVersions: []string{"v1"},
		HostVersion: "test-9",
	}
}

func TestHostFileIsWrittenAtomicallyWithOwnerOnlyMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	hf := testHostFile(4242)
	if err := WriteHostFile(dir, &hf); err != nil {
		t.Fatalf("WriteHostFile = %v", err)
	}
	fi, err := os.Stat(HostFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("host file mode = %v, want 0600", fi.Mode())
	}
	b, err := os.ReadFile(HostFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var got HostFile
	if uerr := json.Unmarshal(b, &got); uerr != nil {
		t.Fatal(uerr)
	}
	if diff := cmp.Diff(hf, got); diff != "" {
		t.Fatalf("host file content mismatch (-want +got):\n%s", diff)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("data dir has %d entries after the write, want only host.json", len(entries))
	}
}

func TestHostFileFromCrashedHostIsReplacedOnStart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	stale := testHostFile(999999)
	if err := WriteHostFile(dir, &stale); err != nil {
		t.Fatal(err)
	}
	fresh := testHostFile(1)
	fresh.HostVersion = "test-10"
	if err := WriteHostFile(dir, &fresh); err != nil {
		t.Fatalf("WriteHostFile over a stale file = %v", err)
	}
	b, err := os.ReadFile(HostFilePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var got HostFile
	if uerr := json.Unmarshal(b, &got); uerr != nil {
		t.Fatal(uerr)
	}
	if diff := cmp.Diff(fresh, got); diff != "" {
		t.Fatalf("stale host file was not replaced (-want +got):\n%s", diff)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("data dir has %d entries after the replace, want only host.json", len(entries))
	}
}

func TestRemoveHostFileToleratesAbsentFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := RemoveHostFile(dir); err != nil {
		t.Fatalf("RemoveHostFile on an empty dir = %v, want nil", err)
	}
	fresh := testHostFile(4242)
	if err := WriteHostFile(dir, &fresh); err != nil {
		t.Fatal(err)
	}
	if err := RemoveHostFile(dir); err != nil {
		t.Fatalf("RemoveHostFile = %v, want nil", err)
	}
	if _, err := os.Stat(HostFilePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("host file still present after RemoveHostFile: %v", err)
	}
}
