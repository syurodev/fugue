package interaction

import (
	"slices"
	"time"
)

// effectiveOffers reports the effective offers for a spec: relayed requests
// permit only answered or denied (interaction §7).
func effectiveOffers(spec *Spec) []Outcome {
	if spec.Relayed {
		return []Outcome{OutcomeAnswered, OutcomeDenied}
	}
	return spec.Offers
}

func sameSpec(t *Ticket, spec *Spec) bool {
	if t.Kind != spec.Kind || t.Risk != spec.Risk || t.GrantScopeLabel != spec.GrantScopeLabel ||
		t.Binding != spec.Binding || t.Relayed != spec.Relayed ||
		t.Summary != spec.Summary || t.Body != spec.Body {
		return false
	}
	if !slices.Equal(t.Offers, effectiveOffers(spec)) || !slices.Equal(t.Options, spec.Options) {
		return false
	}
	return sameQuestions(t.Questions, spec.Questions)
}

func sameQuestions(a, b []Question) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Question != b[i].Question || a[i].Header != b[i].Header || a[i].MultiSelect != b[i].MultiSelect {
			return false
		}
		if !slices.Equal(a[i].Options, b[i].Options) {
			return false
		}
	}
	return true
}

func hasOption(options []Option, id string) bool {
	return slices.ContainsFunc(options, func(opt Option) bool {
		return opt.ID == id
	})
}

func cloneTicket(t *Ticket) *Ticket {
	cp := *t
	cp.Offers = slices.Clone(t.Offers)
	cp.Options = slices.Clone(t.Options)
	cp.Questions = cloneQuestions(t.Questions)
	cp.Answers = cloneAnswers(t.Answers)
	return &cp
}

func cloneQuestions(questions []Question) []Question {
	if questions == nil {
		return nil
	}
	out := make([]Question, len(questions))
	for i := range questions {
		out[i] = questions[i]
		out[i].Options = slices.Clone(questions[i].Options)
	}
	return out
}

func cloneAnswers(answers []Answer) []Answer {
	if answers == nil {
		return nil
	}
	out := make([]Answer, len(answers))
	for i := range answers {
		out[i] = answers[i]
		out[i].OptionIDs = slices.Clone(answers[i].OptionIDs)
	}
	return out
}

func (o *Owner) isTicketDoneLocked(t *Ticket, now time.Time) bool {
	if !t.ResolvedAt.IsZero() && now.Sub(t.ResolvedAt) >= ResolvedRetentionTTL {
		return true
	}
	if t.ReceiptID == "" {
		return false
	}
	r, ok := o.receipts[t.ReceiptID]
	if !ok || r.Claimed {
		return true
	}
	return !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt)
}

func (o *Owner) pruneTicketsLocked(now time.Time) {
	for reqID, ticket := range o.byRequest {
		if ticket.Relayed || !ticket.Resolved {
			continue
		}
		if o.isTicketDoneLocked(ticket, now) {
			delete(o.byRequest, reqID)
			delete(o.byOperation, ticket.OperationID)
		}
	}
}

func (o *Owner) pruneReceiptsLocked(now time.Time) {
	for id, r := range o.receipts {
		switch {
		case !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt):
			delete(o.receipts, id)
		case r.Claimed:
			if !r.ClaimedAt.IsZero() && now.Sub(r.ClaimedAt) >= ResolvedRetentionTTL {
				delete(o.receipts, id)
			}
		case !r.IssuedAt.IsZero() && now.Sub(r.IssuedAt) >= ResolvedRetentionTTL:
			delete(o.receipts, id)
		}
	}
}

func (o *Owner) pruneResolvedLocked(now time.Time) {
	o.pruneTicketsLocked(now)
	o.pruneReceiptsLocked(now)
}

// PruneResolved evicts non-relayed resolved tickets whose receipt has been
// claimed/expired or whose retention TTL has elapsed.
func (o *Owner) PruneResolved() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pruneResolvedLocked(o.clock.Now().UTC())
}

// EntryCounts returns the number of tickets and receipts currently held in memory.
func (o *Owner) EntryCounts() (byRequest, byOperation, receipts int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.byRequest), len(o.byOperation), len(o.receipts)
}
