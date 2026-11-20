package app

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	taskStartToolName   = "fugue_task_start"
	taskSendToolName    = "fugue_task_send"
	taskCancelToolName  = "fugue_task_cancel"
	taskStatusToolName  = "fugue_task_status"
	taskResultToolName  = "fugue_task_result"
	taskDoneToolName    = "fugue_task_done"
	taskAskToolName     = "fugue_task_ask"
	taskAnswerToolName  = "fugue_task_answer"
	taskRetryToolName   = "fugue_task_retry"
	taskAcceptToolName  = "fugue_task_accept"
	taskRejectToolName  = "fugue_task_reject"
	taskRecoverToolName = "fugue_task_recover"
	teamNoteToolName    = "fugue_team_note"
	teamBriefToolName   = "fugue_team_brief"
)

func teamTaskTools(svc *teamTaskService) []*FugueTool {
	return []*FugueTool{
		taskStartTool(svc),
		taskSendTool(svc),
		taskCancelTool(svc),
		taskStatusTool(svc),
		taskResultTool(svc),
		taskDoneTool(svc),
		taskAskTool(svc),
		taskAnswerTool(svc),
		taskRetryTool(svc),
		taskAcceptTool(svc),
		taskRejectTool(svc),
		taskRecoverTool(svc),
		teamNoteTool(svc),
		teamBriefTool(svc),
	}
}

func roleAvailable(svc *teamTaskService, allowed ...vendorstore.TeamRole) func(context.Context, vendorstore.Session) bool {
	return func(ctx context.Context, info vendorstore.Session) bool {
		role, err := sessionTeamRole(ctx, svc.store, info.ID)
		if err != nil || role == "" {
			return false
		}
		for _, r := range allowed {
			if role == r {
				return true
			}
		}
		return false
	}
}

// readTool builds a Fugue-internal team coordination tool. Following
// fugue_team_propose and fugue_team_amend, these tools use RiskRead and
// ConfirmNever because they mutate only Fugue's team state machine (not host
// files or shell processes) and enforce role/subtree authorization in Go.
func readTool(name, desc, schema string, avail func(context.Context, vendorstore.Session) bool, run func(context.Context, string, json.RawMessage) (string, error)) *FugueTool {
	return &FugueTool{
		Name: name, Description: desc, InputSchema: json.RawMessage(schema),
		Available:  avail,
		Descriptor: interaction.Descriptor{Risk: interaction.RiskRead, Confirm: interaction.ConfirmNever},
		Plan:       func(json.RawMessage) (FugueToolPlan, error) { return FugueToolPlan{}, nil },
		Run:        run,
	}
}

func decodeStrictArgs(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errcode.New(errcode.InvalidArgument, "tool arguments do not match the tool schema")
	}
	return nil
}

func taskStartTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}`
	avail := roleAvailable(svc, vendorstore.RoleLead, vendorstore.RoleTeamManager)
	return readTool(taskStartToolName, "Start a queued direct child task without waiting for it to finish.", schema, avail,
		func(ctx context.Context, sessionID string, raw json.RawMessage) (string, error) {
			var a struct {
				TaskID string `json:"task_id"`
			}
			if err := decodeStrictArgs(raw, &a); err != nil {
				return "", err
			}
			return svc.Start(ctx, sessionID, a.TaskID)
		})
}

func taskSendTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"task_id":{"type":"string"},"message":{"type":"string","maxLength":4096}},"required":["task_id","message"]}`
	avail := roleAvailable(svc, vendorstore.RoleLead, vendorstore.RoleTeamManager)
	return readTool(taskSendToolName, "Send a follow-up message to a running or asking direct child task.", schema, avail,
		func(ctx context.Context, sessionID string, raw json.RawMessage) (string, error) {
			var a struct {
				TaskID  string `json:"task_id"`
				Message string `json:"message"`
			}
			if err := decodeStrictArgs(raw, &a); err != nil {
				return "", err
			}
			return svc.Send(ctx, sessionID, a.TaskID, a.Message)
		})
}

func taskCancelTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"task_id":{"type":"string"},"reason":{"type":"string","maxLength":512}},"required":["task_id"]}`
	avail := roleAvailable(svc, vendorstore.RoleLead, vendorstore.RoleTeamManager)
	return readTool(taskCancelToolName, "Cancel a non-terminal direct child task.", schema, avail,
		func(ctx context.Context, sessionID string, raw json.RawMessage) (string, error) {
			var a struct {
				TaskID string `json:"task_id"`
				Reason string `json:"reason"`
			}
			if err := decodeStrictArgs(raw, &a); err != nil {
				return "", err
			}
			return svc.Cancel(ctx, sessionID, a.TaskID, a.Reason)
		})
}

func taskStatusTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"task_id":{"type":"string"}},"required":["task_id"]}`
	avail := roleAvailable(svc, vendorstore.RoleLead, vendorstore.RoleTeamManager, vendorstore.RoleMember)
	return readTool(taskStatusToolName, "Read the current state and children of a visible team task.", schema, avail,
		func(ctx context.Context, sessionID string, raw json.RawMessage) (string, error) {
			var a struct {
				TaskID string `json:"task_id"`
			}
			if err := decodeStrictArgs(raw, &a); err != nil {
				return "", err
			}
			return svc.Status(ctx, sessionID, a.TaskID)
		})
}

func taskResultTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"task_id":{"type":"string"},"part":{"type":"string","enum":["summary","output","report","checks","diff"]},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}},"required":["task_id","part"]}`
	avail := roleAvailable(svc, vendorstore.RoleLead, vendorstore.RoleTeamManager, vendorstore.RoleMember)
	return readTool(taskResultToolName, "Read one paged part (summary, output, report, checks, diff) of a visible task result.", schema, avail,
		func(ctx context.Context, sessionID string, raw json.RawMessage) (string, error) {
			var a struct {
				TaskID string `json:"task_id"`
				Part   string `json:"part"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := decodeStrictArgs(raw, &a); err != nil {
				return "", err
			}
			return svc.Result(ctx, sessionID, a.TaskID, a.Part, a.Offset, a.Limit)
		})
}

func taskDoneTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"summary":{"type":"string"},"output":{},"status":{"type":"string","enum":["succeeded","failed"]}},"required":["summary","status"]}`
	avail := roleAvailable(svc, vendorstore.RoleTeamManager, vendorstore.RoleMember)
	return readTool(taskDoneToolName, "Report completion of the current task with summary, status, and optional structured output.", schema, avail,
		func(ctx context.Context, sessionID string, raw json.RawMessage) (string, error) {
			var a struct {
				Summary string          `json:"summary"`
				Output  json.RawMessage `json:"output"`
				Status  string          `json:"status"`
			}
			if err := decodeStrictArgs(raw, &a); err != nil {
				return "", err
			}
			return svc.Done(ctx, sessionID, a.Summary, a.Output, a.Status)
		})
}

func taskAskTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"question":{"type":"string"},"to":{"type":"string","enum":["parent","user"]}},"required":["question"]}`
	avail := roleAvailable(svc, vendorstore.RoleTeamManager, vendorstore.RoleMember)
	return readTool(taskAskToolName, "Ask the parent delegator or user a blocking question.", schema, avail,
		func(ctx context.Context, sessionID string, raw json.RawMessage) (string, error) {
			var a struct {
				Question string `json:"question"`
				To       string `json:"to"`
			}
			if err := decodeStrictArgs(raw, &a); err != nil {
				return "", err
			}
			return svc.Ask(ctx, sessionID, a.Question, a.To)
		})
}

func teamNoteTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"note":{"type":"string","maxLength":2048}},"required":["note"]}`
	avail := roleAvailable(svc, vendorstore.RoleLead, vendorstore.RoleTeamManager)
	return readTool(teamNoteToolName, "Append a shared note to the team brief for all team members.", schema, avail,
		func(ctx context.Context, sessionID string, raw json.RawMessage) (string, error) {
			var a struct {
				Note string `json:"note"`
			}
			if err := decodeStrictArgs(raw, &a); err != nil {
				return "", err
			}
			return svc.Note(ctx, sessionID, a.Note)
		})
}

func teamBriefTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1}}}`
	avail := roleAvailable(svc, vendorstore.RoleLead, vendorstore.RoleTeamManager, vendorstore.RoleMember)
	return readTool(teamBriefToolName, "Read the team brief and append-only team notes.", schema, avail,
		func(ctx context.Context, sessionID string, raw json.RawMessage) (string, error) {
			var a struct {
				Offset int `json:"offset"`
				Limit  int `json:"limit"`
			}
			if err := decodeStrictArgs(raw, &a); err != nil {
				return "", err
			}
			return svc.Brief(ctx, sessionID, a.Offset, a.Limit)
		})
}

