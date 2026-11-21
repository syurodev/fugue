package app

import "runtime"

// HostResources supplies host memory and CPU capacity to the scheduler.
// Tests inject a deterministic fake implementation; nil defaults to osHostResources.
type HostResources interface {
	TotalMemoryBytes() (uint64, error)
	CPUCores() int
}

// DiskSpaceChecker reports the free space of the filesystem holding a path
// (F11.9, §12). Tests inject a deterministic fake; nil defaults to
// osHostResources.
type DiskSpaceChecker interface {
	FreeDiskBytes(path string) (uint64, error)
}

type osHostResources struct{}

func (osHostResources) TotalMemoryBytes() (uint64, error) {
	return hostTotalMemoryBytes()
}

func (osHostResources) CPUCores() int {
	return max(1, runtime.NumCPU())
}

func (osHostResources) FreeDiskBytes(path string) (uint64, error) {
	return hostFreeDiskBytes(path)
}

func resolveHostResources(r HostResources) HostResources {
	if r != nil {
		return r
	}
	return osHostResources{}
}

// resolveDiskSpace binds the disk port; osHostResources implements it so the
// default follows HostResources when no explicit port is injected.
func resolveDiskSpace(d DiskSpaceChecker) DiskSpaceChecker {
	if d != nil {
		return d
	}
	return osHostResources{}
}
