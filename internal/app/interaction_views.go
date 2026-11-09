package app

import (
	"slices"

	"github.com/syurodev/fugue/internal/interaction"
)

// PendingInteractionView is the domain view of a pending request, carrying
// the fields permitted by interaction §8 plus additive multi-question support.
type PendingInteractionView struct {
	RequestID       string                `json:"request_id"`
	Incarnation     string                `json:"incarnation"`
	SessionID       string                `json:"session_id"`
	Tool            string                `json:"tool"`
	Kind            string                `json:"kind"`
	Risk            string                `json:"risk"`
	EnvTag          string                `json:"env_tag"`
	Summary         string                `json:"summary"`
	OfferedOutcomes []string              `json:"offered_outcomes,omitempty"`
	Options         []InteractionOption   `json:"options,omitempty"`
	GrantScopeLabel string                `json:"grant_scope_label,omitempty"`
	Questions       []InteractionQuestion `json:"questions,omitempty"`
}

// InteractionOption is an option offered to the user in a pending interaction.
type InteractionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// InteractionQuestion is one question within a question-kind interaction request.
type InteractionQuestion struct {
	QuestionID  string              `json:"question_id"`
	Prompt      string              `json:"prompt"`
	Options     []InteractionOption `json:"options,omitempty"`
	MultiSelect bool                `json:"multi_select,omitempty"`
	AllowText   bool                `json:"allow_text,omitempty"`
}

// InteractionAnswer is an answer to a question in an interaction request.
type InteractionAnswer struct {
	QuestionID string
	OptionIDs  []string
	Text       string
}

func hasOption(options []interaction.Option, id string) bool {
	return slices.ContainsFunc(options, func(opt interaction.Option) bool {
		return opt.ID == id
	})
}

func viewFromTicket(t *interaction.Ticket) PendingInteractionView {
	summary := t.Summary
	if summary == "" && len(t.Questions) > 0 {
		summary = t.Questions[0].Question
	}

	rawOptions := t.Options
	if len(rawOptions) == 0 && len(t.Questions) > 0 {
		rawOptions = t.Questions[0].Options
	}

	options := make([]InteractionOption, 0, len(rawOptions))
	for _, o := range rawOptions {
		options = append(options, InteractionOption{
			ID:          o.ID,
			Label:       o.Label,
			Description: o.Description,
		})
	}

	questions := make([]InteractionQuestion, 0, len(t.Questions))
	for _, q := range t.Questions {
		qOpts := make([]InteractionOption, 0, len(q.Options))
		for _, o := range q.Options {
			qOpts = append(qOpts, InteractionOption{
				ID:          o.ID,
				Label:       o.Label,
				Description: o.Description,
			})
		}
		questions = append(questions, InteractionQuestion{
			QuestionID:  q.ID,
			Prompt:      q.Question,
			Options:     qOpts,
			MultiSelect: q.MultiSelect,
			AllowText:   true,
		})
	}

	return PendingInteractionView{
		RequestID:       t.RequestID,
		Incarnation:     t.Incarnation,
		SessionID:       t.Binding.SessionID,
		Tool:            t.Binding.ToolName,
		Kind:            string(t.Kind),
		Risk:            t.Risk,
		EnvTag:          t.Binding.EnvTag,
		Summary:         summary,
		OfferedOutcomes: offeredOutcomesFor(t),
		Options:         options,
		GrantScopeLabel: t.GrantScopeLabel,
		Questions:       questions,
	}
}
