package agy

// F9.10a: agy has no quota signal (vendor-runtime §5.3), so no recorded event
// becomes a quota observation and its account says "unknown", not "unlimited".

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/vendor"
)

func TestAgyFixturesNeverProduceAQuotaObservation(t *testing.T) {
	t.Parallel()
	fixtures, err := filepath.Glob(filepath.Join("testdata", "fixtures", "agy-*.jsonl"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("no agy fixtures found: %v", err)
	}
	accounts := vendor.NewAccounts(clock.NewFake(testEpoch()), []vendor.AccountVendor{{Name: "agy"}})
	for _, fixture := range fixtures {
		n := NewNormalizer("session-1")
		n.BeginTurn("turn-1", "")
		for i, line := range readFixture(t, filepath.Base(fixture)) {
			obs, nErr := n.Normalize(line)
			if nErr != nil {
				t.Fatalf("%s line %d: %v", fixture, i+1, nErr)
			}
			if obs.Quota != nil {
				t.Fatalf("%s line %d: agy reported a quota observation", fixture, i+1)
			}
			accounts.ObserveQuota("agy", obs.Quota)
		}
	}
	got := accounts.Snapshot()[0]
	if got.QuotaKnown || len(got.Windows) != 0 || got.State != vendor.AccountOK {
		t.Fatalf("agy account = %+v, want quota unknown with no windows", got)
	}
}

func TestAgyProbeLeavesLoginUnknown(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: fakeAgyBinary(t)}
	report, err := a.Probe(t.Context(), vendor.AccountHome{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Login != vendor.LoginUnknown || strings.Contains(string(report.Login), "logged") {
		t.Fatalf("login = %s: agy has no status command", report.Login)
	}
}
