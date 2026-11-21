package ocr

import (
	"context"
	"time"

	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

// ProbeResult records what the host-start OCR CLI probe observed (VR-01, §10.1).
type ProbeResult struct {
	Found     bool
	Path      string
	Version   string
	Supported bool
	Warning   string
}

// Probe locates the ocr CLI and checks its version against the pinned range.
// Missing or unsupported binaries return a non-supported ProbeResult so callers
// fall back to directory bundling ("no OCR").
func Probe(ctx context.Context, binary string, hostEnv []string, timeout time.Duration) ProbeResult {
	path, err := vendor.ResolveBinary(DefaultBinary, binary)
	if err != nil {
		return ProbeResult{Found: false, Warning: "ocr binary not found"}
	}
	env := process.FilterEnv(hostEnv, process.DefaultAllowlist(), nil, nil)
	output, err := vendor.RunVersionCommand(ctx, DefaultBinary, path, env, timeout)
	if err != nil {
		return ProbeResult{Found: true, Path: path, Warning: "ocr version command failed"}
	}
	ver, supported, warning := VersionVerdict(output)
	return ProbeResult{
		Found:     true,
		Path:      path,
		Version:   ver,
		Supported: supported,
		Warning:   warning,
	}
}
