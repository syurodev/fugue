package codex

// The tested version range (VR-08): F9.7 verified codex 0.167.0 and tests
// against the 0.167.x series (vendor-runtime §5.2, §11). A version outside the
// range still runs, with an untested-version warning; widening the range needs
// new conformance evidence, never a silent edit.

import "github.com/syurodev/fugue/internal/vendor"

// Version is the numeric part of a Codex CLI version.
type Version = vendor.Version

// versionRange is an inclusive numeric range.
type versionRange = vendor.VersionRange

// maxTestedPatch is the upper bound of the 0.167.x patch range.
const maxTestedPatch = 999999

// checkedRange reports the versions this adapter was verified against: the
// 0.167.x series (vendor-runtime §5.2, §11).
func checkedRange() versionRange {
	return versionRange{
		Min:   Version{Major: 0, Minor: 167, Patch: 0},
		Max:   Version{Major: 0, Minor: 167, Patch: maxTestedPatch},
		Label: "0.167.x",
	}
}

// VersionTested reports whether the version lies inside the tested range.
func VersionTested(v Version) bool {
	r := checkedRange()
	return r.Contains(v)
}

// ParseVersion reads the numeric version out of strings like
// "codex-cli 0.167.0" or a bare "0.167.0-beta.1".
func ParseVersion(s string) (Version, bool) {
	return vendor.ParseVersion(s)
}

// versionVerdict classifies one version string: the parsed version, whether it
// is inside the tested range, and the warning to surface when it is not.
func versionVerdict(output string) (version string, tested bool, warning string) {
	return vendor.VersionVerdict(DefaultBinary, output, checkedRange())
}
