package agy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

const (
	defaultInterruptTimeout = 10 * time.Second
	observationBufferSize   = 256
)

type sessionState int

const (
	stateStarting sessionState = iota
	stateReady
	stateRunning
	stateClosed
)

// Session runs one supervised `agy acp` CLI process.
type Session struct {
	sessionID       string
	acpSessionID    string
	model           string
	cwd             string
	proc            *process.Process
	codec           *Codec
	normalizer      *Normalizer
	clock           clock.Clock
	idGen           ids.Generator
	obsCh           chan Observation
	closeNotify     chan struct{}
	closeNotifyOnce sync.Once
	readerDoneCh    chan struct{}

	writeSem      chan struct{}
	mu            sync.Mutex
	state         sessionState
	currentTurn   string
	turnDoneCh    chan struct{}
	fatalErr      error
	handshakeCaps HandshakeCapabilities
	models        *SessionModelsState
	modes         *SessionModeState
	configOptions []ConfigOption

	pendingRPCs map[string]chan Frame

	closeOnce  sync.Once
	closeProof process.StopProof
	closeErr   error
}

var _ vendor.Session = (*Session)(nil)

// Observations returns the bounded channel of normalized stream observations.
func (s *Session) Observations() <-chan Observation {
	return s.obsCh
}

// HandshakeCapabilities returns the §5.3 capabilities recorded during initialize.
func (s *Session) HandshakeCapabilities() HandshakeCapabilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handshakeCaps
}

// Models returns the session's model list state read from session/new or session/load.
func (s *Session) Models() *SessionModelsState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.models
}

// Modes returns the session's mode state read from session/new or session/load.
func (s *Session) Modes() *SessionModeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.modes
}

// ConfigOptions returns the session's config options read from session/new or session/load.
func (s *Session) ConfigOptions() []ConfigOption {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.configOptions)
}

// PID returns the OS process ID of the supervised CLI.
func (s *Session) PID() int {
	return s.proc.PID()
}

// Identity returns the persistent process identity for crash recovery.
func (s *Session) Identity() process.Identity {
	return s.proc.Identity()
}

// OwnsPID reports whether pid belongs to this session's process tree.
func (s *Session) OwnsPID(pid int) (bool, error) {
	owned, err := s.proc.OwnsPID(pid)
	if err != nil {
		return false, errcode.Wrap(errcode.Internal, "read the session process tree", err)
	}
	return owned, nil
}

// TreeEmpty reports whether this session's process containment is empty.
func (s *Session) TreeEmpty() (bool, error) {
	empty, err := s.proc.TreeEmpty()
	if err != nil {
		return false, errcode.Wrap(errcode.Internal, "check whether the session process tree is empty", err)
	}
	return empty, nil
}

// StderrRing returns the 64 KiB ring buffer capturing raw process stderr.
func (s *Session) StderrRing() *process.RingBuffer {
	return s.proc.StderrRing()
}

// Send delivers one user prompt via ACP `session/prompt` and returns TurnRef.
func (s *Session) Send(ctx context.Context, in vendor.Input) (vendor.TurnRef, error) {
	if err := vendor.ValidateInput(in); err != nil {
		return vendor.TurnRef{}, errcode.Wrap(errcode.CodeOf(err), "validate input", err)
	}
	turnID, err := s.idGen.NewID()
	if err != nil {
		return vendor.TurnRef{}, errcode.Wrap(errcode.Internal, "generate turn id", err)
	}

	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return vendor.TurnRef{}, s.closedErr()
	}
	if s.state != stateReady {
		s.mu.Unlock()
		return vendor.TurnRef{}, vendor.ErrBusy
	}
	acpSID := s.acpSessionID
	reqID, raw, encErr := s.codec.NextRequest(MethodSessionPrompt, PromptParams{
		SessionID: acpSID,
		Prompt:    []ContentBlock{{Type: "text", Text: in.Text}},
	})
	if encErr != nil {
		s.mu.Unlock()
		return vendor.TurnRef{}, encErr
	}
	cid := strconv.FormatInt(reqID, 10)
	s.state = stateRunning
	s.currentTurn = turnID
	s.turnDoneCh = make(chan struct{})
	s.normalizer.BeginTurn(turnID, cid)
	s.mu.Unlock()

	if err := s.writeRawLine(ctx, raw); err != nil {
		s.rollbackTurn()
		return vendor.TurnRef{}, err
	}
	return vendor.TurnRef{ID: turnID}, nil
}

// Interrupt sends the ACP `session/cancel` notification and awaits turn
// completion up to 10 s, escalating to the F9.2 stop order on timeout.
func (s *Session) Interrupt(ctx context.Context, turn vendor.TurnRef) error {
	doneCh, acpSID, err := s.validateInterruptTurn(turn)
	if err != nil {
		return err
	}

	timer := s.clock.NewTimer(defaultInterruptTimeout)
	defer timer.Stop()

	raw, err := s.codec.EncodeNotification(MethodSessionCancel, CancelParams{SessionID: acpSID})
	if err != nil {
		return err
	}
	if acqErr := s.acquireInterruptWrite(ctx, timer); acqErr != nil {
		return acqErr
	}
	writeErr := s.proc.WriteLine(bytes.TrimRight(raw, "\r\n"))
	<-s.writeSem

	if writeErr != nil {
		slog.ErrorContext(ctx, "agy interrupt: write failed, closing session", "session_id", s.sessionID, "error", writeErr)
		if _, closeErr := s.Close(context.WithoutCancel(ctx), vendor.CloseHostStop); closeErr != nil {
			slog.WarnContext(ctx, "agy interrupt: close failed after write error", "session_id", s.sessionID, "error", closeErr)
		}
		return errcode.Wrap(errcode.Internal, "write session/cancel notification", writeErr)
	}
	return s.awaitInterruptDone(ctx, doneCh, timer)
}

