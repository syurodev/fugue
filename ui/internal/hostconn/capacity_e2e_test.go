package hostconn

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/pkg/client"
)

// FUG-55 (owner decision 2026-11-23): a session the user starts over the
// session cap is refused with vendor.capacity, never queued, and the refusal
// carries the counts the app words its message from. The host is a real
// `fugue serve` capped at one session by its own config.toml.
func TestStartOverTheSessionCapIsRefusedWithCountsOverRealHost(t *testing.T) {
	t.Parallel()
	bin := buildFugue(t)
	ctx := testCtx(t)
	fakeDir := fakeVendorDir(t, fakeVendorVersion, true)

	host := serveCappedHost(t, ctx, bin, fakeDir)
	c := dialApp(t, ctx, host)
	wsID := unlockAndSeedWorkspace(t, ctx, c)

	first, err := c.StartSession(ctx, "fug55-first", client.VendorClaude, wsID, "", client.SessionProfile{})
	if err != nil {
		t.Fatalf("start the first session: %v", err)
	}
	waitSessionState(t, ctx, c, first.ID, stateE2EReady)

	_, err = c.StartSession(ctx, "fug55-second", client.VendorClaude, wsID, "", client.SessionProfile{})
	if err == nil {
		t.Fatal("the second start was accepted over a cap of one")
	}
	if code, ok := client.WorkspaceCodeOf(err); !ok || code != "vendor.capacity" {
		t.Fatalf("refusal code = %q, %v; want vendor.capacity", code, ok)
	}
	got, ok := client.VendorCapacityOf(err)
	if !ok || got != (client.Capacity{Running: 1, Limit: 1, Scope: client.CapacityScopeMachine}) {
		t.Fatalf("refusal detail = %+v, %v; want 1 of 1 machine", got, ok)
	}
	// Refused, not queued: the host holds exactly the first session.
	list, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].ID != first.ID {
		t.Fatalf("sessions after the refusal = %+v, want only the first", list.Sessions)
	}
}

// serveCappedHost serves a fresh host whose own config.toml caps the machine
// at one vendor session.
func serveCappedHost(t *testing.T, ctx context.Context, bin, fakeDir string) *testHost {
	t.Helper()
	base := shortDir(t)
	dataDir, runtime, home := filepath.Join(base, "data"), filepath.Join(base, "rt"), filepath.Join(base, "home")
	for _, dir := range []string{dataDir, runtime, home} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[host]\nmax_sessions = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return serveWithFakeVendorOn(t, ctx, bin, dataDir, runtime, fakeDir, "SYNX_FUGUE_HOME="+home)
}
