package client_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/pkg/client"
)

func TestBackgroundPreferenceAndSingleOwner(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	assertBackgroundPreference(t, dir, false)
	mustClientTest(t, client.SetBackgroundEnabled(dir, true))
	assertBackgroundPreference(t, dir, true)
	first := acquireBackground(t, dir)
	second, err := client.TryBackgroundLock(dir)
	if !errors.Is(err, client.ErrBackgroundOwned) || second != nil {
		t.Fatalf("second owner: %v, %v", second, err)
	}
	mustClientTest(t, first.Close())
	third := acquireBackground(t, dir)
	mustClientTest(t, third.Close())
	mustClientTest(t, client.SetBackgroundEnabled(dir, false))
	assertBackgroundPreference(t, dir, false)
}

func mustClientTest(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func assertBackgroundPreference(t *testing.T, dir string, want bool) {
	t.Helper()
	enabled, err := client.BackgroundEnabled(dir)
	if err != nil || enabled != want {
		t.Fatalf("preference = %v, %v; want %v", enabled, err, want)
	}
}

func acquireBackground(t *testing.T, dir string) *client.BackgroundLock {
	t.Helper()
	lock, err := client.TryBackgroundLock(dir)
	if err != nil || lock == nil {
		t.Fatalf("acquire background: %v", err)
	}
	return lock
}

func TestBackgroundRejectsUnsafePreferenceAndLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	mustClientTest(t, client.SetBackgroundEnabled(dir, true))
	mustClientTest(t, client.SetBackgroundEnabled(dir, true))
	marker := filepath.Join(dir, "background.enabled")
	testSetMode(t, marker, 0o666)
	if _, err := client.BackgroundEnabled(dir); err == nil {
		t.Fatal("unsafe preference accepted")
	}
	if err := client.SetBackgroundEnabled(dir, true); err == nil {
		t.Fatal("unsafe existing preference accepted")
	}
	mustClientTest(t, client.SetBackgroundEnabled(dir, false))
	mustClientTest(t, client.SetBackgroundEnabled(dir, false))
	lockPath := filepath.Join(dir, "background.lock")
	mustClientTest(t, os.WriteFile(lockPath, nil, 0o666))
	testSetMode(t, lockPath, 0o666)
	if _, err := client.TryBackgroundLock(dir); err == nil {
		t.Fatal("unsafe lock accepted")
	}
	testSetMode(t, dir, 0o777)
	if _, err := client.BackgroundEnabled(dir); err == nil {
		t.Fatal("unsafe data directory accepted")
	}
	if err := client.SetBackgroundEnabled(dir, true); err == nil {
		t.Fatal("unsafe data directory accepted")
	}
	if _, err := client.TryBackgroundLock(dir); err == nil {
		t.Fatal("unsafe data directory accepted")
	}
}

func TestBackgroundRejectsWrongFileTypes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testSetMode(t, dir, 0o700)
	marker := filepath.Join(dir, "background.enabled")
	mustClientTest(t, os.Mkdir(marker, 0o700))
	mustClientTest(t, os.WriteFile(filepath.Join(marker, "child"), nil, 0o600))
	if err := client.SetBackgroundEnabled(dir, false); err == nil {
		t.Fatal("nonempty preference directory removed")
	}
	lock := filepath.Join(dir, "background.lock")
	mustClientTest(t, os.Mkdir(lock, 0o700))
	if _, err := client.TryBackgroundLock(dir); err == nil {
		t.Fatal("directory accepted as lock")
	}
	file := filepath.Join(dir, "file")
	mustClientTest(t, os.WriteFile(file, nil, 0o600))
	if _, err := client.BackgroundEnabled(file); err == nil {
		t.Fatal("file accepted as data directory")
	}
}
