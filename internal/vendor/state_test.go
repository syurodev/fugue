package vendor

import (
	"errors"
	"testing"
)

func TestTransitionTable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state State
		fact  Fact
		want  State
		allow bool
	}{
		{"handshake ready", State{Kind: StateStarting}, Fact{Kind: FactReady}, State{Kind: StateReady}, true},
		{"fugue send starts turn", State{Kind: StateReady}, Fact{Kind: FactFugueSend}, State{Kind: StateRunning}, true},
		{"vendor turn does not start session turn", State{Kind: StateReady}, Fact{Kind: FactVendorTurn}, State{Kind: StateReady}, true},
		{"approval while running", State{Kind: StateRunning}, Fact{Kind: FactApprovalRequested}, State{Kind: StateAwaitingApproval}, true},
		{"approved returns running", State{Kind: StateAwaitingApproval}, Fact{Kind: FactApprovalGranted}, State{Kind: StateRunning}, true},
		{"denied returns running", State{Kind: StateAwaitingApproval}, Fact{Kind: FactApprovalDenied}, State{Kind: StateRunning}, true},
		{"completed turn ready", State{Kind: StateRunning}, Fact{Kind: FactTurnCompleted}, State{Kind: StateReady}, true},
		{"interrupted turn ready", State{Kind: StateRunning}, Fact{Kind: FactTurnInterrupted}, State{Kind: StateReady}, true},
		{"failed turn ready", State{Kind: StateRunning}, Fact{Kind: FactTurnFailed, Code: "vendor_error"}, State{Kind: StateReady}, true},
		{"unknown turn detached", State{Kind: StateRunning}, Fact{Kind: FactTurnUnknown}, State{Kind: StateDetached}, true},
		{"vault lock waits", State{Kind: StateRunning}, Fact{Kind: FactWait, Reason: WaitingVaultLocked}, State{Kind: StateWaiting, Reason: WaitingVaultLocked}, true},
		{"quota waits", State{Kind: StateReady}, Fact{Kind: FactWait, Reason: WaitingQuota}, State{Kind: StateWaiting, Reason: WaitingQuota}, true},
		{"capacity waits", State{Kind: StateReady}, Fact{Kind: FactWait, Reason: WaitingCapacity}, State{Kind: StateWaiting, Reason: WaitingCapacity}, true},
		{"unlock ready", State{Kind: StateWaiting, Reason: WaitingVaultLocked}, Fact{Kind: FactUnlocked}, State{Kind: StateReady}, true},
		{"unlock does not clear quota", State{Kind: StateWaiting, Reason: WaitingQuota}, Fact{Kind: FactUnlocked}, State{}, false},
		{"process exit closes", State{Kind: StateReady}, Fact{Kind: FactProcessExited}, State{Kind: StateClosed}, true},
		{"host recovery detaches", State{Kind: StateRunning}, Fact{Kind: FactHostRestarted}, State{Kind: StateDetached}, true},
		{"explicit resume starts", State{Kind: StateDetached}, Fact{Kind: FactResume}, State{Kind: StateStarting}, true},
		{"close", State{Kind: StateReady}, Fact{Kind: FactClose}, State{Kind: StateClosed}, true},
		{"closed terminal", State{Kind: StateClosed}, Fact{Kind: FactReady}, State{}, false},
		{"invalid waiting reason", State{Kind: StateReady}, Fact{Kind: FactWait, Reason: "secret"}, State{}, false},
		{"invalid edge", State{Kind: StateStarting}, Fact{Kind: FactFugueSend}, State{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := Transition(test.state, test.fact)
			if !test.allow {
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("Transition error = %v, want ErrInvalidTransition", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Transition: %v", err)
			}
			if got != test.want {
				t.Fatalf("state = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestTransitionRejectsMalformedFactsWithoutPanic(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		state State
		fact  Fact
	}{
		{State{Kind: "invalid"}, Fact{Kind: FactReady}},
		{State{Kind: StateRunning}, Fact{Kind: FactTurnFailed}},
		{State{Kind: StateReady}, Fact{Kind: "invalid"}},
	} {
		if _, _, err := Transition(test.state, test.fact); !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("Transition(%+v, %+v) = %v, want typed error", test.state, test.fact, err)
		}
	}
}
