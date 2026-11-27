package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/process"
)

// SubmitLoginCode relays one pasted browser login code to the running login
// process's stdin pipe (host-api v0.22 §5.5). Only the principal that started
// the login may submit, only while it runs, and only for an AcceptsCode vendor.
func (s *AccountsService) SubmitLoginCode(ctx context.Context, principal, commandID, loginID string, code redact.Secret) error {
	if !IsLocalPrincipal(principal) {
		return errcode.New(errcode.PermissionDenied, "only local principals may use accounts")
	}
	if commandID == "" {
		return errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	if loginID == "" {
		return errcode.New(errcode.InvalidArgument, "login_id is required")
	}
	if err := validateLoginCode(code); err != nil {
		return err
	}

	s.loginCmdMu.Lock()
	if prev, ok := s.loginDedup.Get(commandID); ok {
		s.loginCmdMu.Unlock()
		if prev.principal != principal {
			return errcode.New(errcode.PermissionDenied, "login belongs to another principal")
		}
		if prev.loginID != loginID {
			return errcode.New(errcode.InvalidArgument, "command_id was previously used with a different login_id")
		}
		return prev.err
	}
	if inf, ok := s.loginInflight[commandID]; ok {
		s.loginCmdMu.Unlock()
		return awaitLoginInflight(ctx, principal, loginID, inf)
	}
	inf := &inflightCommand[loginCodeResult]{done: make(chan struct{})}
	s.loginInflight[commandID] = inf
	s.loginCmdMu.Unlock()

	res := loginCodeResult{loginID: loginID, principal: principal}
	defer func() {
		s.loginCmdMu.Lock()
		delete(s.loginInflight, commandID)
		inf.result = res
		inf.err = res.err
		close(inf.done)
		s.loginCmdMu.Unlock()
	}()

	res.err = s.executeSubmitLoginCode(principal, loginID, code)
	s.loginDedup.Put(commandID, res)
	return res.err
}

func awaitLoginInflight(ctx context.Context, principal, loginID string, inf *inflightCommand[loginCodeResult]) error {
	select {
	case <-inf.done:
		if inf.result.principal != principal {
			return errcode.New(errcode.PermissionDenied, "login belongs to another principal")
		}
		if inf.result.loginID != loginID {
			return errcode.New(errcode.InvalidArgument, "command_id was previously used with a different login_id")
		}
		return inf.err
	case <-ctx.Done():
		return fmt.Errorf("context canceled: %w", ctx.Err())
	}
}

// validateLoginCode checks the length cap and printable-ASCII constraint on
// a temporary byte copy of code and zeroes that copy before returning (Go
// strings in the RPC request and redact.Secret cannot be zeroed).
func validateLoginCode(code redact.Secret) error {
	buf := []byte(code.Reveal())
	defer clear(buf)
	if len(buf) == 0 {
		return errcode.New(errcode.InvalidArgument, "login code is required")
	}
	if len(buf) > LoginCodeMaxBytes {
		return errcode.New(errcode.InvalidArgument, "login code exceeds 1 KiB limit")
	}
	for _, b := range buf {
		if b < 0x20 || b > 0x7e {
			return errcode.New(errcode.InvalidArgument, "login code must contain printable ASCII only")
		}
	}
	return nil
}

func (s *AccountsService) executeSubmitLoginCode(principal, loginID string, code redact.Secret) error {
	slot := s.runningLoginByID(loginID)
	if slot == nil {
		return errcode.New(errcode.FailedPrecondition, "login is not running")
	}
	if slot.principal != principal {
		return errcode.New(errcode.PermissionDenied, "login belongs to another principal")
	}
	if !slot.acceptsCode {
		return errcode.New(errcode.FailedPrecondition, "vendor login does not accept a pasted code")
	}
	slot.stdinMu.Lock()
	defer slot.stdinMu.Unlock()
	slot.mu.Lock()
	run := slot.run
	slot.mu.Unlock()
	if run == nil {
		return errcode.New(errcode.FailedPrecondition, "login process is not running")
	}
	if err := run.WriteCode(code); err != nil {
		if errors.Is(err, process.ErrStdinClosed) {
			return errcode.Wrap(errcode.FailedPrecondition, "login process is no longer accepting input", err)
		}
		return errcode.Wrap(errcode.CodeOf(err), "write login code to process", err)
	}
	return nil
}

func (s *AccountsService) runningLoginByID(loginID string) *loginRun {
	s.loginsMu.Lock()
	defer s.loginsMu.Unlock()
	for _, slot := range s.logins {
		if slot.id == loginID {
			return slot
		}
	}
	return nil
}
