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
		registered := false
		if frame.Kind == FrameServerRequest {
			if !s.handleServerRequestFrame(ctx, &frame, &registered) {
				continue
			}
		}
		obs := s.normalizer.NormalizeFrame(&frame)
		if registered && obs.ServerRequest == nil {
			s.rejectUnrelayedServerRequest(frame.CanonicalID, RPCErrorPermissionNotRelayed)
		}
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

func (s *Session) handleServerRequestFrame(ctx context.Context, frame *Frame, registered *bool) bool {
	if frame.Method != MethodSessionRequestPermission {
		s.rejectServerRequest(ctx, frame, RPCErrorMethodNotSupported, RPCErrorMethodNotSupportedByFugue)
		return true
	}
	if frame.CanonicalID == "" {
		if len(frame.ID) > 0 {
			s.rejectServerRequest(ctx, frame, RPCErrorServerError, RPCErrorPermissionNotRelayed)
		}
		return false
	}
	*registered = s.registerServerRequest(frame)
	return true
}

func (s *Session) drainStdout() {
	for {
		if _, err := s.codec.ReadFrame(s.proc.Stdout()); err != nil {
			return
		}
	}
}

func (s *Session) rejectServerRequest(ctx context.Context, frame *Frame, code int, message string) {
	raw, err := s.codec.EncodeResponseError(frame.ID, code, message)
	if err != nil {
		slog.WarnContext(ctx, "agy: encode server request error response failed", "session_id", s.sessionID, "method", frame.Method, "error", err)
		return
	}
	if writeErr := s.writeRawLine(ctx, raw); writeErr != nil {
		slog.DebugContext(ctx, "agy: write server request error response failed", "session_id", s.sessionID, "method", frame.Method, "error", writeErr)
	}
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
