package main

import (
	"context"
	"strconv"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/pkg/client"
)

func awaitFailureUXLeadRunning(t *testing.T, ctx context.Context, c *client.Client, teamID string) {
	t.Helper()
	testutil.Spin(t, "lead task running", func() bool {
		tv, err := c.GetTeam(ctx, &client.GetTeamInput{TeamID: teamID})
		if err != nil {
			return false
		}
		for i := range tv.Tasks {
			if tv.Tasks[i].HierarchyRole == "lead" {
				return tv.Tasks[i].State == failureUXTaskRunning
			}
		}
		return false
	})
}

func failureUXSessionCount(t *testing.T, ctx context.Context, c *client.Client) int {
	t.Helper()
	list, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	return len(list.Sessions)
}

// TestFailureUXQuotaGateQueuesATeamTaskOverARealHost is the "quota wait" row
// of activation §3 (A-E05) for team tasks: with the vendor account at or
// above the 85% gate a member task waits in queued (never failed), no member
// session starts, and the account read model shows the window and the gate;
// below the gate the same team starts its member.
func TestFailureUXQuotaGateQueuesATeamTaskOverARealHost(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, utilization, wantMember string
		wantSessions                  int
	}{
		{name: "above the gate", utilization: "0.9", wantMember: failureUXTaskQueued, wantSessions: 1},
		{name: "below the gate", utilization: "0.5", wantMember: failureUXTaskRunning, wantSessions: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startE2EHostWith(t, &e2eHostOptions{bridge: true})
			ctx := failureUXCtx(t)
			_, _ = startFailureUXLead(t, ctx, h.client, failureUXRepo(t), "FAILUX_PROPOSE;members=1;quota="+tc.utilization+";")
			teamID := approveFailureUXProposal(t, ctx, h.client)
			awaitFailureUXLeadRunning(t, ctx, h.client, teamID)
			tv := awaitFailureUXMembers(t, ctx, h.client, teamID, tc.wantMember)

			if tv.Summary.FailedCount != 0 {
				t.Fatalf("failed tasks = %d, want 0: the quota gate queues, it does not fail", tv.Summary.FailedCount)
			}
			if got := failureUXSessionCount(t, ctx, h.client); got != tc.wantSessions {
				t.Fatalf("sessions = %d, want %d", got, tc.wantSessions)
			}
			accounts, err := h.client.ListAccounts(ctx)
			if err != nil {
				t.Fatalf("ListAccounts: %v", err)
			}
			assertFailureUXQuotaWindow(t, accounts, tc.utilization)
		})
	}
}

func assertFailureUXQuotaWindow(t *testing.T, accounts []client.Account, utilization string) {
	t.Helper()
	for i := range accounts {
		if accounts[i].Vendor != client.VendorClaude {
			continue
		}
		if !accounts[i].QuotaKnown || accounts[i].QuotaGate != 0.85 || len(accounts[i].Windows) != 1 ||
			accounts[i].Windows[0].Name != "five_hour" || strconv.FormatFloat(accounts[i].Windows[0].Utilization, 'f', 1, 64) != utilization {
			t.Fatalf("claude account = %+v, want one five_hour window at %s and the 0.85 gate", accounts[i], utilization)
		}
		return
	}
	t.Fatalf("accounts = %+v, want a claude account", accounts)
}

// TestFailureUXSessionCapQueuesATeamTaskOverARealHost is the "worker wait" row
// of activation §3 (A-E05) for team tasks: with the host session cap reached
// by the lead and one member, the next member waits in queued (never failed)
// and no process starts for it, while a user session start is refused with the
// typed vendor.capacity code.
func TestFailureUXSessionCapQueuesATeamTaskOverARealHost(t *testing.T) {
	t.Parallel()
	h := startE2EHostWith(t, &e2eHostOptions{bridge: true, hostConfig: "[host]\nmax_sessions = 2\n"})
	ctx := failureUXCtx(t)
	ws, _ := startFailureUXLead(t, ctx, h.client, failureUXRepo(t), "FAILUX_PROPOSE;members=2;")
	teamID := approveFailureUXProposal(t, ctx, h.client)
	tv := awaitFailureUXMembers(t, ctx, h.client, teamID, failureUXTaskRunning, failureUXTaskQueued)

	if tv.Summary.FailedCount != 0 {
		t.Fatalf("failed tasks = %d, want 0: the session cap queues, it does not fail", tv.Summary.FailedCount)
	}
	if got := failureUXSessionCount(t, ctx, h.client); got != 2 {
		t.Fatalf("sessions = %d, want 2 (the lead and the running member)", got)
	}
	_, err := h.client.StartSession(ctx, "failux-over-cap", client.VendorClaude, ws.ID, failureUXRepoRoot, client.SessionProfile{})
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("StartSession over the cap = %v, want ResourceExhausted", err)
	}
	if code, ok := client.TasksCodeOf(err); !ok || code != string(errcode.VendorCapacity) {
		t.Fatalf("StartSession over the cap code = %q (%v), want %q", code, ok, errcode.VendorCapacity)
	}
}
