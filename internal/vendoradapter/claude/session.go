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

	writeMu     sync.Mutex
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

// PID returns the operating system process ID of the supervised CLI.
func (s *Session) PID() int {
	return s.proc.PID()
}

// Identity returns the persistent process identity for crash recovery.
func (s *Session) Identity() process.Identity {
	return s.proc.Identity()
}

// StderrRing returns the 64 KiB ring buffer capturing raw process stderr.
func (s *Session) StderrRing() *process.RingBuffer {
	return s.proc.StderrRing()
}

// Send delivers one user message or slash command to the vendor process on
// stdin and returns the new TurnRef. It validates the input and returns
// vendor.ErrBusy if a turn is already in progress.
func (s *Session) Send(ctx context.Context, in vendor.Input) (vendor.TurnRef, error) {
	if err := vendor.ValidateInput(in); err != nil {
		return vendor.TurnRef{}, errcode.Wrap(errcode.InvalidArgument, "validate input", err)
	}

	msg := wireUserMessage{
		Type: roleUser,
		Message: wireUserMessagePayload{
			Role: roleUser,
			Content: []wireUserContent{
				{Type: contentTypeText, Text: in.Text},
			},
		},
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return vendor.TurnRef{}, errcode.Wrap(errcode.Internal, "encode user message", err)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return vendor.TurnRef{}, s.closedErr()
	}
	if s.state != stateReady {
		s.mu.Unlock()
		return vendor.TurnRef{}, vendor.ErrBusy
	}

	turnID, err := s.idGen.NewID()
	if err != nil {
		s.mu.Unlock()
		return vendor.TurnRef{}, errcode.Wrap(errcode.Internal, "generate turn id", err)
	}

	s.state = stateRunning
	s.currentTurn = turnID
	s.turnDoneCh = make(chan struct{})
	s.normalizer.BeginTurn(turnID)
	s.mu.Unlock()

	if err := s.proc.WriteLine(raw); err != nil {
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

	msg := wireUserMessage{
		Type: roleUser,
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

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		return s.closedErr()
	}
	if s.state != stateRunning || s.currentTurn != turn.ID {
		s.mu.Unlock()
		return vendor.ErrTurnNotRunning
	}
	s.mu.Unlock()

	if err := s.proc.WriteLine(raw); err != nil {
		if errors.Is(err, process.ErrStdinClosed) {
			return vendor.ErrSessionClosed
		}
		return errcode.Wrap(errcode.Internal, "write steer message", err)
	}
	return nil
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

	s.writeMu.Lock()
	s.mu.Lock()
	if s.state == stateClosed {
		s.mu.Unlock()
		s.writeMu.Unlock()
		return s.closedErr()
	}
	if s.state != stateRunning || s.currentTurn != turn.ID {
		s.mu.Unlock()
		s.writeMu.Unlock()
		return vendor.ErrTurnNotRunning
	}
	doneCh := s.turnDoneCh
	s.mu.Unlock()

	writeErr := s.proc.WriteLine(raw)
	s.writeMu.Unlock()

	if writeErr != nil {
		slog.Error("claude interrupt: write control message failed, closing session", "session_id", s.sessionID, "error", writeErr)
		if _, closeErr := s.Close(context.WithoutCancel(ctx), vendor.CloseHostStop); closeErr != nil {
			slog.Warn("claude interrupt: close failed after write error", "session_id", s.sessionID, "error", closeErr)
		}
		return errcode.Wrap(errcode.Internal, "write interrupt control message", writeErr)
	}

	timer := s.clock.NewTimer(defaultInterruptTimeout)
	defer timer.Stop()

	select {
	case <-doneCh:
		return nil
	case <-timer.C():
		slog.Warn("claude interrupt: turn did not finish within 10s, escalating to stop", "session_id", s.sessionID)
		if _, closeErr := s.Close(context.WithoutCancel(ctx), vendor.CloseHostStop); closeErr != nil {
			slog.Warn("claude interrupt: close failed after timeout", "session_id", s.sessionID, "error", closeErr)
		}
		return errcode.New(errcode.DeadlineExceeded, "turn interrupt timed out; session stopped")
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "turn interrupt canceled", ctx.Err())
	}
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
			slog.Warn("claude close: close stdin error", "session_id", s.sessionID, "error", err)
		}
		proof, err := s.proc.Stop(ctx)
		if err != nil {
			slog.Warn("claude close: stop process error", "session_id", s.sessionID, "error", err)
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

	for {
		line, err := s.proc.Stdout().ReadLine()
		if err != nil {
			s.handleReaderError(ctx, err, initReported, readerErrCh)
			return
		}

		obs, normErr := s.normalizer.Normalize(line)
		if normErr != nil {
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
		if _, stopErr := s.proc.Stop(ctx); stopErr != nil {
			slog.Warn("claude reader: stop after frame too large failed", "session_id", s.sessionID, "error", stopErr)
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
		if s.state == stateRunning {
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
