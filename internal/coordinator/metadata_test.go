package coordinator

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/kernel"
)

func TestCommandIdentityReadsSupportedCommands(t *testing.T) {
	t.Parallel()

	admit := new(kernel.AdmitRunCommand)
	admit.CommandID, admit.RunID, admit.Origin, admit.Token = "c2", "r2", "ignored", 2
	cmdID, runID, token := commandIdentity(admit)
	if cmdID != "c2" || runID != "r2" || token != 2 {
		t.Errorf("unexpected identity: %q, %q, %d", cmdID, runID, token)
	}

	accept := new(kernel.AcceptRunCommand)
	accept.CommandID, accept.RunID = "c1", "r1"
	cmdID, runID, token = commandIdentity(accept)
	if cmdID != "c1" || runID != "r1" || token != 0 {
		t.Errorf("unexpected accept identity: %q, %q, %d", cmdID, runID, token)
	}

	timer := new(kernel.TimerFiredCommand)
	timer.CommandID, timer.RunID = "x", "y"
	for _, c := range []kernel.Command{nil, (*kernel.AdmitRunCommand)(nil), timer} {
		if cmdID, runID, token = commandIdentity(c); cmdID != "" || runID != "" || token != 0 {
			t.Errorf("expected empty identity for %T, got %q, %q, %d", c, cmdID, runID, token)
		}
	}
}

func supportedCommands() []kernel.Command {
	return []kernel.Command{
		new(kernel.AcceptRunCommand), new(kernel.AdmitRunCommand), new(kernel.AdvanceRunCommand),
		new(kernel.PrepareDispatchCommand), new(kernel.ObserveModelStreamCommand),
		new(kernel.ObserveModelResponseCommand), new(kernel.ObserveModelFailureCommand),
		new(kernel.ObserveToolCheckCommand), new(kernel.ObserveToolResultCommand),
		new(kernel.ExtendBudgetCommand), new(kernel.RequestStopCommand),
	}
}

func TestStampCommandCopiesAndSetsIdentity(t *testing.T) {
	t.Parallel()

	for _, original := range supportedCommands() {
		stamped, err := stampCommand(original, "cmd", "run", "origin", 7)
		if err != nil {
			t.Fatalf("%s: %v", original.CommandName(), err)
		}
		if stamped == original {
			t.Errorf("%s: stamp returned the caller's value", original.CommandName())
		}
		if cmdID, runID, _ := commandIdentity(original); cmdID != "" || runID != "" {
			t.Errorf("%s: the caller's command was modified", original.CommandName())
		}
		cmdID, runID, token := commandIdentity(stamped)
		if cmdID != "cmd" || runID != "run" || (token != 7 && original.CommandName() != "AcceptRun") {
			t.Errorf("%s: stamped identity %q, %q, %d", original.CommandName(), cmdID, runID, token)
		}
	}
}

func TestStampCommandOverwritesClaimedOrigin(t *testing.T) {
	t.Parallel()

	forged := new(kernel.ExtendBudgetCommand)
	forged.Origin, forged.Actor = "user", "user"
	stamped, err := stampCommand(forged, "c", "r", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := stamped.(*kernel.ExtendBudgetCommand); !ok || got.Origin != "model" {
		t.Fatalf("origin %+v, want the trusted origin", got)
	}
}

func TestStampCommandRejectsUnsupportedAndNil(t *testing.T) {
	t.Parallel()

	for _, c := range []kernel.Command{nil, (*kernel.AcceptRunCommand)(nil), new(kernel.TimerFiredCommand)} {
		_, err := stampCommand(c, "c", "r", "o", 1)
		var rej kernel.Rejection
		if !errors.As(err, &rej) || rej.Code != kernel.RejectionCodeUnrecognizedCommand {
			t.Errorf("%T: expected unrecognized-command rejection, got %v", c, err)
		}
	}
}

func TestIsWorkerObservationAllowlist(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		"ObserveModelStream": true, "ObserveModelResponse": true, "ObserveModelFailure": true,
		"ObserveToolCheck": true, "ObserveToolResult": true,
	}
	for _, c := range supportedCommands() {
		if got := isWorkerObservation(c); got != allowed[c.CommandName()] {
			t.Errorf("%s: allowed=%v", c.CommandName(), got)
		}
	}
	if isWorkerObservation(nil) {
		t.Error("nil command must not be a worker observation")
	}
}
