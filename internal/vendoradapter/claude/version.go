package claude

// The tested version range (VR-08): the design spike verified claude 2.1.319
// (vendor-runtime §5) and the F9.5 fixtures were recorded from it. A version
// outside the range still runs, with an untested-version warning; widening the
// range needs new conformance evidence, never a silent edit.

import "github.com/syurodev/fugue/internal/vendor"

// Version is the numeric part of a Claude Code version.
type Version = vendor.Version

// versionRange is an inclusive numeric range.
type versionRange = vendor.VersionRange

// checkedRange reports the versions this adapter was verified against: the
// design spike and the F9.5 fixtures both used claude 2.1.319
// (vendor-runtime §5), so both ends pin it.
func checkedRange() versionRange {
	return versionRange{Min: Version{Major: 2, Minor: 1, Patch: 319}, Max: Version{Major: 2, Minor: 1, Patch: 319}}
}

// VersionTested reports whether the version lies inside the tested range.
func VersionTested(v Version) bool {
	r := checkedRange()
	return r.Contains(v)
}

// ParseVersion reads the numeric version out of strings like
// "2.1.319 (Claude Code)" or a bare "2.1.319-beta.1".
func ParseVersion(s string) (Version, bool) {
	return vendor.ParseVersion(s)
}

// versionVerdict classifies one version string: the parsed version, whether it
// is inside the tested range, and the warning to surface when it is not.
func versionVerdict(output string) (version string, tested bool, warning string) {
	return vendor.VersionVerdict(DefaultBinary, output, checkedRange())
}
