package claude

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

const (
	roleUser         = "user"
	contentTypeText  = "text"
	controlInterrupt = "interrupt"

	defaultHandshakeTimeout = 30 * time.Second
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

// Session runs one supervised Claude Code CLI process.
type Session struct {
	sessionID    string
	proc         *process.Process
	normalizer   *Normalizer
	clock        clock.Clock
	idGen        ids.Generator
	obsCh        chan Observation
	closeNotify  chan struct{}
	readerDoneCh chan struct{}

	writeSem    chan struct{}
	mu          sync.Mutex
	state       sessionState
	currentTurn string
	turnDoneCh  chan struct{}
	fatalErr    error

	closeOnce  sync.Once
	closeProof process.StopProof
	closeErr   error
}

var _ vendor.Session = (*Session)(nil)

type wireUserContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type wireUserMessagePayload struct {
	Role    string            `json:"role"`
	Content []wireUserContent `json:"content"`
}

type wireUserMessage struct {
	Type    string                 `json:"type"`
	UUID    string                 `json:"uuid,omitempty"`
	Message wireUserMessagePayload `json:"message"`
}

type wireControlSubtype struct {
	Subtype string `json:"subtype"`
}

type wireControlRequest struct {
	Type      string             `json:"type"`
	RequestID string             `json:"request_id"`
	Request   wireControlSubtype `json:"request"`
}

// Observations returns the bounded channel of normalized stream observations.
// The channel is closed when the session terminates.
func (s *Session) Observations() <-chan Observation {
	return s.obsCh
}

// SlashCommands returns the normalizer's latest vendor-order command list.
func (s *Session) SlashCommands() []string { return s.normalizer.SlashCommands() }

// PID returns the operating system process ID of the supervised CLI.
func (s *Session) PID() int {
	return s.proc.PID()
}

// Identity returns the persistent process identity for crash recovery.
func (s *Session) Identity() process.Identity {
	return s.proc.Identity()
}

// OwnsPID reports whether pid belongs to this session's process tree; an
// unreadable tree is an error and the caller fails closed (Q-41 A).
func (s *Session) OwnsPID(pid int) (bool, error) {
	owned, err := s.proc.OwnsPID(pid)
	if err != nil {
		return false, errcode.Wrap(errcode.Internal, "read the session process tree", err)
	}
	return owned, nil
}

// TreeEmpty reports whether this session's process containment is verified to
// have no remaining live processes.
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

func encodeUserMessage(msgUUID, text string) ([]byte, error) {
	msg := wireUserMessage{
		Type: roleUser,
		UUID: msgUUID,
		Message: wireUserMessagePayload{
			Role: roleUser,
			Content: []wireUserContent{
				{Type: contentTypeText, Text: text},
			},
		},
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "encode user message", err)
	}
	return raw, nil
}

// Send delivers one user message or slash command to the vendor process on
// stdin and returns the new TurnRef. It validates the input and returns
// vendor.ErrBusy if a turn is already in progress.
func (s *Session) Send(ctx context.Context, in vendor.Input) (vendor.TurnRef, error) {
	if err := vendor.ValidateInput(in); err != nil {
		return vendor.TurnRef{}, errcode.Wrap(errcode.InvalidArgument, "validate input", err)
	}

	turnID, err := s.idGen.NewID()
	if err != nil {
		return vendor.TurnRef{}, errcode.Wrap(errcode.Internal, "generate turn id", err)
	}
	msgUUID, err := s.idGen.NewID()
	if err != nil {
		return vendor.TurnRef{}, errcode.Wrap(errcode.Internal, "generate message uuid", err)
	}

	raw, err := encodeUserMessage(msgUUID, in.Text)
	if err != nil {
		return vendor.TurnRef{}, err
	}

	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-ctx.Done():
		return vendor.TurnRef{}, errcode.Wrap(errcode.Canceled, "send canceled", ctx.Err())
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

	s.state = stateRunning
	s.currentTurn = turnID
	s.turnDoneCh = make(chan struct{})
	s.normalizer.BeginTurn(turnID)
	s.normalizer.RegisterSend(turnID, msgUUID)
	s.mu.Unlock()

	if err := s.proc.WriteLine(raw); err != nil {
		s.normalizer.UnregisterSend(msgUUID)
		s.rollbackTurn()
		if errors.Is(err, process.ErrStdinClosed) {
			return vendor.TurnRef{}, vendor.ErrSessionClosed
		}
		return vendor.TurnRef{}, errcode.Wrap(errcode.Internal, "write user message", err)
	}

	return vendor.TurnRef{ID: turnID}, nil
}

