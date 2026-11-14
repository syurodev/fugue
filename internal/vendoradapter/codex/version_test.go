package codex

// VR-08: versions outside the tested range still run, with an untested-version
// warning; the range pins the 0.167.x series verified by the F9.7 fixtures.

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/vendor"
)

func TestCheckedRangePinsTheFixtureSeries(t *testing.T) {
	t.Parallel()
	parsed, ok := ParseVersion(fixtureCLI)
	if !ok {
		t.Fatal("the pinned version string no longer parses")
	}
	if diff := cmp.Diff(Version{Major: 0, Minor: 167, Patch: 0}, parsed); diff != "" {
		t.Errorf("parse mismatch (-want +got):\n%s", diff)
	}
	r := checkedRange()
	if !r.Contains(parsed) || !VersionTested(parsed) || !r.Contains(Version{Major: 0, Minor: 167, Patch: 9}) {
		t.Fatalf("checked range %s must contain 0.167.x", r)
	}
	if r.Contains(Version{Major: 0, Minor: 168, Patch: 0}) || r.Contains(Version{Major: 0, Minor: 166, Patch: 9}) {
		t.Fatalf("checked range %s must not contain versions outside 0.167.x", r)
	}
}

func TestParseVersionAcceptsCLIOutputShapes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in string }{
		{"codex-cli 0.167.0"},
		{fixtureCLI},
		{"codex-cli 0.167.2-beta.1"},
		{"  0.167.0 (Codex)  "},
	} {
		if _, ok := ParseVersion(tc.in); !ok {
			t.Errorf("ParseVersion(%q) rejected a CLI shape", tc.in)
		}
	}
	for _, bad := range []string{"", "codex", "0.167", "x.y.z", "0.167.0.4.5"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("ParseVersion(%q) accepted a non-version", bad)
		}
	}
}

func TestCodexVersionOutsideTheTestedRangeWarnsButRuns(t *testing.T) {
	t.Parallel()
	assertVersionVerdictTable(t)

	// Probe and Start with an untested CLI version both succeed while surfacing the warning.
	a := NewAdapter(nil, WithBinary(fakeCodexBinary(t)))
	home := vendor.AccountHome{ConfigDir: fakeUntestedVersionConfigDir}
	report, err := a.Probe(t.Context(), home)
	if err != nil {
		t.Fatalf("Probe with untested version failed: %v", err)
	}
	if report.Version != untestedCLI || report.VersionTested || report.VersionWarning == "" {
		t.Fatalf("probe report = %+v, want %s untested with warning", report, untestedCLI)
	}

	sess, err := a.Start(t.Context(), &vendor.StartSpec{
		Cwd:       t.TempDir(),
		Home:      home,
		SessionID: "session-untested-version",
	})
	if err != nil {
		t.Fatalf("Start with untested version failed: %v", err)
	}
	defer closeTestSession(t, sess)

	codexSess, ok := sess.(*Session)
	if !ok {
		t.Fatalf("expected *Session, got %T", sess)
	}
	var gotInit *Init
	for obs := range codexSess.Observations() {
		if obs.Init != nil {
			gotInit = obs.Init
			break
		}
	}
	if gotInit == nil || gotInit.Version != untestedCLI {
		t.Fatalf("init observation = %+v, want version %s", gotInit, untestedCLI)
	}
}

func assertVersionVerdictTable(t *testing.T) {
	t.Helper()
	version, tested, warning := versionVerdict("codex-cli " + untestedCLI)
	if version != untestedCLI || tested {
		t.Fatalf("verdict = %q tested=%v, want %s untested", version, tested, untestedCLI)
	}
	if want := "codex 0.168.0 is outside the tested range 0.167.x; it runs unverified"; warning != want {
		t.Fatalf("warning = %q, want %q", warning, want)
	}

	older, tested, warning := versionVerdict("codex-cli 0.166.9")
	if older != "0.166.9" || tested || warning == "" {
		t.Fatalf("older verdict = %q tested=%v warning=%q, want untested with warning", older, tested, warning)
	}

	inside, tested, warning := versionVerdict("codex-cli 0.167.4")
	if !tested || warning != "" || inside != "0.167.4" {
		t.Fatalf("inside verdict = %q tested=%v warning=%q", inside, tested, warning)
	}
}

func TestUnrecognizableVersionRunsUnverified(t *testing.T) {
	t.Parallel()
	version, tested, warning := versionVerdict("totally unexpected output")
	if version != "" || tested || warning == "" {
		t.Fatalf("verdict = %q tested=%v warning=%q, want empty and unverified", version, tested, warning)
	}
}
