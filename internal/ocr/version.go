package ocr

import (
	"fmt"
	"strings"

	"github.com/syurodev/fugue/internal/vendor"
)

const (
	// DefaultBinary is the ocr CLI name on PATH.
	DefaultBinary = "ocr"

	// SupportedSchemaVersion is the delegate-mode JSON schema_version
	// supported by Fugue (orchestration §10.1).
	SupportedSchemaVersion = "1"
)

// checkedRange pins the OCR CLI versions verified for delegate-mode JSON
// output (--format json was introduced in 1.9.0).
func checkedRange() vendor.VersionRange {
	return vendor.VersionRange{
		Min:   vendor.Version{Major: 1, Minor: 9, Patch: 0},
		Max:   vendor.Version{Major: 1, Minor: 9, Patch: 99},
		Label: "1.9.0..1.9.99",
	}
}

// ParseVersion extracts the semantic version from `ocr --version` output
// such as "1.9.0", "ocr 1.9.0", or "ocr version v1.9.0".
func ParseVersion(output string) (vendor.Version, bool) {
	fields := strings.Fields(strings.TrimSpace(output))
	for _, f := range fields {
		cand := strings.TrimPrefix(f, "v")
		if v, ok := vendor.ParseVersion(cand); ok {
			return v, true
		}
	}
	return vendor.Version{}, false
}

// VersionVerdict classifies `ocr --version` output against the pinned range.
func VersionVerdict(output string) (version string, supported bool, warning string) {
	v, ok := ParseVersion(output)
	if !ok {
		return "", false, "ocr did not report a recognizable version"
	}
	version = fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	r := checkedRange()
	if r.Contains(v) {
		return version, true, ""
	}
	return version, false, fmt.Sprintf("ocr %s is outside the supported range %s", version, r)
}
