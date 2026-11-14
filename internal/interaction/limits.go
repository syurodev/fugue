package interaction

import (
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// The default limits of interaction §9.
const (
	// MaxSummaryBytes bounds the summary of every request.
	MaxSummaryBytes = 2 * 1024
	// MaxRelayedDetailBytes bounds the full compact JSON input carried in Detail
	// for a relayed confirm request (interaction §9).
	MaxRelayedDetailBytes = 256 * 1024
	// MaxPromptBytes bounds the prompt content of a KindQuestion request:
	// the body and each question and header text.
	MaxPromptBytes = 8 * 1024
	// MaxAnswerBytes bounds the free text of a question answer and the
	// optional message of a relayed approval.
	MaxAnswerBytes = 16 * 1024
	// MaxNoteBytes bounds the deny note delivered with a denial.
	MaxNoteBytes = 4 * 1024
	// MaxOptions bounds the number of options per request or per question.
	MaxOptions = 20
	// MaxPendingNativePerSession bounds pending native requests per session;
	// the tool frontier of a session is sequential.
	MaxPendingNativePerSession = 1
	// MaxPendingPerHost bounds pending requests of every kind per host.
	MaxPendingPerHost = 64
)

// checkTextLimit fails typed when content exceeds its interaction §9 limit.
func checkTextLimit(content string, limit int, what string) error {
	if len(content) > limit {
		return errcode.Wrap(errcode.InvalidArgument, "interaction "+what+" exceeds its size limit", ErrTextTooLarge)
	}
	return nil
}

// addPendingLocked keeps admission checks constant-time between expiry deadlines.
func (o *Owner) addPendingLocked(ticket *Ticket) {
	o.pendingIDs[ticket.RequestID] = struct{}{}
	o.pendingCount++
	if !ticket.Relayed {
		o.pendingBySession[ticket.Binding.SessionID]++
	}
	if !ticket.ExpiresAt.IsZero() && (o.nextPendingExpiry.IsZero() || ticket.ExpiresAt.Before(o.nextPendingExpiry)) {
		o.nextPendingExpiry = ticket.ExpiresAt
	}
}

func (o *Owner) removePendingLocked(ticket *Ticket) {
	if _, ok := o.pendingIDs[ticket.RequestID]; !ok {
		return
	}
	delete(o.pendingIDs, ticket.RequestID)
	o.pendingCount--
	if ticket.Relayed {
		return
	}
	sessionID := ticket.Binding.SessionID
	if o.pendingBySession[sessionID] <= 1 {
		delete(o.pendingBySession, sessionID)
	} else {
		o.pendingBySession[sessionID]--
	}
}

func (o *Owner) expirePendingCountsLocked(now time.Time) {
	o.nextPendingExpiry = time.Time{}
	for requestID, ticket := range o.byRequest {
		if _, pending := o.pendingIDs[requestID]; !pending || ticket.ExpiresAt.IsZero() {
			continue
		}
		if !now.Before(ticket.ExpiresAt) {
			o.removePendingLocked(ticket)
			continue
		}
		if o.nextPendingExpiry.IsZero() || ticket.ExpiresAt.Before(o.nextPendingExpiry) {
			o.nextPendingExpiry = ticket.ExpiresAt
		}
	}
}

// checkPendingLimitsLocked enforces the pending-request limits of §9: at most
// one pending native request per session (the tool frontier of a session is
// sequential) and at most 64 pending requests per host. Relayed vendor
// requests ignore the per-session frontier and are bounded only by the host
// limit.
func (o *Owner) checkPendingLimitsLocked(spec *Spec, now time.Time) error {
	if !o.nextPendingExpiry.IsZero() && !now.Before(o.nextPendingExpiry) {
		o.expirePendingCountsLocked(now)
	}
	if o.pendingCount >= MaxPendingPerHost {
		return errcode.New(errcode.FailedPrecondition, "interaction pending request limit reached for the host")
	}
	if !spec.Relayed && o.pendingBySession[spec.Binding.SessionID] >= MaxPendingNativePerSession {
		return errcode.New(errcode.FailedPrecondition, "session already has a pending native interaction request")
	}
	return nil
}
