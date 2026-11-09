package claude

// VR-08: versions outside the tested range still run, with an untested-version
// warning; the range pins exactly what the spike and the fixtures verified.

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestCheckedRangePinsTheFixtureVersion(t *testing.T) {
	t.Parallel()
	parsed, ok := ParseVersion(fixtureCLI)
	if !ok {
		t.Fatal("the pinned version string no longer parses")
	}
	if diff := cmp.Diff(Version{Major: 2, Minor: 1, Patch: 319}, parsed); diff != "" {
		t.Errorf("parse mismatch (-want +got):\n%s", diff)
	}
	r := checkedRange()
	if r.Min != parsed || r.Max != parsed {
		t.Fatalf("checked range = %s, want both ends at 2.1.319", r)
	}
}

func TestParseVersionAcceptsCLIOutputShapes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ in string }{
		{"2.1.319 (Claude Code)"},
		{fixtureCLI},
		{"2.1.319-beta.1"},
		{"  3.0.0 (Claude Code)  "},
	} {
		if _, ok := ParseVersion(tc.in); !ok {
			t.Errorf("ParseVersion(%q) rejected a CLI shape", tc.in)
		}
	}
	for _, bad := range []string{"", "claude", "2.1", "x.y.z", "2.1.319.4.5"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("ParseVersion(%q) accepted a non-version", bad)
		}
	}
}

func TestVersionOutsideCheckedRangeWarnsButRuns(t *testing.T) {
	t.Parallel()
	version, tested, warning := versionVerdict("2.2.0 (Claude Code)")
	if version != "2.2.0" || tested {
		t.Fatalf("verdict = %q tested=%v, want 2.2.0 untested", version, tested)
	}
	if warning == "" {
		t.Fatal("an untested version must carry a warning")
	}
	if want := "claude 2.2.0 is outside the tested range 2.1.319..2.1.319; it runs unverified"; warning != want {
		t.Fatalf("warning = %q, want %q", warning, want)
	}

	older, tested, warning := versionVerdict("2.0.9")
	if older != "2.0.9" || tested || warning == "" {
		t.Fatalf("older verdict = %q tested=%v warning=%q, want untested with warning", older, tested, warning)
	}

	inside, tested, warning := versionVerdict("2.1.319 (Claude Code)")
	if !tested || warning != "" || inside != fixtureCLI {
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
