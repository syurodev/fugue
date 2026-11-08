package process

import (
	"context"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// StopProofStatus classifies whether process termination is verified by an empty OS container.
type StopProofStatus string

const (
	// StopProofProved indicates the session's cgroup v2 scope was verified empty
	// (populated 0 in cgroup.events or an empty cgroup.procs).
	StopProofProved StopProofStatus = "proved"

	// StopProofUnknown indicates the process tree was signaled and waited on,
	// but the platform or session lacks a verified empty cgroup proof.
	StopProofUnknown StopProofStatus = "unknown"
)

// StopProof records the verification outcome and reason of a process tree shutdown.
type StopProof struct {
	Status StopProofStatus
	Reason string
}

// Identity uniquely identifies a spawned process session across host restarts.
// All three fields form the stable serializable representation persisted by
// vendorstore: pid integer, start_token text, and scope_unit text.
type Identity struct {
	PID        int    `json:"pid"`
	StartToken string `json:"start_token"`
	ScopeUnit  string `json:"scope_unit"`
}

// Options configures the launch and supervision of a vendor CLI process.
type Options struct {
	Argv         []string
	Cwd          string
	Env          []string
	Clock        clock.Clock
	Interrupt    func(context.Context) error
	OnEscalation func(step string)

	// SessionID names the systemd scope unit (`fugue-vendor-<id>.scope`) on Linux.
	// When empty, a unique scope identifier is generated from the process.
	SessionID string
	// MemoryMax is an optional systemd MemoryMax property (e.g. "4G") passed on Linux.
	MemoryMax string
	// CPUWeight is an optional systemd CPUWeight property (1..10000) passed on Linux.
	CPUWeight int

	// CgroupRoot overrides "/sys/fs/cgroup" on Linux (for testing).
	CgroupRoot string
	// SystemdRunPath overrides "systemd-run" lookup on Linux (for testing).
	SystemdRunPath string
	// ProcRoot overrides "/proc" on Linux (for testing).
	ProcRoot string
	// Probe caches the Linux systemd-run and cgroup v2 capability check across spawns.
	Probe *SystemdProbe
}

// ReapOptions configures host-restart reaping of a recorded process Identity.
type ReapOptions struct {
	Clock      clock.Clock
	CgroupRoot string
	ProcRoot   string
}
