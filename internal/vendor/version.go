package vendor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// versionPattern matches a numeric major.minor.patch with an optional
// pre-release suffix, the shapes `claude --version` and `codex --version` print.
var versionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-[0-9A-Za-z.-]+)?$`)

// Version is the numeric part of a vendor CLI version.
type Version struct {
	Major, Minor, Patch int
}

// VersionRange is an inclusive numeric range with an optional human-readable label.
type VersionRange struct {
	Min, Max Version
	Label    string
}

// Contains reports whether v lies inside the range.
func (r VersionRange) Contains(v Version) bool {
	return CompareVersions(v, r.Min) >= 0 && CompareVersions(v, r.Max) <= 0
}

// String renders the range for warnings and tests.
func (r VersionRange) String() string {
	if r.Label != "" {
		return r.Label
	}
	return fmt.Sprintf("%d.%d.%d..%d.%d.%d",
		r.Min.Major, r.Min.Minor, r.Min.Patch, r.Max.Major, r.Max.Minor, r.Max.Patch)
}

// CompareVersions returns -1 when a < b, 0 when a == b, and +1 when a > b.
func CompareVersions(a, b Version) int {
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

// ParseVersion reads the numeric version out of strings like
// "2.1.319 (Claude Code)", "codex-cli 0.167.0", or a bare "2.1.319-beta.1".
func ParseVersion(s string) (Version, bool) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return Version{}, false
	}
	token := fields[0]
	if !versionPattern.MatchString(token) && len(fields) >= 2 && strings.HasSuffix(fields[0], "-cli") {
		token = fields[1]
	}
	m := versionPattern.FindStringSubmatch(token)
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

// VersionVerdict classifies one version string: the parsed version, whether it
// is inside the tested range, and the warning to surface when it is not.
func VersionVerdict(vendorName, output string, r VersionRange) (version string, tested bool, warning string) {
	v, ok := ParseVersion(output)
	if !ok {
		return "", false, "the CLI did not report a recognizable version; it runs unverified"
	}
	version = fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if r.Contains(v) {
		return version, true, ""
	}
	return version, false, fmt.Sprintf("%s %s is outside the tested range %s; it runs unverified", vendorName, version, r)
}
