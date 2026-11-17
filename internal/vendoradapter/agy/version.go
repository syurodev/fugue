package agy

// The tested version range (VR-08): exactly agy 1.5.10, the version whose
// ACP behavior (handshake, session, prompt, tool calls and the nested
// `session/request_permission` outcome) was measured against the adapter
// (vendor-runtime §5.3, §11). The earlier 1.5.9 pin is dropped because the
// nested permission answer was never measured on 1.5.9. A version outside the
// range still runs, with an untested-version warning; widening the range needs
// new conformance evidence, never a silent edit.

import "github.com/syurodev/fugue/internal/vendor"

// Version is the numeric part of an Antigravity CLI version.
type Version = vendor.Version

// versionRange is an inclusive numeric range.
type versionRange = vendor.VersionRange

// PinnedVersion is the exact Antigravity CLI version verified against the adapter.
const PinnedVersion = "1.5.10"

// checkedRange reports the versions this adapter was verified against.
func checkedRange() versionRange {
	return versionRange{
		Min:   Version{Major: 1, Minor: 5, Patch: 10},
		Max:   Version{Major: 1, Minor: 5, Patch: 10},
		Label: PinnedVersion,
	}
}

// VersionTested reports whether the version lies inside the pinned range.
func VersionTested(v Version) bool {
	r := checkedRange()
	return r.Contains(v)
}

// ParseVersion reads the numeric version out of strings like "1.5.9" or
// "agy 1.5.9".
func ParseVersion(s string) (Version, bool) {
	return vendor.ParseVersion(s)
}

// versionVerdict classifies one version string: the parsed version, whether it
// is inside the pinned range, and the warning to surface when it is not.
func versionVerdict(output string) (version string, tested bool, warning string) {
	return vendor.VersionVerdict(DefaultBinary, output, checkedRange())
}
