package client_test

import (
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
)

// TestStartSessionAgainstARealHost starts a session on a real `fugue serve`
// whose vault is locked, and gets the host's typed vault-locked answer back:
// the write methods fail closed while the vault is locked.
func TestStartSessionAgainstARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	_, err := c.StartSession(testCtx(t), "start-1", client.VendorClaude, "no-such-workspace", "", client.SessionProfile{})
	if err == nil {
		t.Fatal("a start on a locked vault succeeded")
	}
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("error = %v, want the failed-precondition code the host maps vendor.vault_locked to", err)
	}
}

// TestStartSessionRetryAttachesToTheOperationOnARealHost retries a start with
// the same command id and the same command and gets the original failure
// attached, not a second run; the same id for a different command is refused
// as a conflict. The locked vault makes every start fail typed, which is the
// operation the retries attach to.
func TestStartSessionRetryAttachesToTheOperationOnARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	ctx := testCtx(t)
	const commandID = "start-dup-1"
	first, err := c.StartSession(ctx, commandID, client.VendorClaude, "ws", "", client.SessionProfile{})
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("first start = %v, %v; want the locked-vault failure", first, err)
	}
	again, err := c.StartSession(ctx, commandID, client.VendorClaude, "ws", "", client.SessionProfile{})
	if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("retry = %v, %v; want the same attached failure", again, err)
	}
	if _, err := c.StartSession(ctx, commandID, client.VendorClaude, "other-workspace", "", client.SessionProfile{}); err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("conflicting retry error = %v, want the invalid-argument code the host maps vendor.command_conflict to", err)
	}
}

// TestResumeSessionAgainstARealHost resumes a session the host does not know
// and gets the typed not-found answer back: the session binding is checked
// before anything spawns.
func TestResumeSessionAgainstARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	session, err := c.ResumeSession(testCtx(t), "resume-1", "no-such-session", "", "", client.SessionProfile{})
	if err == nil {
		t.Fatalf("resume of an unknown session = %v, want a typed failure", session)
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("error = %v, want the not-found code the host maps vendor.session_not_found to", err)
	}
}

// TestSendSteerAndInterruptAgainstARealHost deliver a message, a steer and a
// stop to a real host whose vault is locked. The message and the steer fail
// typed on the locked vault — writing input needs the vault — and the stop
// of an unknown session fails typed not-found: stopping does not read
// protected payloads, so the vault does not gate it.
func TestSendSteerAndInterruptAgainstARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	ctx := testCtx(t)
	for _, tc := range []struct {
		name string
		code connect.Code
		run  func() error
	}{
		{"send", connect.CodeFailedPrecondition, func() error { return c.Send(ctx, "send-1", "no-such-session", "hello") }},
		{"steer", connect.CodeFailedPrecondition, func() error { return c.Steer(ctx, "steer-1", "no-such-session", "hello") }},
		{"interrupt", connect.CodeNotFound, func() error {
			_, err := c.Interrupt(ctx, "stop-1", "no-such-session")
			return fmt.Errorf("interrupt the unknown session: %w", err)
		}},
	} {
		if err := tc.run(); err == nil || connect.CodeOf(err) != tc.code {
			t.Fatalf("%s error = %v, want %v", tc.name, err, tc.code)
		}
	}
}

// TestListCommandsAgainstARealHost lists the commands of a session the host
// does not know and gets the typed not-found answer back.
func TestListCommandsAgainstARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	_, err := c.ListCommands(testCtx(t), "no-such-session")
	if err == nil {
		t.Fatal("commands of an unknown session succeeded")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("error = %v, want the not-found code the host maps vendor.session_not_found to", err)
	}
}

// TestListWorkspacesAgainstARealHost reads the empty workspace list of a
// fresh real host: the composer's start flow starts from nothing.
func TestListWorkspacesAgainstARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	list, err := c.ListWorkspaces(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("workspaces = %v, want an empty list on a fresh host", list)
	}
}

// TestWorkspaceAgainstARealHost reads a workspace the host does not know and
// gets the typed not-found answer back.
func TestWorkspaceAgainstARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	_, err := c.Workspace(testCtx(t), "no-such-workspace")
	if err == nil {
		t.Fatal("an unknown workspace succeeded")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("error = %v, want the not-found code the host maps workspace.workspace_not_found to", err)
	}
}

// TestProbeVendorsAgainstARealHost probes on a real host and pins the
// mapping either way the environment answers: a host that cannot resolve the
// vendor CLI answers typed not-found, and one that can reports the probed
// vendor with its capability. The composer's Steer and stop gating reads
// exactly this answer.
func TestProbeVendorsAgainstARealHost(t *testing.T) {
	t.Parallel()
	c := startRealHost(t)
	probes, err := c.ProbeVendors(testCtx(t))
	if err != nil {
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Fatalf("error = %v, want the typed not-found the host maps a missing vendor binary to", err)
		}
		return
	}
	if len(probes) == 0 {
		t.Fatal("the host answered no probes")
	}
	if probes[0].Vendor != client.VendorClaude {
		t.Fatalf("probe vendor = %q, want claude", probes[0].Vendor)
	}
}
