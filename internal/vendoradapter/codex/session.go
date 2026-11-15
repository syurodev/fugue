package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

// Session runs one supervised `codex app-server` CLI process.
type Session struct {
	sessionID       string
	threadID        string
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

	writeSem    chan struct{}
	mu          sync.Mutex
	state       sessionState
	currentTurn string
	codexTurnID string
	turnDoneCh  chan struct{}
	fatalErr    error

	pendingRPCs       map[string]chan Frame
	seenApprovals     map[string]bool
	pendingApprovals  map[string]pendingServerRequest
	resolvedApprovals map[string]bool

	closeOnce  sync.Once
	closeProof process.StopProof
	closeErr   error
}

var _ vendor.Session = (*Session)(nil)

// Observations returns the bounded channel of normalized stream observations.
func (s *Session) Observations() <-chan Observation {
	return s.obsCh
}

// SlashCommands returns the session's supported slash commands.
func (s *Session) SlashCommands() []string {
	return SlashCommandNames()
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

// ReadRateLimits sends an on-demand `account/rateLimits/read` request.
func (s *Session) ReadRateLimits(ctx context.Context) error {
	frame, err := s.callRPC(ctx, MethodRateLimitsRead, nil)
	if err != nil {
		return err
	}
	if frame.Error != nil {
		return errcode.Wrap(errcode.Internal, "read codex rate limits", frame.Error)
	}
	return nil
}

// Send delivers one user message or slash command to `codex app-server` and
// returns the new TurnRef.
func (s *Session) Send(ctx context.Context, in vendor.Input) (vendor.TurnRef, error) {
	if err := vendor.ValidateInput(in); err != nil {
		return vendor.TurnRef{}, errcode.Wrap(errcode.InvalidArgument, "validate input", err)
	}
	slashCall, isSlash, err := ParseSlashCommand(in.Text)
	if err != nil {
		return vendor.TurnRef{}, err
	}
	turnID, err := s.idGen.NewID()
	if err != nil {
		return vendor.TurnRef{}, errcode.Wrap(errcode.Internal, "generate turn id", err)
	}

	method, params, err := s.buildTurnRequest(in.Text, slashCall, isSlash)
	if err != nil {
		return vendor.TurnRef{}, err
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
	s.codexTurnID = ""
	s.turnDoneCh = make(chan struct{})
	s.normalizer.BeginTurn(turnID)
	s.mu.Unlock()

	frame, rpcErr := s.callRPC(ctx, method, params)
	if rpcErr != nil {
		s.rollbackTurn()
		return vendor.TurnRef{}, rpcErr
	}
	if frame.Error != nil {
		s.rollbackTurn()
		return vendor.TurnRef{}, errcode.Wrap(errcode.Internal, "codex turn request failed", frame.Error)
	}
	s.recordCodexTurnFromResponse(method, frame.Result)
	return vendor.TurnRef{ID: turnID}, nil
}

func (s *Session) buildTurnRequest(text string, slashCall SlashCommandCall, isSlash bool) (string, any, error) {
	s.mu.Lock()
	threadID := s.threadID
	model := s.model
	s.mu.Unlock()

	if !isSlash {
		return MethodTurnStart, TurnStartParams{
			ThreadID: threadID,
			Input:    []UserInputElement{NewTextInput(text)},
			Model:    model,
		}, nil
	}
	switch slashCall.Method {
	case MethodThreadCompactStart:
		return MethodThreadCompactStart, ThreadCompactStartParams{ThreadID: threadID}, nil
	case MethodReviewStart:
		return MethodReviewStart, ReviewStartParams{
			ThreadID: threadID,
			Target:   *slashCall.Target,
			Delivery: ReviewDeliveryInline,
		}, nil
	default:
		return "", nil, errcode.New(errcode.InvalidArgument, "unsupported codex slash command")
	}
}

func (s *Session) recordCodexTurnFromResponse(method string, result json.RawMessage) {
	switch method {
	case MethodTurnStart, MethodReviewStart:
		var resp TurnStartResponse
		if json.Unmarshal(result, &resp) == nil && resp.Turn.ID != "" {
			s.mu.Lock()
			if s.codexTurnID == "" {
				s.codexTurnID = resp.Turn.ID
			}
			s.mu.Unlock()
		}
	}
}

// Steer sends a user message while a turn is actively running (`turn/steer`).
func (s *Session) Steer(ctx context.Context, turn vendor.TurnRef, in vendor.Input) error {
	if err := vendor.ValidateInput(in); err != nil {
		return errcode.Wrap(errcode.InvalidArgument, "validate steer input", err)
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
	threadID := s.threadID
	expectedTurnID := s.activeCodexTurnLocked()
	s.mu.Unlock()

	frame, err := s.callRPC(ctx, MethodTurnSteer, TurnSteerParams{
		ThreadID:       threadID,
		ExpectedTurnID: expectedTurnID,
		Input:          []UserInputElement{NewTextInput(in.Text)},
	})
	if err != nil {
		return err
	}
	if frame.Error != nil {
		return errcode.Wrap(errcode.FailedPrecondition, "codex steer rejected", frame.Error)
	}
	return nil
}

// Interrupt sends `turn/interrupt` and awaits turn completion up to 10 s,
// escalating to the F9.2 stop order if the turn does not end in time.
func (s *Session) Interrupt(ctx context.Context, turn vendor.TurnRef) error {
	doneCh, threadID, codexTurnID, err := s.validateInterruptTurn(turn)
	if err != nil {
		return err
	}

	timer := s.clock.NewTimer(defaultInterruptTimeout)
	defer timer.Stop()

	_, raw, err := s.codec.NextRequest(MethodTurnInterrupt, TurnInterruptParams{
		ThreadID: threadID,
		TurnID:   codexTurnID,
	})
	if err != nil {
		return err
	}

	if acqErr := s.acquireInterruptWrite(ctx, timer); acqErr != nil {
		return acqErr
	}
	writeErr := s.proc.WriteLine(bytes.TrimRight(raw, "\r\n"))
	<-s.writeSem

	if writeErr != nil {
		slog.ErrorContext(ctx, "codex interrupt: write failed, closing session", "session_id", s.sessionID, "error", writeErr)
		if _, closeErr := s.Close(context.WithoutCancel(ctx), vendor.CloseHostStop); closeErr != nil {
			slog.WarnContext(ctx, "codex interrupt: close failed after write error", "session_id", s.sessionID, "error", closeErr)
		}
		return errcode.Wrap(errcode.Internal, "write turn/interrupt request", writeErr)
	}
	return s.awaitInterruptDone(ctx, doneCh, timer)
}

func (s *Session) validateInterruptTurn(turn vendor.TurnRef) (doneCh chan struct{}, threadID, codexTurnID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateClosed {
		return nil, "", "", s.closedErrLocked()
	}
	if s.state != stateRunning || s.currentTurn != turn.ID {
		return nil, "", "", vendor.ErrTurnNotRunning
	}
	return s.turnDoneCh, s.threadID, s.activeCodexTurnLocked(), nil
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
		return s.escalateInterrupt(ctx, "codex interrupt: write acquisition timed out, escalating to stop")
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "turn interrupt canceled", ctx.Err())
	}
}

func (s *Session) awaitInterruptDone(ctx context.Context, doneCh <-chan struct{}, timer clock.Timer) error {
	select {
	case <-doneCh:
		return nil
	case <-timer.C():
		return s.escalateInterrupt(ctx, "codex interrupt: turn did not finish within 10s, escalating to stop")
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "turn interrupt canceled", ctx.Err())
	}
}

