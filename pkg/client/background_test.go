package client_test

import (
	"errors"
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
