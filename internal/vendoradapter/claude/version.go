package claude

// The tested version range (VR-08): the design spike verified claude 2.1.319
// (vendor-runtime §5) and the F9.5 fixtures were recorded from it. A version
// outside the range still runs, with an untested-version warning; widening the
// range needs new conformance evidence, never a silent edit.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// versionPattern matches a numeric major.minor.patch with an optional
// pre-release suffix, the shapes `claude --version` prints.
var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-[0-9A-Za-z.-]+)?$`)

// Version is the numeric part of a Claude Code version.
type Version struct {
	Major, Minor, Patch int
}

// versionRange is an inclusive numeric range.
type versionRange struct{ Min, Max Version }

// checkedRange reports the versions this adapter was verified against: the
// design spike and the F9.5 fixtures both used claude 2.1.319
// (vendor-runtime §5), so both ends pin it. TestCheckedRangePinsTheFixture
// keeps the literal honest against ParseVersion. A function, not a variable,
// keeps the package free of mutable state.
func checkedRange() versionRange {
	return versionRange{Min: Version{Major: 2, Minor: 1, Patch: 319}, Max: Version{Major: 2, Minor: 1, Patch: 319}}
}

// VersionTested reports whether the version lies inside the tested range.
func VersionTested(v Version) bool {
	r := checkedRange()
	return r.Contains(v)
}

// ParseVersion reads the numeric version out of strings like
// "2.1.319 (Claude Code)" or a bare "2.1.319-beta.1": the first whitespace
// token must be a numeric major.minor.patch; a pre-release suffix is ignored.
func ParseVersion(s string) (Version, bool) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return Version{}, false
	}
	m := versionPattern.FindStringSubmatch(fields[0])
	if m == nil {
		return Version{}, false
	}
	nums := make([]int, 3)
	for i, digits := range m[1:] {
		n, err := strconv.Atoi(digits)
		if err != nil {
			return Version{}, false
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}, true
}

// Contains reports whether v lies inside the range.
func (r versionRange) Contains(v Version) bool {
	return compareVersions(v, r.Min) >= 0 && compareVersions(v, r.Max) <= 0
}

// String renders the range for warnings and tests.
func (r versionRange) String() string {
	return fmt.Sprintf("%d.%d.%d..%d.%d.%d",
		r.Min.Major, r.Min.Minor, r.Min.Patch, r.Max.Major, r.Max.Minor, r.Max.Patch)
}

func compareVersions(a, b Version) int {
	for _, pair := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// versionVerdict classifies one version string: the parsed version, whether it
// is inside the tested range, and the warning to surface when it is not.
func versionVerdict(output string) (version string, tested bool, warning string) {
	v, ok := ParseVersion(output)
	if !ok {
		return "", false, "the CLI did not report a recognizable version; it runs unverified"
	}
	version = fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if VersionTested(v) {
		return version, true, ""
	}
	return version, false, fmt.Sprintf("claude %s is outside the tested range %s; it runs unverified", version, checkedRange())
}