func delegatorJSONTool[A any](svc *teamTaskService, name, desc, schema string, fn func(context.Context, string, A) (string, error)) *FugueTool {
	avail := roleAvailable(svc, vendorstore.RoleLead, vendorstore.RoleTeamManager)
	return readTool(name, desc, schema, avail,
		func(ctx context.Context, sessionID string, raw json.RawMessage) (string, error) {
			var a A
			if err := decodeStrictArgs(raw, &a); err != nil {
				return "", err
			}
			return fn(ctx, sessionID, a)
		})
}

func taskAnswerTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"task_id":{"type":"string"},"answer":{"type":"string","maxLength":4096},"forward_to":{"type":"string","enum":["parent","user"]}},"required":["task_id"]}`
	type args struct {
		TaskID    string `json:"task_id"`
		Answer    string `json:"answer"`
		ForwardTo string `json:"forward_to"`
	}
	return delegatorJSONTool(svc, taskAnswerToolName, "Answer an asking direct child task or forward its question to your parent or the user.", schema,
		func(ctx context.Context, sessionID string, a args) (string, error) {
			return svc.Answer(ctx, sessionID, a.TaskID, a.Answer, a.ForwardTo)
		})
}

func taskRetryTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"task_id":{"type":"string"},"role":{"type":"string"},"model":{"type":"string"}},"required":["task_id"]}`
	type args struct {
		TaskID string `json:"task_id"`
		Role   string `json:"role"`
		Model  string `json:"model"`
	}
	return delegatorJSONTool(svc, taskRetryToolName, "Retry a failed direct child task with the same role and model (up to 2 retries).", schema,
		func(ctx context.Context, sessionID string, a args) (string, error) {
			return svc.Retry(ctx, sessionID, a.TaskID, a.Role, a.Model)
		})
}

func taskAcceptTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"task_id":{"type":"string"},"note":{"type":"string","maxLength":4096}},"required":["task_id"]}`
	type args struct {
		TaskID string `json:"task_id"`
		Note   string `json:"note"`
	}
	return delegatorJSONTool(svc, taskAcceptToolName, "Accept a direct child task in review state.", schema,
		func(ctx context.Context, sessionID string, a args) (string, error) {
			return svc.Accept(ctx, sessionID, a.TaskID, a.Note)
		})
}

func taskRejectTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"task_id":{"type":"string"},"reason":{"type":"string","maxLength":4096}},"required":["task_id","reason"]}`
	type args struct {
		TaskID string `json:"task_id"`
		Reason string `json:"reason"`
	}
	return delegatorJSONTool(svc, taskRejectToolName, "Reject a direct child task in review state and request changes.", schema,
		func(ctx context.Context, sessionID string, a args) (string, error) {
			return svc.Reject(ctx, sessionID, a.TaskID, a.Reason)
		})
}

func taskRecoverTool(svc *teamTaskService) *FugueTool {
	const schema = `{"type":"object","properties":{"task_id":{"type":"string"},"decision":{"type":"string","enum":["resume","keep_diff","cancel"]},"reason":{"type":"string","maxLength":512}},"required":["task_id","decision"]}`
	type args struct {
		TaskID   string `json:"task_id"`
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	return delegatorJSONTool(svc, taskRecoverToolName, "Decide recovery (resume, keep_diff, or cancel) for a direct child task in unknown state.", schema,
		func(ctx context.Context, sessionID string, a args) (string, error) {
			return svc.Recover(ctx, sessionID, a.TaskID, a.Decision, a.Reason)
		})
}
