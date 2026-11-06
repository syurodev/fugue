package coordinator

import (
	"fmt"

	"github.com/syurodev/fugue/internal/kernel"
)

// originWorker is the envelope origin of every worker observation. It is fixed
// by the coordinator and never read from the observation.
const originWorker = "worker"

// errUnsupportedCommand is what the kernel itself says about a command it does
// not decide, so a caller sees one answer whichever layer refuses first.
var errUnsupportedCommand = fmt.Errorf("coordinator: %w", kernel.Reject(kernel.RejectionCodeUnrecognizedCommand, "unsupported or nil command"))

// identityOf reads the caller-supplied identity of one command type; a nil
// command has none.
func identityOf[T any](v *T, get func(*T) (cmdID, runID string, token uint64)) (string, string, uint64) {
	if v == nil {
		return "", "", 0
	}
	return get(v)
}

// commandIdentity returns the caller-supplied command ID, run ID and token of a
// supported command. The origin is deliberately not readable: the coordinator
// takes it only from the trusted Request.Origin or from the worker path.
func commandIdentity(c kernel.Command) (cmdID, runID string, token uint64) {
	if cmdID, runID, token, ok := observationIdentity(c); ok {
		return cmdID, runID, token
	}
	if cmdID, runID, token, ok := hostCommandIdentity(c); ok {
		return cmdID, runID, token
	}
	return "", "", 0
}

func observationIdentity(c kernel.Command) (string, string, uint64, bool) {
	switch v := c.(type) {
	case *kernel.ObserveModelStreamCommand:
		id, r, tok := identityOf(v, func(c *kernel.ObserveModelStreamCommand) (string, string, uint64) {
			return c.CommandID, c.RunID, c.Token
		})
		return id, r, tok, true
	case *kernel.ObserveModelResponseCommand:
		id, r, tok := identityOf(v, func(c *kernel.ObserveModelResponseCommand) (string, string, uint64) {
			return c.CommandID, c.RunID, c.Token
		})
		return id, r, tok, true
	case *kernel.ObserveModelFailureCommand:
		id, r, tok := identityOf(v, func(c *kernel.ObserveModelFailureCommand) (string, string, uint64) {
			return c.CommandID, c.RunID, c.Token
		})
		return id, r, tok, true
	case *kernel.ObserveToolCheckCommand:
		id, r, tok := identityOf(v, func(c *kernel.ObserveToolCheckCommand) (string, string, uint64) {
			return c.CommandID, c.RunID, c.Token
		})
		return id, r, tok, true
	case *kernel.ObserveToolResultCommand:
		id, r, tok := identityOf(v, func(c *kernel.ObserveToolResultCommand) (string, string, uint64) {
			return c.CommandID, c.RunID, c.Token
		})
		return id, r, tok, true
	}
	return "", "", 0, false
}

func hostCommandIdentity(c kernel.Command) (string, string, uint64, bool) {
	switch v := c.(type) {
	case *kernel.AcceptRunCommand:
		id, r, tok := identityOf(v, func(c *kernel.AcceptRunCommand) (string, string, uint64) { return c.CommandID, c.RunID, 0 })
		return id, r, tok, true
	case *kernel.AdmitRunCommand:
		id, r, tok := identityOf(v, func(c *kernel.AdmitRunCommand) (string, string, uint64) { return c.CommandID, c.RunID, c.Token })
		return id, r, tok, true
	case *kernel.AdvanceRunCommand:
		id, r, tok := identityOf(v, func(c *kernel.AdvanceRunCommand) (string, string, uint64) { return c.CommandID, c.RunID, c.Token })
		return id, r, tok, true
	case *kernel.PrepareDispatchCommand:
		id, r, tok := identityOf(v, func(c *kernel.PrepareDispatchCommand) (string, string, uint64) { return c.CommandID, c.RunID, c.Token })
		return id, r, tok, true
	case *kernel.ExtendBudgetCommand:
		id, r, tok := identityOf(v, func(c *kernel.ExtendBudgetCommand) (string, string, uint64) { return c.CommandID, c.RunID, c.Token })
		return id, r, tok, true
	case *kernel.RequestStopCommand:
		id, r, tok := identityOf(v, func(c *kernel.RequestStopCommand) (string, string, uint64) { return c.CommandID, c.RunID, c.Token })
		return id, r, tok, true
	case *kernel.TimerFiredCommand:
		id, r, tok := identityOf(v, func(c *kernel.TimerFiredCommand) (string, string, uint64) { return c.CommandID, c.RunID, 0 })
		return id, r, tok, true
	}
	return "", "", 0, false
}

