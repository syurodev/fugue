package process

import (
	"context"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// StopProofStatus classifies whether process termination is cryptographically/system proven.
type StopProofStatus string

const (
	// StopProofUnknown indicates the process tree was signaled and the parent exited,
	// but the platform does not provide a definitive empty-cgroup proof.
	StopProofUnknown StopProofStatus = "unknown"
)

// StopProof records the verification outcome and reason of a process tree shutdown.
type StopProof struct {
	Status StopProofStatus
	Reason string
}

// Options configures the launch and supervision of a vendor CLI process.
type Options struct {
	Argv         []string
	Cwd          string
	Env          []string
	Clock        clock.Clock
	Interrupt    func(context.Context) error
	OnEscalation func(step string)
}