// Steer is unsupported by ACP/Antigravity (vendor-runtime §6).
func (s *Session) Steer(_ context.Context, _ vendor.TurnRef, _ vendor.Input) error {
	return vendor.ErrUnsupported
}

// Resolve is unsupported in F9.8a; F9.8b adds verbatim permission relay.
func (s *Session) Resolve(_ context.Context, _ vendor.ApprovalRef, _ vendor.Decision) error {
	return vendor.ErrUnsupported
}

func (s *Session) validateInterruptTurn(turn vendor.TurnRef) (doneCh chan struct{}, acpSID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateClosed {
		return nil, "", s.closedErrLocked()
	}
	if s.state != stateRunning || s.currentTurn != turn.ID {
		return nil, "", vendor.ErrTurnNotRunning
	}
	return s.turnDoneCh, s.acpSessionID, nil
}

func (s *Session) acquireInterruptWrite(ctx context.Context, timer clock.Timer) error {
	select {
	case s.writeSem <- struct{}{}:
		return nil
	default:
	}
	select {
	case s.writeSem <- struct{}{}:
		return nil
	case <-timer.C():
		return s.escalateInterrupt(ctx, "agy interrupt: write acquisition timed out, escalating to stop")
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "turn interrupt canceled", ctx.Err())
	}
}

func (s *Session) awaitInterruptDone(ctx context.Context, doneCh <-chan struct{}, timer clock.Timer) error {
	select {
	case <-doneCh:
		return nil
	case <-timer.C():
		return s.escalateInterrupt(ctx, "agy interrupt: turn did not finish within 10s, escalating to stop")
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "turn interrupt canceled", ctx.Err())
	}
}

func (s *Session) escalateInterrupt(ctx context.Context, msg string) error {
	slog.WarnContext(ctx, msg, "session_id", s.sessionID)
	if _, closeErr := s.Close(context.WithoutCancel(ctx), vendor.CloseHostStop); closeErr != nil {
		slog.WarnContext(ctx, "agy interrupt: close failed after timeout", "session_id", s.sessionID, "error", closeErr)
	}
	return errcode.New(errcode.DeadlineExceeded, "turn interrupt timed out; session stopped")
}

// Close idempotently closes stdin, runs the stop escalation order, and returns
// the resulting StopProof.
func (s *Session) Close(ctx context.Context, _ vendor.CloseReason) (process.StopProof, error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.state = stateClosed
		if s.turnDoneCh != nil {
			close(s.turnDoneCh)
			s.turnDoneCh = nil
		}
		s.closeNotifyOnce.Do(func() { close(s.closeNotify) })
		if err := s.proc.CloseStdin(); err != nil {
			slog.WarnContext(ctx, "agy close: close stdin error", "session_id", s.sessionID, "error", err)
		}
		s.mu.Unlock()

		proof, err := s.proc.Stop(ctx)
		if err != nil {
			slog.WarnContext(ctx, "agy close: stop process error", "session_id", s.sessionID, "error", err)
		}
		s.closeProof = proof
		s.closeErr = err

		<-s.readerDoneCh
	})
	return s.closeProof, s.closeErr
}

func (s *Session) prepareRPC(method string, params any) (ch chan Frame, cid string, raw []byte, err error) {
	id, raw, err := s.codec.NextRequest(method, params)
	if err != nil {
		return nil, "", nil, err
	}
	cid = strconv.FormatInt(id, 10)
	ch = make(chan Frame, 1)
	s.mu.Lock()
	s.pendingRPCs[cid] = ch
	s.mu.Unlock()
	return ch, cid, raw, nil
}

func (s *Session) removePendingRPC(cid string) {
	s.mu.Lock()
	delete(s.pendingRPCs, cid)
	s.mu.Unlock()
}

func (s *Session) writeRawLine(ctx context.Context, raw []byte) error {
	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-s.closeNotify:
		return s.closedErr()
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "agy write canceled", ctx.Err())
	}
	if err := s.proc.WriteLine(bytes.TrimRight(raw, "\r\n")); err != nil {
		if errors.Is(err, process.ErrStdinClosed) {
			return vendor.ErrSessionClosed
		}
		return errcode.Wrap(errcode.Internal, "write agy frame", err)
	}
	return nil
}

func (s *Session) sendCancelNotification(acpSID string) error {
	raw, err := s.codec.EncodeNotification(MethodSessionCancel, CancelParams{SessionID: acpSID})
	if err != nil {
		return err
	}
	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	default:
		return nil
	}
	if writeErr := s.proc.WriteLine(bytes.TrimRight(raw, "\r\n")); writeErr != nil {
		if errors.Is(writeErr, process.ErrStdinClosed) {
			return process.ErrStdinClosed
		}
		return errcode.Wrap(errcode.Internal, "write stop cancel notification", writeErr)
	}
	return nil
}

func (s *Session) acpID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acpSessionID
}

func (s *Session) rollbackTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateRunning {
		s.state = stateReady
		s.currentTurn = ""
		if s.turnDoneCh != nil {
			close(s.turnDoneCh)
			s.turnDoneCh = nil
		}
		s.normalizer.RollbackTurn()
	}
}

func (s *Session) closedErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closedErrLocked()
}

func (s *Session) closedErrLocked() error {
	if s.fatalErr != nil {
		return s.fatalErr
	}
	return vendor.ErrSessionClosed
}