func (s *Session) escalateInterrupt(ctx context.Context, msg string) error {
	slog.WarnContext(ctx, msg, "session_id", s.sessionID)
	if _, closeErr := s.Close(context.WithoutCancel(ctx), vendor.CloseHostStop); closeErr != nil {
		slog.WarnContext(ctx, "codex interrupt: close failed after timeout", "session_id", s.sessionID, "error", closeErr)
	}
	return errcode.New(errcode.DeadlineExceeded, "turn interrupt timed out; session stopped")
}

// Close idempotently closes stdin, runs the stop escalation order, and returns
// the resulting StopProof.
func (s *Session) Close(ctx context.Context, _ vendor.CloseReason) (process.StopProof, error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.rejectPendingApprovalsLocked()
		s.state = stateClosed
		if s.turnDoneCh != nil {
			close(s.turnDoneCh)
			s.turnDoneCh = nil
		}
		s.closeNotifyOnce.Do(func() { close(s.closeNotify) })
		if err := s.proc.CloseStdin(); err != nil {
			slog.WarnContext(ctx, "codex close: close stdin error", "session_id", s.sessionID, "error", err)
		}
		s.mu.Unlock()

		proof, err := s.proc.Stop(ctx)
		if err != nil {
			slog.WarnContext(ctx, "codex close: stop process error", "session_id", s.sessionID, "error", err)
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

func (s *Session) callRPC(ctx context.Context, method string, params any) (Frame, error) {
	ch, cid, raw, err := s.prepareRPC(method, params)
	if err != nil {
		return Frame{}, err
	}
	defer s.removePendingRPC(cid)

	if err := s.writeRawLine(ctx, raw); err != nil {
		return Frame{}, err
	}
	select {
	case frame := <-ch:
		return frame, nil
	case <-s.closeNotify:
		return Frame{}, s.closedErr()
	case <-s.readerDoneCh:
		return Frame{}, s.closedErr()
	case <-ctx.Done():
		return Frame{}, errcode.Wrap(errcode.Canceled, "codex rpc canceled", ctx.Err())
	}
}

func (s *Session) writeNotification(ctx context.Context, method string, params any) error {
	raw, err := s.codec.EncodeNotification(method, params)
	if err != nil {
		return err
	}
	return s.writeRawLine(ctx, raw)
}

func (s *Session) writeRawLine(ctx context.Context, raw []byte) error {
	select {
	case s.writeSem <- struct{}{}:
		defer func() { <-s.writeSem }()
	case <-s.closeNotify:
		return s.closedErr()
	case <-ctx.Done():
		return errcode.Wrap(errcode.Canceled, "codex write canceled", ctx.Err())
	}
	if err := s.proc.WriteLine(bytes.TrimRight(raw, "\r\n")); err != nil {
		if errors.Is(err, process.ErrStdinClosed) {
			return vendor.ErrSessionClosed
		}
		return errcode.Wrap(errcode.Internal, "write codex frame", err)
	}
	return nil
}

func (s *Session) activeCodexTurnLocked() string {
	if cTurn := s.normalizer.CodexTurnID(); cTurn != "" {
		return cTurn
	}
	if s.codexTurnID != "" {
		return s.codexTurnID
	}
	return s.currentTurn
}

func (s *Session) interruptTarget() (threadID, turnID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.threadID, s.activeCodexTurnLocked()
}

func (s *Session) rollbackTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateRunning {
		s.state = stateReady
		s.currentTurn = ""
		s.codexTurnID = ""
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
