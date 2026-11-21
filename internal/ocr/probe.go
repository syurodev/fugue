package ocr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

// DefaultBinary is the ocr CLI name on PATH.
const DefaultBinary = "ocr"

// ProbeResult records what the host-start OCR CLI probe observed for display
// and logs (VR-01, §10.1). Compatibility is checked by JSON schema_version, not
// CLI version.
type ProbeResult struct {
	Found   bool
	Path    string
	Version string
}

// Probe locates the ocr CLI and reads its version string for display and logs.
func Probe(ctx context.Context, binary string, hostEnv []string, timeout time.Duration) ProbeResult {
	path, err := vendor.ResolveBinary(DefaultBinary, binary)
	if err != nil {
		return ProbeResult{Found: false}
	}
	env := process.FilterEnv(hostEnv, process.DefaultAllowlist(), nil, nil)
	output, err := vendor.RunVersionCommand(ctx, DefaultBinary, path, env, timeout)
	if err != nil {
		return ProbeResult{Found: true, Path: path}
	}
	return ProbeResult{
		Found:   true,
		Path:    path,
		Version: parseVersionString(output),
	}
}

func parseVersionString(output string) string {
	for _, f := range strings.Fields(strings.TrimSpace(output)) {
		cand := strings.TrimPrefix(f, "v")
		if v, ok := vendor.ParseVersion(cand); ok {
			return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
		}
	}
	return ""
}
