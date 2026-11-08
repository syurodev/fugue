//go:build linux

package process

import (
	"context"

	"github.com/syurodev/fugue/internal/platform/clock"
)

func prepareSpawnArgv(ctx context.Context, opts *Options) ([]string, string, bool) {
	return prepareLinuxSpawnArgv(ctx, opts)
}

func attachPlatformContainment(
	ctx context.Context,
	p *Process,
	opts *Options,
	unitName string,
	usedSystemd bool,
) {
	cgroupRoot, procRoot, _ := resolveContainmentPaths(opts)
	attachLinuxContainment(ctx, p, cgroupRoot, procRoot, unitName, usedSystemd)
}

func reapPlatform(ctx context.Context, id Identity, clk clock.Clock, cgroupRoot, procRoot string) (StopProof, error) {
	if cgroupRoot == "" {
		cgroupRoot = defaultCgroupRoot
	}
	if procRoot == "" {
		procRoot = defaultProcRoot
	}
	if id.ScopeUnit != "" {
		return reapContainedScope(ctx, id.ScopeUnit, clk, cgroupRoot)
	}
	return reapByStartToken(ctx, id, clk, newProcfsScanner(procRoot))
}
