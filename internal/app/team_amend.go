package app

// F11.2b amend path: fugue_team_amend changes an active team (§5.4).
// Amendments within the decided roles and agent count apply at once through
// TeamAmended and re-sealed specs; a new role or more agents creates a small
// inbox card and waits for the user, while the team keeps running unchanged.
// The lead's notice is the tool result only: no transcript item and no wake
// (Q-99). Retry of a failed task is not here — it is F11.3 (Q-100).

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// teamAmendToolName is the MCP tool name the lead's session calls.
const teamAmendToolName = "fugue_team_amend"

// teamAmendQuestionID is the question ID of the amend card.
const teamAmendQuestionID = "amend"

// teamAmendTaskLimit bounds one amendment's additions (§5.3 bounds).
const teamAmendTaskLimit = 64

// teamAmendPayload is the sealed payload of a TeamAmended append.
type teamAmendPayload struct {
	RoleEdits    []decidedRole `json:"role_edits,omitempty"`
	AddedTaskIDs []string      `json:"added_task_ids,omitempty"`
}

// AmendArgs is the fugue_team_amend input after decoding. Roles and
// task_budgets amend within the decided roles and agent count and apply at
// once; add_roles and add_tasks need the user (OR-E16) and go to a card.
type AmendArgs struct {
	TeamID      string                 `json:"team_id"`
	Roles       []teams.RoleEdit       `json:"roles,omitempty"`
	TaskBudgets []teams.TaskBudgetEdit `json:"task_budgets,omitempty"`
	AddRoles    []teams.ProposedRole   `json:"add_roles,omitempty"`
	AddTasks    []teams.ProposedTask   `json:"add_tasks,omitempty"`
}

// teamAmendInputSchema is the JSON Schema of fugue_team_amend shown to the
// vendor. Bounds mirror the teams input bounds (§5.3).
const teamAmendInputSchema = `{
  "type": "object",
  "properties": {
    "team_id": {"type": "string", "minLength": 1},
    "roles": {
      "type": "array", "maxItems": 24,
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "vendor": {"type": "string", "enum": ["claude", "codex", "agy"]},
          "model": {"type": "string", "maxLength": 128},
          "effort": {"type": "string", "enum": ["low", "medium", "high", "xhigh", "max"]},
          "permission": {"type": "string", "enum": ["read", "write"]}
        },
        "required": ["name"]
      }
    },
    "task_budgets": {
      "type": "array", "maxItems": 64,
      "items": {
        "type": "object",
        "properties": {
          "task_id": {"type": "string"},
          "budget": {
            "type": "object",
            "properties": {
              "max_turns": {"type": "integer", "minimum": 0},
              "max_tokens": {"type": "integer", "minimum": 0},
              "max_duration_seconds": {"type": "integer", "minimum": 0}
            }
          }
        },
        "required": ["task_id", "budget"]
      }
    },
    "add_roles": {
      "type": "array", "maxItems": 24,
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string"},
          "vendor": {"type": "string", "enum": ["claude", "codex", "agy"]},
          "model": {"type": "string", "maxLength": 128},
          "effort": {"type": "string", "enum": ["low", "medium", "high", "xhigh", "max"]},
          "permission": {"type": "string", "enum": ["read", "write"]},
          "checks": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["name", "vendor", "permission"]
      }
    },
    "add_tasks": {
      "type": "array", "maxItems": 64,
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "parent_task_id": {"type": "string"},
          "team_role": {"type": "string", "enum": ["lead", "team_manager", "member", "solo"]},
          "role": {"type": "string"},
          "goal": {"type": "string"},
          "context": {"type": "string"},
          "write_scope": {"type": "array", "items": {"type": "string"}},
          "deliverable": {"type": "string", "enum": ["diff", "report", "both"]},
          "depends_on": {"type": "array", "items": {"type": "string"}},
          "checks": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["id", "team_role", "role", "goal", "write_scope", "deliverable"]
      }
    }
  },
  "required": ["team_id"]
}`

// teamAmendTool builds the fugue_team_amend Fugue tool (§5.4). The call
// itself is read-risk and never asks: amendments within the decided roles
// apply at once, and anything beyond them creates the card, which is the ask.
func teamAmendTool(svc *TeamProposalService) *FugueTool {
	return &FugueTool{
		Name: teamAmendToolName,
		Description: "Amend a team you lead while it runs: change a role's vendor, model or " +
			"effort, or a task's budget, and the change applies at once. Adding a role or more " +
			"agents than the approved plan goes to the user as a small approval card, and the " +
			"team keeps running unchanged until they decide.",
		InputSchema: json.RawMessage(teamAmendInputSchema),
		Descriptor:  interaction.Descriptor{Risk: interaction.RiskRead, Confirm: interaction.ConfirmNever},
		Available: func(ctx context.Context, info vendorstore.Session) bool {
			enabled, err := svc.teamsEnabled(ctx, info.WorkspaceID)
			if err != nil || !enabled {
				return false
			}
			_, active, activeErr := svc.store.ActiveTeamForSession(ctx, info.ID)
			return activeErr == nil && active
		},
		Plan: func(json.RawMessage) (FugueToolPlan, error) { return FugueToolPlan{}, nil },
		Run: func(ctx context.Context, sessionID string, args json.RawMessage) (string, error) {
			decoded, err := decodeAmendArgs(args)
			if err != nil {
				return "", err
			}
			return svc.Amend(ctx, sessionID, decoded)
		},
	}
}

// decodeAmendArgs decodes the tool arguments strictly. The error text is
// fixed: amendment content never reaches errors (§13).
func decodeAmendArgs(raw json.RawMessage) (*AmendArgs, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var args AmendArgs
	if err := decoder.Decode(&args); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, "the amendment arguments do not match the tool schema")
	}
	return &args, nil
}

