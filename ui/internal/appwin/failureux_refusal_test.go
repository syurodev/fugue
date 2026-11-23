package appwin

import (
	"strings"
	"testing"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
)

// A-E05 (activation §3): the banners of a host that never came up or speaks
// no common version, and of a session closed without a stop proof. The
// reasons are the hostconn constants the manager leaves, which the real-host
// end-to-end tests in ui/internal/hostconn pin against a real `fugue serve`.

// testAppVersion is the app version the refusal banners name.
const testAppVersion = "0.18.0"

// TestFailureUXHeldDataDirNamesTheDirectoryAndTheAction pins the held data
// directory row: execution store unavailable with the data directory path and
// the action (close the other process), in place of the generic lost
// connection.
func TestFailureUXHeldDataDirNamesTheDirectoryAndTheAction(t *testing.T) {
	t.Parallel()
	f := newFailureUX()
	specs := f.specs(hostconn.StateDisconnected, hostconn.ReasonDataDirInUse, "/data/fugue", false, session.Session{})
	if len(specs) != 1 || specs[0].title != "Execution store unavailable" {
		t.Fatalf("specs = %+v, want the one execution-store banner", specs)
	}
	for _, want := range []string{"/data/fugue", "Close that process"} {
		if !strings.Contains(specs[0].message, want) {
			t.Fatalf("message %q misses %q", specs[0].message, want)
		}
	}
	if specs[0].dismissible || !specs[0].visible {
		t.Fatalf("the refused-start banner is dismissible %v, visible %v; want pinned and visible", specs[0].dismissible, specs[0].visible)
	}
}

// TestFailureUXUnsupportedSchemaNamesTheVersionsAndTheUpgrade pins the
// unsupported schema row: the data directory, the app's own version, and the
// upgrade as the way out.
func TestFailureUXUnsupportedSchemaNamesTheVersionsAndTheUpgrade(t *testing.T) {
	t.Parallel()
	f := newFailureUX()
	f.appParams = client.Params{Version: testAppVersion, APIVersions: []string{"v1"}}
	specs := f.specs(hostconn.StateDisconnected, hostconn.ReasonStoreUnsupported, "/data/fugue", false, session.Session{})
	if len(specs) != 1 || specs[0].title != "Execution store unavailable" {
		t.Fatalf("specs = %+v, want the one execution-store banner", specs)
	}
	for _, want := range []string{"/data/fugue", "newer Fugue", testAppVersion, "Upgrade Fugue"} {
		if !strings.Contains(specs[0].message, want) {
			t.Fatalf("message %q misses %q", specs[0].message, want)
		}
	}
}

// TestFailureUXIncompatibleHostShowsBothVersions pins the unsupported
// protocol row: the manager's reason carries the host's versions, the banner
// adds the app's, and names the upgrade.
func TestFailureUXIncompatibleHostShowsBothVersions(t *testing.T) {
	t.Parallel()
	f := newFailureUX()
	f.appParams = client.Params{Version: testAppVersion, APIVersions: []string{"v2"}}
	reason := (&client.VersionMismatchError{HostAPIVersions: []string{"v1"}}).Error()
	specs := f.specs(hostconn.StateIncompatible, reason, "/data/fugue", false, session.Session{})
	if len(specs) != 1 || specs[0].title != "Fugue app and host versions do not match" {
		t.Fatalf("specs = %+v, want the version-mismatch banner", specs)
	}
	for _, want := range []string{"supports v1", testAppVersion, "Host API v2", "Upgrade Fugue"} {
		if !strings.Contains(specs[0].message, want) {
			t.Fatalf("message %q misses %q", specs[0].message, want)
		}
	}
}

// TestFailureUXRefusalReasonsDoNotLeakIntoOtherStates pins that the refusal
// banners follow their own reasons only: a plain disconnect still reads as a
// lost connection, and a connected app shows no refusal.
func TestFailureUXRefusalReasonsDoNotLeakIntoOtherStates(t *testing.T) {
	t.Parallel()
	f := newFailureUX()
	if specs := f.specs(hostconn.StateDisconnected, "connection lost", "/d", false, session.Session{}); len(specs) != 1 || specs[0].title != "Connection to the Fugue core lost" {
		t.Fatalf("specs = %+v, want the lost-connection banner", specs)
	}
	if specs := f.specs(hostconn.StateConnected, hostconn.ReasonDataDirInUse, "/d", false, session.Session{}); len(specs) != 0 {
		t.Fatalf("a connected app shows %+v for a stale refusal reason", specs)
	}
}

// TestFailureUXUnprovedStopShowsRestartGuidance pins the hung-worker row:
// the open session closed without a stop proof tells the user to restart the
// host and says the restart proves nothing about external effects; a proved
// stop or another state stays quiet.
func TestFailureUXUnprovedStopShowsRestartGuidance(t *testing.T) {
	t.Parallel()
	f := newFailureUX()
	f.stopUnproved = true
	specs := f.specs(hostconn.StateConnected, "", "", true, session.Session{ID: "s1", State: stateClosed})
	if len(specs) != 1 || specs[0].title != "Restart the Fugue host to close this run" {
		t.Fatalf("specs = %+v, want the restart-the-host banner", specs)
	}
	if !strings.Contains(specs[0].message, "does not prove that its external effects have stopped") {
		t.Fatalf("message %q misses the no-proof note", specs[0].message)
	}
	if titles := visibleTitles(f, true, session.Session{ID: "s1", State: stateReady}); len(titles) != 0 {
		t.Fatalf("a live session shows %v, want none", titles)
	}
	f.stopUnproved = false
	if titles := visibleTitles(f, true, session.Session{ID: "s1", State: stateClosed}); len(titles) != 0 {
		t.Fatalf("a proved stop shows %v, want none", titles)
	}
	if titles := visibleTitles(f, false, session.Session{}); len(titles) != 0 {
		t.Fatalf("no open session shows %v, want none", titles)
	}
}
