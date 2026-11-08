package client_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/pkg/client"
)

// TestReadHostFileUnreadable pins that an unreadable discovery file is a read
// failure, not "no host".
func TestReadHostFileUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	if err := os.WriteFile(path, []byte(`{"pid":1,"socket_path":"/s"}`), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeQuietly(dir) })
	if _, err := client.ReadHostFile(dir); err == nil || errors.Is(err, client.ErrHostNotRunning) {
		t.Errorf("unreadable file: err = %v, want a read error that is not ErrHostNotRunning", err)
	}
}

// Test_DataDirPinsTheHostRule pins the resolution rule the host's
// internal/config applies: the SYNX_FUGUE_DATA_DIR override when set (it must
// be absolute), else <user config dir>/Fugue, with the failure shapes of
// both. The lookup and the user-config-dir resolver come in as parameters, so
// the client never reads the process environment implicitly.
func Test_DataDirPinsTheHostRule(t *testing.T) {
	t.Parallel()
	const testConfigDir = "/cfg"
	lookup := func(string) (string, bool) { return "", false }
	dir, err := client.DataDir(lookup, func() (string, error) { return testConfigDir, nil })
	if err != nil || dir != filepath.Join(testConfigDir, "Fugue") {
		t.Errorf("DataDir without override = %q, %v, want %s", dir, err, filepath.Join(testConfigDir, "Fugue"))
	}
	dir, err = client.DataDir(func(k string) (string, bool) {
		if k != client.EnvDataDir {
			t.Errorf("lookup key = %q, want %s", k, client.EnvDataDir)
		}
		return "/data/fugue", true
	}, func() (string, error) { return testConfigDir, nil })
	if err != nil || dir != "/data/fugue" {
		t.Errorf("DataDir with override = %q, %v, want /data/fugue", dir, err)
	}
	// A relative override is rejected with the typed cause, never resolved.
	_, err = client.DataDir(func(string) (string, bool) { return "relative/fugue", true },
		func() (string, error) { return "/cfg", nil })
	if !errors.Is(err, client.ErrDataDirNotAbsolute) {
		t.Errorf("relative override: err = %v, want ErrDataDirNotAbsolute", err)
	}
	// An empty override counts as set and fails the same rule.
	_, err = client.DataDir(func(string) (string, bool) { return "", true },
		func() (string, error) { return testConfigDir, nil })
	if !errors.Is(err, client.ErrDataDirNotAbsolute) {
		t.Errorf("empty override: err = %v, want ErrDataDirNotAbsolute", err)
	}
	// An unresolvable user config dir is a failure, never a guess.
	_, err = client.DataDir(lookup, func() (string, error) { return "", errNoConfigDir })
	if err == nil {
		t.Error("DataDir succeeded without a user config dir")
	}
}
