package coordinator

import (
	"github.com/syurodev/fugue/internal/kernel"
)

// originWorker is the envelope origin of every worker observation. It is fixed
// by the coordinator and never read from the observation.
const originWorker = "worker"

// commandIdentity returns the caller-supplied command ID, run ID and token of a
// supported command. The origin is deliberately not readable: the coordinator
// takes it only from the trusted Request.Origin or from the worker path.
func commandIdentity(c kernel.Command) (cmdID, runID string, token uint64) {
	switch v := c.(type) {
	case *kernel.AcceptRunCommand:
		if v != nil {
			return v.CommandID, v.RunID, 0
		}
	case *kernel.AdmitRunCommand:
		if v != nil {
			return v.CommandID, v.RunID, v.Token
		}
	case *kernel.AdvanceRunCommand:
		if v != nil {
			return v.CommandID, v.RunID, v.Token
		}
	case *kernel.PrepareDispatchCommand:
		if v != nil {
			return v.CommandID, v.RunID, v.Token
		}
	case *kernel.ObserveModelStreamCommand:
		if v != nil {
			return v.CommandID, v.RunID, v.Token
		}
	case *kernel.ObserveModelResponseCommand:
		if v != nil {
			return v.CommandID, v.RunID, v.Token
		}
	case *kernel.ObserveModelFailureCommand:
		if v != nil {
			return v.CommandID, v.RunID, v.Token
		}
	case *kernel.ObserveToolCheckCommand:
		if v != nil {
			return v.CommandID, v.RunID, v.Token
		}
	case *kernel.ObserveToolResultCommand:
		if v != nil {
			return v.CommandID, v.RunID, v.Token
		}
	case *kernel.ExtendBudgetCommand:
		if v != nil {
			return v.CommandID, v.RunID, v.Token
		}
	case *kernel.RequestStopCommand:
		if v != nil {
			return v.CommandID, v.RunID, v.Token
		}
	}
	return "", "", 0
}

// stampWith returns a copy of the command with set applied, so the caller's
// value is never modified.
func stampWith[T any, P interface {
	*T
	kernel.Command
}](v P, set func(*T)) (kernel.Command, error) {
	if v == nil {
		return nil, kernel.Reject(kernel.RejectionCodeUnrecognizedCommand, "unsupported or nil command")
	}
	cp := *v
	set(&cp)
	return P(&cp), nil
}

// stampCommand returns a copy of c carrying the coordinator-owned identity. A
// command type the coordinator does not support is rejected the way the kernel
// rejects it.
func stampCommand(c kernel.Command, cmdID, runID, origin string, token uint64) (kernel.Command, error) {
	switch v := c.(type) {
	case *kernel.AcceptRunCommand:
		return stampWith(v, func(c *kernel.AcceptRunCommand) { c.CommandID, c.RunID, c.Origin = cmdID, runID, origin })
	case *kernel.AdmitRunCommand:
		return stampWith(v, func(c *kernel.AdmitRunCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
	case *kernel.AdvanceRunCommand:
		return stampWith(v, func(c *kernel.AdvanceRunCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
	case *kernel.PrepareDispatchCommand:
		return stampWith(v, func(c *kernel.PrepareDispatchCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
	case *kernel.ObserveModelStreamCommand:
		return stampWith(v, func(c *kernel.ObserveModelStreamCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
	case *kernel.ObserveModelResponseCommand:
		return stampWith(v, func(c *kernel.ObserveModelResponseCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
	case *kernel.ObserveModelFailureCommand:
		return stampWith(v, func(c *kernel.ObserveModelFailureCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
	case *kernel.ObserveToolCheckCommand:
		return stampWith(v, func(c *kernel.ObserveToolCheckCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
	case *kernel.ObserveToolResultCommand:
		return stampWith(v, func(c *kernel.ObserveToolResultCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
	case *kernel.ExtendBudgetCommand:
		return stampWith(v, func(c *kernel.ExtendBudgetCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
	case *kernel.RequestStopCommand:
		return stampWith(v, func(c *kernel.RequestStopCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
	}
	return nil, kernel.Reject(kernel.RejectionCodeUnrecognizedCommand, "unsupported or nil command")
}

// isWorkerObservation reports whether a worker may report c. Everything else is
// a host-bound command and never travels through a worker lease.
func isWorkerObservation(c kernel.Command) bool {
	switch c.(type) {
	case *kernel.ObserveModelStreamCommand,
		*kernel.ObserveModelResponseCommand,
		*kernel.ObserveModelFailureCommand,
		*kernel.ObserveToolCheckCommand,
		*kernel.ObserveToolResultCommand:
		return true
	}
	return false
}