// Steer sends a user message while a turn is actively running, without
// allocating a new turn ID.
func (s *Session) Steer(ctx context.Context, turn vendor.TurnRef, in vendor.Input) error {
	if err := vendor.ValidateInput(in); err != nil {
		return errcode.Wrap(errcode.InvalidArgument, "validate steer input", err)
	}

	steerUUID, err := s.idGen.NewID()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "generate steer uuid", err)
	}

	msg := wireUserMessage{
		Type: roleUser,
		UUID: steerUUID,
		Message: wireUserMessagePayload{
			Role: roleUser,
			Content: []wireUserContent{
				{Type: contentTypeText, Text: in.Text},
			},
		},
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "encode steer message", err)
	}

	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "steer canceled", ctx.Err())
	}

	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return s.closedErr()
	}
	if s.state != stateRunning || s.currentTurn != turn.ID {
		s.mu.Unlock()
		return vendor.ErrTurnNotRunning
	}
	s.normalizer.RegisterSteer(turn.ID, steerUUID)
	s.mu.Unlock()

	if err := s.proc.WriteLine(raw); err != nil {
		s.normalizer.UnregisterSteer(steerUUID)
		if errors.Is(err, process.ErrStdinClosed) {
			return vendor.ErrSessionClosed
		}
		return errcode.Wrap(errcode.Internal, "write steer message", err)
	}
	return nil
}

func (s *Session) escalateInterrupt(ctx context.Context, msg string) error {
	slog.WarnContext(ctx, msg, "session_id", s.sessionID)
	if _, closeErr := s.Close(context.WithoutCancel(ctx), vendor.CloseHostStop); closeErr != nil {
		slog.WarnContext(ctx, "claude interrupt: close failed after timeout", "session_id", s.sessionID, "error", closeErr)
	}
	return errcode.New(errcode.DeadlineExceeded, "turn interrupt timed out; session stopped")
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
		return s.escalateInterrupt(ctx, "claude interrupt: write acquisition timed out, escalating to stop")
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "turn interrupt canceled", ctx.Err())
	}
}

func (s *Session) validateInterruptTurn(turn vendor.TurnRef) (chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateClosed {
		return nil, s.closedErr()
	}
	if s.state != stateRunning || s.currentTurn != turn.ID {
		return nil, vendor.ErrTurnNotRunning
	}
	return s.turnDoneCh, nil
}

func (s *Session) awaitInterruptDone(ctx context.Context, doneCh <-chan struct{}, timer clock.Timer) error {
	select {
	case <-doneCh:
		return nil
	case <-timer.C():
		return s.escalateInterrupt(ctx, "claude interrupt: turn did not finish within 10s, escalating to stop")
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "turn interrupt canceled", ctx.Err())
	}
}

// Interrupt transmits a protocol control message interrupt on stdin and
// awaits turn conclusion up to 10 s. If the turn does not finish within 10 s,
// it escalates to the stop order and closes the session.
func (s *Session) Interrupt(ctx context.Context, turn vendor.TurnRef) error {
	ctrl := wireControlRequest{
		Type:      "control_request",
		RequestID: "req-interrupt-" + turn.ID,
		Request: wireControlSubtype{
			Subtype: controlInterrupt,
		},
	}
	raw, err := json.Marshal(ctrl)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "encode interrupt request", err)
	}

	if _, valErr := s.validateInterruptTurn(turn); valErr != nil {
		return valErr
	}

	timer := s.clock.NewTimer(defaultInterruptTimeout)
	defer timer.Stop()

	if acqErr := s.acquireInterruptWrite(ctx, timer); acqErr != nil {
		return acqErr
	}

	doneCh, err := s.validateInterruptTurn(turn)
	if err != nil {
		<-s.writeSem
		return err
	}

	writeErr := s.proc.WriteLine(raw)
	<-s.writeSem

	if writeErr != nil {
		slog.ErrorContext(ctx, "claude interrupt: write control message failed, closing session", "session_id", s.sessionID, "error", writeErr)
		if _, closeErr := s.Close(context.WithoutCancel(ctx), vendor.CloseHostStop); closeErr != nil {
			slog.WarnContext(ctx, "claude interrupt: close failed after write error", "session_id", s.sessionID, "error", closeErr)
		}
		return errcode.Wrap(errcode.Internal, "write interrupt control message", writeErr)
	}

	return s.awaitInterruptDone(ctx, doneCh, timer)
}

