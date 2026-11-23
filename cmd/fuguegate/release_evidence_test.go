//go:build release

package main

import (
	"os"
	"strings"
	"testing"
)

// rcEnv names the environment variable that carries the release candidate
// commit under test; `task release-check` sets it to the checked-out commit.
const rcEnv = "FUGUE_RC_COMMIT"

// TestReleaseEvidence is the release gate of verification §7 step 2. It runs
// only under the release build tag, from `task release-check`: on an ordinary
// PR it would fail until the release is ready. A missing RC fails the test;
// it never skips.
func TestReleaseEvidence(t *testing.T) {
	rc := os.Getenv(rcEnv)
	if rc == "" {
		t.Fatalf("%s is not set: run `task release-check` (or set it to the RC commit)", rcEnv)
	}
	report, err := checkRelease(t.Context(), "../..", rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Problems) > 0 {
		t.Fatalf("%d release blocker(s) for RC %s:\n- %s", len(report.Problems), report.RC, strings.Join(report.Problems, "\n- "))
	}
}
