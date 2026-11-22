package client

// The tasks client (host-api v0.17 §5.3, F11.10b): the team channel of
// orchestration §6.7. Local principals only; the durable `team:<id>` topic
// streams one event per appended line, with a snapshot of the newest 100.
// While the vault is locked the host refuses PageChannel and the topic
// events carry metadata only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	tasksv1 "github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1/tasksv1connect"
)

// errTasksEventUnknown is the static cause behind an event type this client
// does not decode; it is wrapped, never constructed, at the call site.
var errTasksEventUnknown = errors.New("the team channel event type is not one the client decodes")

// errTasksPayloadNotObject is the static cause behind a topic payload that
// is not a JSON object; it is wrapped, never constructed, at the call site.
var errTasksPayloadNotObject = errors.New("the team channel payload is not a JSON object")

// errTasksSequenceNegative is the static cause behind a negative channel
// sequence in a snapshot payload.
var errTasksSequenceNegative = errors.New("the team channel sequence must not be negative")

// errPruneRetentionInvalid is the static cause behind an invalid prune
// retention duration.
var errPruneRetentionInvalid = errors.New("prune retention must be zero or a positive whole number of seconds")

func channelEventSequence(n int64) (uint64, error) {
	if n < 0 {
		return 0, fmt.Errorf("%w: %d", errTasksSequenceNegative, n)
	}
	return uint64(n), nil
}

// TopicTeamChannel names the durable channel topic of one team: prefix a
// team ID, as in `team:<id>`.
const TopicTeamChannel = "team:"

// EventTeamChannelAppended is the type of one appended channel line.
const EventTeamChannelAppended = "channel.appended"

// ChannelLine is one line of a team's channel (§5.3). Content is the text of
// the line; the topic omits it while the vault is locked, and PageChannel
// refuses instead.
type ChannelLine struct {
	ChannelSeq      int64
	Kind            string
	SenderKind      string
	SenderTaskID    string
	RecipientTaskID string
	ThreadRoot      string
	Attempt         uint32
	SourceSeq       int64
	CreatedAt       time.Time
	Content         string
}

// PageChannelInput carries one PageChannel request. An empty ThreadTaskID
// pages the general channel.
type PageChannelInput struct {
	TeamID       string
	ThreadTaskID string
	Before       int64
	Since        int64
	Limit        uint32
}

// PostChannelMessageInput carries one PostChannelMessage request. The host
// routes by ToTaskID when set, else to the thread's task agent, else to the
// lead; the text is capped at 4 KiB.
type PostChannelMessageInput struct {
	CommandID    string
	TeamID       string
	ThreadTaskID string
	ToTaskID     string
	Text         string
}

// PageChannel pages one team's channel, newest first (§5.3).
func (c *Client) PageChannel(ctx context.Context, in *PageChannelInput) ([]ChannelLine, error) {
	res, err := tasksv1connect.NewTaskServiceClient(c.hc, c.base).PageChannel(ctx,
		connect.NewRequest(&tasksv1.PageChannelRequest{
			TeamId:       in.TeamID,
			ThreadTaskId: in.ThreadTaskID,
			Before:       in.Before,
			Since:        in.Since,
			Limit:        in.Limit,
		}))
	if err != nil {
		return nil, fmt.Errorf("page the team channel: %w", err)
	}
	out := make([]ChannelLine, 0, len(res.Msg.GetLines()))
	for _, l := range res.Msg.GetLines() {
		out = append(out, channelLineFromProto(l))
	}
	return out, nil
}

// PostChannelMessage posts one user message to the routed agent (§5.3) and
// returns the `#n` of the new line. A replayed command_id with the same
// arguments returns the same line without posting again.
func (c *Client) PostChannelMessage(ctx context.Context, in *PostChannelMessageInput) (int64, error) {
	res, err := tasksv1connect.NewTaskServiceClient(c.hc, c.base).PostChannelMessage(ctx,
		connect.NewRequest(&tasksv1.PostChannelMessageRequest{
			CommandId:    in.CommandID,
			TeamId:       in.TeamID,
			ThreadTaskId: in.ThreadTaskID,
			ToTaskId:     in.ToTaskID,
			Text:         in.Text,
		}))
	if err != nil {
		return 0, fmt.Errorf("post the channel message: %w", err)
	}
	return res.Msg.GetChannelSeq(), nil
}

// PruneWorktreesResult reports what one host prune pass removed.
type PruneWorktreesResult struct {
	RemovedWorktrees    []string
	RemovedBranches     []string
	RemovedAdminEntries int
}

// PruneWorktrees removes ended-team task worktrees and branches past the
// retention duration (zero uses the host's 24h default, orchestration §9).
func (c *Client) PruneWorktrees(ctx context.Context, retention time.Duration) (PruneWorktreesResult, error) {
	if retention < 0 || (retention != 0 && (retention < time.Second || retention%time.Second != 0)) {
		return PruneWorktreesResult{}, fmt.Errorf("%w: %s", errPruneRetentionInvalid, retention)
	}
	res, err := tasksv1connect.NewTaskServiceClient(c.hc, c.base).PruneWorktrees(ctx,
		connect.NewRequest(&tasksv1.PruneWorktreesRequest{
			RetentionSeconds: int64(retention / time.Second),
		}))
	if err != nil {
		return PruneWorktreesResult{}, fmt.Errorf("prune task worktrees: %w", err)
	}
	return PruneWorktreesResult{
		RemovedWorktrees:    res.Msg.GetRemovedWorktrees(),
		RemovedBranches:     res.Msg.GetRemovedBranches(),
		RemovedAdminEntries: int(res.Msg.GetRemovedAdminEntries()),
	}, nil
}