// Resolve answers a relayed approval request. In F9.5, before F9.6 introduces
// the MCP bridge, it always returns vendor.ErrUnsupported.
func (s *Session) Resolve(ctx context.Context, req vendor.ApprovalRef, d vendor.Decision) error {
	s.mu.Lock()
	closed := s.state == stateClosed
	s.mu.Unlock()
	if closed {
		return s.closedErr()
	}
	return vendor.ErrUnsupported
}

// Close idempotently closes stdin, executes the stop escalation order, and
// returns the resulting StopProof.
func (s *Session) Close(ctx context.Context, reason vendor.CloseReason) (process.StopProof, error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.state = stateClosed
		if s.turnDoneCh != nil {
			close(s.turnDoneCh)
			s.turnDoneCh = nil
		}
		s.mu.Unlock()

		close(s.closeNotify)
		if err := s.proc.CloseStdin(); err != nil {
			slog.WarnContext(ctx, "claude close: close stdin error", "session_id", s.sessionID, "error", err)
		}
		proof, err := s.proc.Stop(ctx)
		if err != nil {
			slog.WarnContext(ctx, "claude close: stop process error", "session_id", s.sessionID, "error", err)
		}
		s.closeProof = proof
		s.closeErr = err

		<-s.readerDoneCh
	})
	return s.closeProof, s.closeErr
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
	if s.fatalErr != nil {
		return s.fatalErr
	}
	return vendor.ErrSessionClosed
}

func (s *Session) runReader(ctx context.Context, initCh chan<- *Init, readerErrCh chan<- error) {
	defer close(s.readerDoneCh)
	defer close(s.obsCh)

	initReported := false
	lineNum := 0

	for {
		lineNum++
		line, err := s.proc.Stdout().ReadLine()
		if err != nil {
			s.handleReaderError(ctx, err, initReported, readerErrCh)
			return
		}

		obs, normErr := s.normalizer.Normalize(line)
		if normErr != nil {
			slog.DebugContext(ctx, "claude reader: normalize event ignored",
				"session_id", s.sessionID,
				"line_number", lineNum,
				"error", normErr)
			continue
		}

		s.handleObservation(&obs, &initReported, initCh)

		select {
		case s.obsCh <- obs:
		case <-s.closeNotify:
			return
		}
	}
}

func (s *Session) handleReaderError(ctx context.Context, err error, initReported bool, readerErrCh chan<- error) {
	s.mu.Lock()
	if errors.Is(err, process.ErrFrameTooLarge) {
		s.fatalErr = process.ErrFrameTooLarge
	}
	s.state = stateClosed
	if s.turnDoneCh != nil {
		close(s.turnDoneCh)
		s.turnDoneCh = nil
	}
	s.mu.Unlock()

	if !initReported {
		select {
		case readerErrCh <- err:
		default:
		}
	}

	if errors.Is(err, process.ErrFrameTooLarge) {
		if _, stopErr := s.proc.Stop(context.WithoutCancel(ctx)); stopErr != nil {
			slog.WarnContext(ctx, "claude reader: stop after frame too large failed", "session_id", s.sessionID, "error", stopErr)
		}
	}
}

func (s *Session) handleObservation(obs *Observation, initReported *bool, initCh chan<- *Init) {
	if obs.Init != nil && !*initReported {
		*initReported = true
		select {
		case initCh <- obs.Init:
		default:
		}
	}

	if obs.TurnEnd != nil {
		s.mu.Lock()
		if s.state == stateRunning && obs.TurnEnd.TurnID == s.currentTurn {
			s.state = stateReady
			s.currentTurn = ""
			if s.turnDoneCh != nil {
				close(s.turnDoneCh)
				s.turnDoneCh = nil
			}
		}
		s.mu.Unlock()
	}
}