// stampWith returns a copy of the command with set applied, so the caller's
// value is never modified.
func stampWith[T any, P interface {
	*T
	kernel.Command
}](v P, set func(*T)) (kernel.Command, error) {
	if v == nil {
		return nil, errUnsupportedCommand
	}
	cp := *v
	set(&cp)
	return P(&cp), nil
}

// stampCommand returns a copy of c carrying the coordinator-owned identity. A
// command type the coordinator does not support is rejected the way the kernel
// rejects it.
func stampCommand(c kernel.Command, cmdID, runID, origin string, token uint64) (kernel.Command, error) {
	if stamped, ok, err := stampObservation(c, cmdID, runID, origin, token); ok {
		return stamped, err
	}
	if stamped, ok, err := stampHostCommand(c, cmdID, runID, origin, token); ok {
		return stamped, err
	}
	return nil, errUnsupportedCommand
}

func stampObservation(c kernel.Command, cmdID, runID, origin string, token uint64) (kernel.Command, bool, error) {
	switch v := c.(type) {
	case *kernel.ObserveModelStreamCommand:
		res, err := stampWith(v, func(c *kernel.ObserveModelStreamCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
		return res, true, err
	case *kernel.ObserveModelResponseCommand:
		res, err := stampWith(v, func(c *kernel.ObserveModelResponseCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
		return res, true, err
	case *kernel.ObserveModelFailureCommand:
		res, err := stampWith(v, func(c *kernel.ObserveModelFailureCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
		return res, true, err
	case *kernel.ObserveToolCheckCommand:
		res, err := stampWith(v, func(c *kernel.ObserveToolCheckCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
		return res, true, err
	case *kernel.ObserveToolResultCommand:
		res, err := stampWith(v, func(c *kernel.ObserveToolResultCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
		return res, true, err
	}
	return nil, false, nil
}

func stampHostCommand(c kernel.Command, cmdID, runID, origin string, token uint64) (kernel.Command, bool, error) {
	switch v := c.(type) {
	case *kernel.AcceptRunCommand:
		res, err := stampWith(v, func(c *kernel.AcceptRunCommand) { c.CommandID, c.RunID, c.Origin = cmdID, runID, origin })
		return res, true, err
	case *kernel.AdmitRunCommand:
		res, err := stampWith(v, func(c *kernel.AdmitRunCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
		return res, true, err
	case *kernel.AdvanceRunCommand:
		res, err := stampWith(v, func(c *kernel.AdvanceRunCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
		return res, true, err
	case *kernel.PrepareDispatchCommand:
		res, err := stampWith(v, func(c *kernel.PrepareDispatchCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
		return res, true, err
	case *kernel.ExtendBudgetCommand:
		res, err := stampWith(v, func(c *kernel.ExtendBudgetCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
		return res, true, err
	case *kernel.RequestStopCommand:
		res, err := stampWith(v, func(c *kernel.RequestStopCommand) {
			c.CommandID, c.RunID, c.Origin, c.Token = cmdID, runID, origin, token
		})
		return res, true, err
	case *kernel.TimerFiredCommand:
		res, err := stampWith(v, func(c *kernel.TimerFiredCommand) {
			c.CommandID, c.RunID, c.Origin = cmdID, runID, origin
		})
		return res, true, err
	}
	return nil, false, nil
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