// TasksCodeOf returns the public code of a tasks call failure, for example
// tasks.team_ended.
func TasksCodeOf(err error) (string, bool) {
	detail, ok := errorDetailOf(err)
	if !ok {
		return "", false
	}
	return detail.GetCode(), true
}

// TeamChannelSnapshot is the team:<id> topic's snapshot: the newest 100
// lines at one sequence.
type TeamChannelSnapshot struct {
	Sequence uint64
	Lines    []ChannelLine
}

// TeamChannelEvent is one team:<id> topic event: one appended line. Content
// is empty while the vault is locked — the metadata always flows.
type TeamChannelEvent struct {
	Type   string
	Line   ChannelLine
	Locked bool
}

// DecodeTeamChannelSnapshot decodes the team:<id> topic's snapshot payload.
func DecodeTeamChannelSnapshot(payload []byte) (TeamChannelSnapshot, error) {
	var wire struct {
		Lines []channelLineWire `json:"lines"`
	}
	if err := decodeTasksJSON(payload, &wire, "the team channel snapshot"); err != nil {
		return TeamChannelSnapshot{}, err
	}
	out := TeamChannelSnapshot{Lines: make([]ChannelLine, 0, len(wire.Lines))}
	for i := range wire.Lines {
		out.Lines = append(out.Lines, wire.Lines[i].view())
	}
	for i := range out.Lines {
		seq, seqErr := channelEventSequence(out.Lines[i].ChannelSeq)
		if seqErr != nil {
			return TeamChannelSnapshot{}, seqErr
		}
		if seq > out.Sequence {
			out.Sequence = seq
		}
	}
	return out, nil
}

// DecodeTeamChannelEvent decodes a team:<id> topic event of the given type.
// Content is empty while the vault is locked (§5.3).
func DecodeTeamChannelEvent(eventType string, payload []byte) (TeamChannelEvent, error) {
	if eventType != EventTeamChannelAppended {
		return TeamChannelEvent{}, fmt.Errorf("%w: %q", errTasksEventUnknown, eventType)
	}
	var wire channelLineWire
	if err := decodeTasksJSON(payload, &wire, "the team channel event"); err != nil {
		return TeamChannelEvent{}, err
	}
	return TeamChannelEvent{Type: eventType, Line: wire.view(), Locked: wire.Content == ""}, nil
}

func channelLineFromProto(l *tasksv1.ChannelLine) ChannelLine {
	if l == nil {
		return ChannelLine{}
	}
	var createdAt time.Time
	if l.GetCreatedAt() != nil {
		createdAt = l.GetCreatedAt().AsTime()
	}
	return ChannelLine{
		ChannelSeq:      l.GetChannelSeq(),
		Kind:            l.GetKind(),
		SenderKind:      l.GetSenderKind(),
		SenderTaskID:    l.GetSenderTaskId(),
		RecipientTaskID: l.GetRecipientTaskId(),
		ThreadRoot:      l.GetThreadRootTaskId(),
		Attempt:         l.GetAttempt(),
		SourceSeq:       l.GetSourceSeq(),
		CreatedAt:       createdAt,
		Content:         l.GetContent(),
	}
}

// channelLineWire mirrors the host's topic payload shape.
type channelLineWire struct {
	ChannelSeq      int64  `json:"channel_seq"`
	Kind            string `json:"kind"`
	SenderKind      string `json:"sender_kind"`
	SenderTaskID    string `json:"sender_task_id,omitempty"`
	RecipientTaskID string `json:"recipient_task_id,omitempty"`
	ThreadRoot      string `json:"thread_root_task_id,omitempty"`
	SourceSeq       int64  `json:"source_seq"`
	CreatedAt       int64  `json:"created_at"`
	Content         string `json:"content,omitempty"`
}

func (w *channelLineWire) view() ChannelLine {
	return ChannelLine{
		ChannelSeq:      w.ChannelSeq,
		Kind:            w.Kind,
		SenderKind:      w.SenderKind,
		SenderTaskID:    w.SenderTaskID,
		RecipientTaskID: w.RecipientTaskID,
		ThreadRoot:      w.ThreadRoot,
		SourceSeq:       w.SourceSeq,
		CreatedAt:       time.UnixMilli(w.CreatedAt),
		Content:         w.Content,
	}
}

// decodeTasksJSON decodes a topic payload into out, refusing a payload that
// is not a JSON object: json.Unmarshal takes a bare null as a no-op, and a
// null event would silently become the zero value.
func decodeTasksJSON(payload []byte, out any, what string) error {
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode %s: %w", what, err)
	}
	var probe map[string]json.RawMessage
	if probeErr := json.Unmarshal(payload, &probe); probeErr != nil || probe == nil {
		return fmt.Errorf("%w: %s", errTasksPayloadNotObject, what)
	}
	return nil
}
