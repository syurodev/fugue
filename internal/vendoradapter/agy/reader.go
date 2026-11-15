package agy

import (
	"context"
	"errors"
	"log/slog"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
)

func (s *Session) runReader(ctx context.Context, readerErrCh chan<- error) {
	defer close(s.readerDoneCh)
	defer close(s.obsCh)

	for {
		frame, err := s.codec.ReadFrame(s.proc.Stdout())
		if err != nil {
			s.handleReaderError(ctx, err, readerErrCh)
			return
		}
		if frame.Kind == FrameServerRequest {
			s.rejectServerRequest(ctx, &frame)
		}
		obs := s.normalizer.NormalizeFrame(&frame)
		s.dispatchFrameAndObservation(&frame, &obs)
		if isEmptyObservation(&obs) {
			continue
		}
		select {
		case s.obsCh <- obs:
		case <-s.closeNotify:
			s.drainStdout()
			return
		}
	}
}

func (s *Session) drainStdout() {
	for {
		if _, err := s.codec.ReadFrame(s.proc.Stdout()); err != nil {
			return
		}
	}
}

func (s *Session) rejectServerRequest(ctx context.Context, frame *Frame) {
	raw, err := s.codec.EncodeResponseError(frame.ID, RPCErrorMethodNotSupported, serverRequestRejectMessage(frame.Method))
	if err != nil {
		slog.WarnContext(ctx, "agy: encode server request error response failed", "session_id", s.sessionID, "method", frame.Method, "error", err)
		return
	}
	if writeErr := s.writeRawLine(ctx, raw); writeErr != nil {
		slog.DebugContext(ctx, "agy: write server request error response failed", "session_id", s.sessionID, "method", frame.Method, "error", writeErr)
	}
}

func serverRequestRejectMessage(method string) string {
	if method == MethodSessionRequestPermission {
		return RPCErrorPermissionNotRelayed
	}
	return RPCErrorMethodNotSupportedByFugue
}

func (s *Session) dispatchFrameAndObservation(frame *Frame, obs *Observation) {
	if frame.Kind == FrameResponse && frame.CanonicalID != "" {
		s.mu.Lock()
		ch := s.pendingRPCs[frame.CanonicalID]
		s.mu.Unlock()
		if ch != nil {
			select {
			case ch <- *frame:
			default:
			}
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

func isEmptyObservation(obs *Observation) bool {
	return obs.Init == nil &&
		len(obs.Records) == 0 &&
		len(obs.Updates) == 0 &&
		obs.Partial == nil &&
		obs.TurnEnd == nil &&
		obs.Quota == nil &&
		obs.ServerRequest == nil &&
		obs.Nested == 0
}

func (s *Session) handleReaderError(ctx context.Context, err error, readerErrCh chan<- error) {
	s.mu.Lock()
	if errors.Is(err, process.ErrFrameTooLarge) || errcode.CodeOf(err) == errcode.FrameTooLarge {
		s.fatalErr = process.ErrFrameTooLarge
	}
	s.state = stateClosed
	if s.turnDoneCh != nil {
		close(s.turnDoneCh)
		s.turnDoneCh = nil
	}
	s.mu.Unlock()

	select {
	case readerErrCh <- err:
	default:
	}

	if errors.Is(err, process.ErrFrameTooLarge) || errcode.CodeOf(err) == errcode.FrameTooLarge {
		if _, stopErr := s.proc.Stop(context.WithoutCancel(ctx)); stopErr != nil {
			slog.WarnContext(ctx, "agy reader: stop after frame too large failed", "session_id", s.sessionID, "error", stopErr)
		}
	}
}
