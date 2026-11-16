package claude

// F9.10a: the recorded rate_limit_event and result lines feed the vendor's
// account. The recorded fixture is the real CLI's output; the exhausted-plan
// and failed-auth cases are that same line with one field changed (named
// "derived" below), because a real exhausted or logged-out account cannot be
// recorded on demand.

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/vendor"
)

const sevenDay = "seven_day"

const quotaFixture = "claude-2.1.319-session3-steer.jsonl"

func fixtureLine(t *testing.T, fixture, marker string) []byte {
	t.Helper()
	var last []byte
	for _, line := range readFixture(t, fixture) {
		if bytes.Contains(line, []byte(marker)) {
			last = line
		}
	}
	if last == nil {
		t.Fatalf("fixture %s has no line with %s", fixture, marker)
	}
	return last
}

func claudeAccounts() *vendor.Accounts {
	return vendor.NewAccounts(clock.NewFake(testEpoch()), []vendor.AccountVendor{{Name: DefaultBinary, DirectQuota: true}})
}

func TestRecordedRateLimitEventsBecomeAccountQuotaWindows(t *testing.T) {
	t.Parallel()
	accounts := claudeAccounts()
	n := NewNormalizer("session-1")
	n.BeginTurn("turn-1")
	for _, line := range readFixture(t, quotaFixture) {
		obs, err := n.Normalize(line)
		if err != nil {
			t.Fatal(err)
		}
		accounts.ObserveQuota("claude", obs.Quota)
	}
	assertRecordedWindows(t, &accounts.Snapshot()[0])
}

func assertRecordedWindows(t *testing.T, got *vendor.Account) {
	t.Helper()
	if got.State != vendor.AccountOK || !got.QuotaKnown || len(got.Windows) != 2 {
		t.Fatalf("account = %+v, want ok with two known windows", got)
	}
	five, seven := got.Windows[0], got.Windows[1]
	if five.Name != "five_hour" || five.Utilization != 0.16 || !five.ResetsAt.Equal(time.Unix(1794314400, 0)) || five.Status != "allowed" {
		t.Fatalf("five_hour = %+v", five)
	}
	if seven.Name != sevenDay || seven.Utilization != 0.36 || !seven.ResetsAt.Equal(time.Unix(1794744000, 0)) {
		t.Fatalf("seven_day = %+v", seven)
	}
}

func TestDerivedRejectedWindowPausesTheAccountUntilResetsAt(t *testing.T) {
	t.Parallel()
	line := fixtureLine(t, quotaFixture, `"rate_limit_event"`)
	derived := bytes.Replace(line, []byte(`"utilization":0.16,"resetsAt":1794314400,"status":"allowed"`), []byte(`"utilization":1,"resetsAt":1794314400,"status":"rejected"`), 1)
	if bytes.Equal(line, derived) {
		t.Fatal("the derived line did not change: the fixture shape moved")
	}
	n := NewNormalizer("session-1")
	n.BeginTurn("turn-1")
	obs, err := n.Normalize(derived)
	if err != nil {
		t.Fatal(err)
	}
	accounts := claudeAccounts()
	accounts.ObserveQuota("claude", obs.Quota)
	got := accounts.Snapshot()[0]
	if got.State != vendor.AccountPaused || !got.PausedUntil.Equal(time.Unix(1794314400, 0)) {
		t.Fatalf("account = %+v, want paused until the five_hour resetsAt", got)
	}
}

func TestResultFailureIsClassifiedByStatusAndPlanState(t *testing.T) {
	t.Parallel()
	result := fixtureLine(t, "claude-2.1.319-session1.jsonl", `"type":"result"`)
	rejected := bytes.ReplaceAll(fixtureLine(t, quotaFixture, `"rate_limit_event"`), []byte(`"status":"allowed"`), []byte(`"status":"rejected"`))
	failed := func(status string) []byte {
		out := strings.Replace(string(result), `"is_error":false`, `"is_error":true`, 1)
		out = strings.Replace(out, `"api_error_status":null`, `"api_error_status":`+status, 1)
		return []byte(out)
	}
	cases := []struct {
		name       string
		planWasHit bool
		status     string
		want       vendor.FailureClass
	}{
		{"401 needs login", false, "401", vendor.FailureAuth},
		{"429 on a rejected plan is quota", true, "429", vendor.FailureQuota},
		{"429 with no rejected window is a passing limit", false, "429", vendor.FailureRateLimited},
		{"500 changes nothing", true, "500", vendor.FailureNone},
		{"no status changes nothing", true, "null", vendor.FailureNone},
	}
	for _, tc := range cases {
		n := NewNormalizer("session-1")
		n.BeginTurn("turn-1")
		if tc.planWasHit {
			if _, err := n.Normalize(rejected); err != nil {
				t.Fatal(err)
			}
		}
		obs, err := n.Normalize(failed(tc.status))
		if err != nil {
			t.Fatal(err)
		}
		if obs.TurnEnd == nil || obs.TurnEnd.Outcome != OutcomeFailed || obs.TurnEnd.Failure != tc.want {
			t.Errorf("%s: turn end = %+v, want a failed turn classed %q", tc.name, obs.TurnEnd, tc.want)
		}
	}
}
