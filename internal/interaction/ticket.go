package interaction

import "slices"

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
