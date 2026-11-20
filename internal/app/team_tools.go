package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// teamProposeInputSchema is the JSON Schema of fugue_team_propose shown to
// the vendor. Bounds mirror the teams package input bounds (§5.3).
const teamProposeInputSchema = `{
  "type": "object",
  "properties": {
    "title": {"type": "string", "maxLength": 512},
    "brief": {"type": "string", "maxLength": 16384},
    "roles": {
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
    "tasks": {
      "type": "array", "minItems": 1, "maxItems": 64,
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
          "checks": {"type": "array", "items": {"type": "string"}},
          "profile": {
            "type": "object",
            "properties": {
              "vendor": {"type": "string", "enum": ["claude", "codex", "agy"]},
              "model": {"type": "string", "maxLength": 128},
              "effort": {"type": "string", "enum": ["low", "medium", "high", "xhigh", "max"]}
            }
          }
        },
        "required": ["id", "team_role", "role", "goal", "write_scope", "deliverable"]
      }
    },
    "budget": {
      "type": "object",
      "properties": {
        "max_agents": {"type": "integer", "minimum": 0, "maximum": 64},
        "max_duration_seconds": {"type": "integer", "minimum": 0, "maximum": 604800}
      },
      "additionalProperties": false
    }
  },
  "required": ["title", "brief", "tasks"]
}`

// teamProposeTool builds the fugue_team_propose Fugue tool (§5.1–5.3). The
// call itself is read-risk and never asks: the proposal card it creates is
// the ask, and nothing starts before the user decides (OR-02, OR-13).
func teamProposeTool(svc *TeamProposalService) *FugueTool {
	return &FugueTool{
		Name: teamProposeToolName,
		Description: "Propose a multi-agent team for the current work: roles, the model per " +
			"role, the task tree with dependencies. Propose when the work has at least two parts " +
			"that can run in parallel, would take about 30 minutes or more solo, or fits another " +
			"model better. Work solo when the task is small or strictly sequential. The user " +
			"decides on the proposal; nothing starts without that decision.",
		InputSchema: json.RawMessage(teamProposeInputSchema),
		Descriptor:  interaction.Descriptor{Risk: interaction.RiskRead, Confirm: interaction.ConfirmNever},
		Available: func(ctx context.Context, info vendorstore.Session) bool {
			enabled, err := svc.teamsEnabled(ctx, info.WorkspaceID)
			if err != nil || !enabled {
				return false
			}
			// §6.6: a manager or a member never proposes a team; the role
			// comes from the store (Q-117). A lookup that fails offers nothing.
			role, roleErr := sessionTeamRole(ctx, svc.store, info.ID)
			return roleErr == nil && role != vendorstore.RoleTeamManager && role != vendorstore.RoleMember
		},
		Plan: func(json.RawMessage) (FugueToolPlan, error) { return FugueToolPlan{}, nil },
		Run: func(ctx context.Context, sessionID string, args json.RawMessage) (string, error) {
			decoded, err := decodeProposeArgs(args)
			if err != nil {
				return "", err
			}
			proposalID, err := svc.Propose(ctx, sessionID, decoded)
			if err != nil {
				return "", err
			}
			return "proposal " + proposalID + " is recorded and waits for the user's decision in the approval inbox", nil
		},
	}
}

// decodeProposeArgs decodes the tool arguments strictly. The error text is
// fixed: proposal content never reaches errors (§13).
func decodeProposeArgs(raw json.RawMessage) (*ProposeArgs, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var args ProposeArgs
	if err := decoder.Decode(&args); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, "the proposal arguments do not match the tool schema")
	}
	return &args, nil
}
