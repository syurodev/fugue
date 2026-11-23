package appwin

import (
	"testing"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
)

func TestInboxRowStateHidesZero(t *testing.T) {
	t.Parallel()
	if label, _ := inboxRowState(0); label != "" {
		t.Fatalf("zero pending drew %q, want nothing", label)
	}
	label, tone := inboxRowState(3)
	if label != "3 pending" || tone != kit.ToneWarning {
		t.Fatalf("3 pending = %q, %v; want the warning pill", label, tone)
	}
}

func TestAccountsRowStateHidesUnknownAndOK(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		snapshot  bool
		attention int
		want      string
	}{
		{"no snapshot yet", false, 0, ""},
		{"no snapshot ignores a stale count", false, 2, ""},
		{"all well", true, 0, ""},
		{"one alert", true, 1, "1 alert"},
		{"two alerts", true, 2, "2 alerts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			label, tone := accountsRowState(tc.snapshot, tc.attention)
			if label != tc.want {
				t.Fatalf("label = %q, want %q", label, tc.want)
			}
			if tc.want != "" && tone != kit.ToneWarning {
				t.Fatalf("tone = %v, want warning", tone)
			}
		})
	}
}

func TestSecretsRowState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		locked, has bool
		count       int
		label       string
		badge       string
	}{
		{"locked wins over a snapshot", true, true, 4, vaultLockedLabel, ""},
		{"locked without snapshot", true, false, 0, vaultLockedLabel, ""},
		{"count badge", false, true, 4, "", "4"},
		{"empty vault shows nothing", false, true, 0, "", ""},
		{"no snapshot shows nothing", false, false, 0, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			label, tone, badge := secretsRowState(tc.locked, tc.has, tc.count)
			if label != tc.label || badge != tc.badge {
				t.Fatalf("got label %q badge %q, want %q and %q", label, badge, tc.label, tc.badge)
			}
			if label != "" && tone != kit.ToneWarning {
				t.Fatalf("locked tone = %v, want warning", tone)
			}
		})
	}
}

func TestSessionStateTextAndTone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		sess session.Session
		text string
		tone kit.ListRowTone
	}{
		{"ready", session.Session{State: stateReady}, stateReady, kit.ToneSuccess},
		{"running", session.Session{State: stateRunning}, stateRunning, kit.ToneInfo},
		{"starting", session.Session{State: stateStarting}, stateStarting, kit.ToneInfo},
		{"approval", session.Session{State: stateAwaitingApproval}, "needs you", kit.ToneWarning},
		{"waiting with reason", session.Session{State: stateWaiting, WaitingReason: reasonVaultLocked}, "waiting · vault_locked", kit.ToneWarning},
		{"detached", session.Session{State: stateDetached}, stateDetached, kit.ToneWarning},
		{"closed", session.Session{State: stateClosed}, stateClosed, kit.ToneNeutral},
		{"unknown state keeps its text", session.Session{State: "odd"}, "odd", kit.ToneNeutral},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text, tone := sessionState(tc.sess)
			if text != tc.text || tone != tc.tone {
				t.Fatalf("got %q, %v; want %q, %v", text, tone, tc.text, tc.tone)
			}
		})
	}
}
