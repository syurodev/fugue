package interaction

import (
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// The default limits of interaction §9.
const (
	// MaxSummaryBytes bounds the summary of every request.
	MaxSummaryBytes = 2 * 1024
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
		return errcode.New(errcode.InvalidArgument, "interaction "+what+" exceeds its size limit")
	}
	return nil
}

// checkPendingLimitsLocked enforces the pending-request limits of §9: at most
// one pending native request per session (the tool frontier of a session is
// sequential) and at most 64 pending requests per host. Relayed vendor
// requests ignore the per-session frontier and are bounded only by the host
// limit.
func (o *Owner) checkPendingLimitsLocked(spec *Spec, now time.Time) error {
	pending := 0
	nativeForSession := 0
	for _, ticket := range o.byRequest {
		if ticket.Resolved || (!ticket.ExpiresAt.IsZero() && !now.Before(ticket.ExpiresAt)) {
			continue
		}
		pending++
		if !ticket.Relayed && ticket.Binding.SessionID == spec.Binding.SessionID {
			nativeForSession++
		}
	}
	if pending >= MaxPendingPerHost {
		return errcode.New(errcode.FailedPrecondition, "interaction pending request limit reached for the host")
	}
	if !spec.Relayed && nativeForSession >= MaxPendingNativePerSession {
		return errcode.New(errcode.FailedPrecondition, "session already has a pending native interaction request")
	}
	return nil
}