// Amend applies the session's amendments to one of its active teams (§5.4).
func (s *TeamProposalService) Amend(ctx context.Context, sessionID string, args *AmendArgs) (string, error) {
	if args == nil || args.TeamID == "" {
		return "", errcode.New(errcode.InvalidArgument, "the amendment names a team")
	}
	if len(args.AddTasks) > teamAmendTaskLimit {
		return "", errcode.New(errcode.InvalidArgument, "an amendment carries at most 64 tasks")
	}
	unlock := s.locks.lock(sessionID)
	defer unlock()
	if _, _, err := s.resolveTeamsSession(ctx, sessionID); err != nil {
		return "", err
	}
	rec, _, err := s.store.TeamSnapshot(ctx, args.TeamID)
	if err != nil {
		return "", boundaryError("read the amended team", err)
	}
	if rec.SessionID != sessionID {
		return "", errcode.New(errcode.NotFound, "the team is not an active team of this session")
	}
	if rec.State != vendorstore.TeamStateActive {
		return "", errcode.New(errcode.FailedPrecondition, "only an active team can be amended")
	}
	decided, err := s.decidedRoles(ctx, rec.TeamID)
	if err != nil {
		return "", err
	}
	if len(args.AddRoles) == 0 && len(args.AddTasks) == 0 {
		return s.applyDirectAmend(ctx, &rec, args, decided)
	}
	return s.requestAmendCard(ctx, sessionID, &rec, args)
}

// validateAmendAnswer pre-validates one amend card answer before the card
// resolves, with the same fail-closed rules as the proposal card.
func (s *TeamProposalService) validateAmendAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	option, _, err := decisionAnswerOf(teamAmendQuestionID, decision)
	if err != nil {
		return err
	}
	if option == "" {
		return nil
	}
	if option != decisionApprove && option != decisionReject {
		return errcode.New(errcode.InvalidArgument, "the decision option was not offered")
	}
	if option == decisionReject {
		return nil
	}
	pending := s.pendingAmends.get(ticket.Binding.InvocationID)
	if pending == nil || pending.amendID != ticket.Binding.AttemptID {
		return errcode.New(errcode.FailedPrecondition, "the amendment is no longer pending")
	}
	rec, _, err := s.store.TeamSnapshot(ctx, ticket.Binding.InvocationID)
	if err != nil {
		return boundaryError("read the amended team", err)
	}
	if rec.SessionID != ticket.Binding.SessionID || rec.State != vendorstore.TeamStateActive {
		return errcode.New(errcode.FailedPrecondition, "the amendment is no longer pending")
	}
	return s.validateAmendAdditions(ctx, &rec, pending.args)
}

// applyAmendAnswer applies one amend card answer. An approved amend really
// creates its queued tasks in the same transaction as the TeamAmended event,
// and nothing starts them (Q-98); a rejected amend changes nothing.
func (s *TeamProposalService) applyAmendAnswer(ctx context.Context, ticket *interaction.Ticket, decision *interaction.Decision) error {
	option, _, err := decisionAnswerOf(teamAmendQuestionID, decision)
	if err != nil {
		return err
	}
	if option == "" {
		return nil
	}
	teamID := ticket.Binding.InvocationID
	unlock := s.locks.lock(ticket.Binding.SessionID)
	defer unlock()
	pending := s.pendingAmends.get(teamID)
	if pending == nil || pending.amendID != ticket.Binding.AttemptID {
		return errcode.New(errcode.FailedPrecondition, "the amendment is no longer pending")
	}
	if option == decisionReject {
		s.pendingAmends.clear(teamID)
		return nil
	}
	return s.approveAmendment(ctx, ticket.Binding.SessionID, teamID, pending.args)
}

// approveAmendment applies a user-approved amend under the session's keyed
// lock: the additions re-validate against the current team, and the added
// tasks insert with their TeamCreated events in the AmendTeam transaction.
func (s *TeamProposalService) approveAmendment(ctx context.Context, sessionID, teamID string, args *AmendArgs) error {
	rec, _, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return boundaryError("read the amended team", err)
	}
	if rec.SessionID != sessionID || rec.State != vendorstore.TeamStateActive {
		return errcode.New(errcode.FailedPrecondition, "the amendment is no longer pending")
	}
	if err := s.validateAmendAdditions(ctx, &rec, args); err != nil {
		return err
	}
	added := make([]vendorstore.NewTaskInput, 0, len(args.AddTasks))
	addedIDs := make([]string, 0, len(args.AddTasks))
	for i := range args.AddTasks {
		taskInput, inputErr := newTaskInputOf(teamID, &args.AddTasks[i], s.ids)
		if inputErr != nil {
			return inputErr
		}
		added = append(added, *taskInput)
		addedIDs = append(addedIDs, taskInput.TaskID)
	}
	payload, marshalErr := json.Marshal(teamAmendPayload{AddedTaskIDs: addedIDs})
	if marshalErr != nil {
		return errcode.Wrap(errcode.Internal, "marshal the amendment payload", marshalErr)
	}
	eventID, idErr := s.ids.NewID()
	if idErr != nil {
		return errcode.Wrap(errcode.Internal, "generate the amend event ID", idErr)
	}
	if _, amendErr := s.store.AmendTeam(ctx, teamID, &vendorstore.TeamAmendInput{
		ExpectedSeq: rec.LastSeq, AmendEventID: eventID, AmendPayload: payload, AddedTasks: added,
	}); amendErr != nil {
		return boundaryError("apply the amendment", amendErr)
	}
	s.pendingAmends.clear(teamID)
	return nil
}
