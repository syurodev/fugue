package coordinator

import "github.com/syurodev/fugue/internal/kernel"

func commandMetadata(c kernel.Command) (cmdID, runID, origin string, token uint64) {
	switch cmd := c.(type) {
	case *kernel.AcceptRunCommand:
		return cmd.CommandID, cmd.RunID, cmd.Origin, 0
	case *kernel.AdmitRunCommand:
		return cmd.CommandID, cmd.RunID, cmd.Origin, cmd.Token
	case *kernel.AdvanceRunCommand:
		return cmd.CommandID, cmd.RunID, cmd.Origin, cmd.Token
	case *kernel.PrepareDispatchCommand:
		return cmd.CommandID, cmd.RunID, cmd.Origin, cmd.Token
	case *kernel.ObserveModelStreamCommand:
		return cmd.CommandID, cmd.RunID, cmd.Origin, cmd.Token
	case *kernel.ObserveModelResponseCommand:
		return cmd.CommandID, cmd.RunID, cmd.Origin, cmd.Token
	case *kernel.ObserveToolCheckCommand:
		return cmd.CommandID, cmd.RunID, cmd.Origin, cmd.Token
	case *kernel.ObserveToolResultCommand:
		return cmd.CommandID, cmd.RunID, cmd.Origin, cmd.Token
	default:
		return "", "", "", 0
	}
}

func setCommandMetadata(c kernel.Command, cmdID, runID, origin string, token uint64) {
	switch cmd := c.(type) {
	case *kernel.AcceptRunCommand:
		if cmdID != "" {
			cmd.CommandID = cmdID
		}
		if runID != "" {
			cmd.RunID = runID
		}
		if origin != "" {
			cmd.Origin = origin
		}
	case *kernel.AdmitRunCommand:
		if cmdID != "" {
			cmd.CommandID = cmdID
		}
		if runID != "" {
			cmd.RunID = runID
		}
		if origin != "" {
			cmd.Origin = origin
		}
		if token != 0 {
			cmd.Token = token
		}
	case *kernel.AdvanceRunCommand:
		if cmdID != "" {
			cmd.CommandID = cmdID
		}
		if runID != "" {
			cmd.RunID = runID
		}
		if origin != "" {
			cmd.Origin = origin
		}
		if token != 0 {
			cmd.Token = token
		}
	case *kernel.PrepareDispatchCommand:
		if cmdID != "" {
			cmd.CommandID = cmdID
		}
		if runID != "" {
			cmd.RunID = runID
		}
		if origin != "" {
			cmd.Origin = origin
		}
		if token != 0 {
			cmd.Token = token
		}
	case *kernel.ObserveModelStreamCommand:
		if cmdID != "" {
			cmd.CommandID = cmdID
		}
		if runID != "" {
			cmd.RunID = runID
		}
		if origin != "" {
			cmd.Origin = origin
		}
		if token != 0 {
			cmd.Token = token
		}
	case *kernel.ObserveModelResponseCommand:
		if cmdID != "" {
			cmd.CommandID = cmdID
		}
		if runID != "" {
			cmd.RunID = runID
		}
		if origin != "" {
			cmd.Origin = origin
		}
		if token != 0 {
			cmd.Token = token
		}
	case *kernel.ObserveToolCheckCommand:
		if cmdID != "" {
			cmd.CommandID = cmdID
		}
		if runID != "" {
			cmd.RunID = runID
		}
		if origin != "" {
			cmd.Origin = origin
		}
		if token != 0 {
			cmd.Token = token
		}
	case *kernel.ObserveToolResultCommand:
		if cmdID != "" {
			cmd.CommandID = cmdID
		}
		if runID != "" {
			cmd.RunID = runID
		}
		if origin != "" {
			cmd.Origin = origin
		}
		if token != 0 {
			cmd.Token = token
		}
	}
}
