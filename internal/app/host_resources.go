package app

import "runtime"

// HostResources supplies host memory and CPU capacity to the scheduler.
// Tests inject a deterministic fake implementation; nil defaults to osHostResources.
type HostResources interface {
	TotalMemoryBytes() (uint64, error)
	CPUCores() int
}

type osHostResources struct{}

func (osHostResources) TotalMemoryBytes() (uint64, error) {
	return hostTotalMemoryBytes()
}

func (osHostResources) CPUCores() int {
	return max(1, runtime.NumCPU())
}

func resolveHostResources(r HostResources) HostResources {
	if r != nil {
		return r
	}
	return osHostResources{}
}
