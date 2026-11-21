package app

// F11.10b durable team channel topics (host-api v0.17 §5.3): one `team:<id>`
// topic per team, one event per appended channel line, snapshot of the
// newest 100 lines. The topic reads the same rows `PageChannel` reads; while
// the vault is locked the events and the snapshot carry metadata only — the
// content never leaves the host unopened (§5.3, Q-175 A). Registration is the
// authorization registry: every principal that is not local is denied by the
// hub's default authorizer, so vendor session principals never subscribe.

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// TeamChannelTopicName is the durable topic of one team's channel.
func TeamChannelTopicName(teamID string) string {
	return "team:" + teamID
}

// channelEventName is the type of one appended channel line.
const channelEventName = "channel.appended"

// channelSnapshotLines is the §5.3 snapshot size: the newest 100 lines.
const channelSnapshotLines = 100

// channelLineWire is the payload of one channel event. Content is present
// only while the vault is unlocked; a locked vault emits metadata only.
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

// channelSnapshotWire is the snapshot state of one team's channel.
type channelSnapshotWire struct {
	Lines []channelLineWire `json:"lines"`
}

// teamChannelTopics owns the per-team durable topics: it registers them at
// startup and on team creation, and publishes each appended line through the
// post-commit observer the store calls.
type teamChannelTopics struct {
	store *vendorstore.Store
	hub   *EventHub
	vault *vault.Service

	mu         sync.Mutex
	registered map[string]bool
}

func newTeamChannelTopics(store *vendorstore.Store, hub *EventHub, v *vault.Service) *teamChannelTopics {
	return &teamChannelTopics{store: store, hub: hub, vault: v, registered: make(map[string]bool, 8)}
}

// RegisterStoredTeams registers a topic for every team at startup, ended
// teams included — their channel stays readable (§6.7: the channel is the
// team's history).
func (t *teamChannelTopics) RegisterStoredTeams(ctx context.Context) {
	ids, err := t.store.AllTeamIDs(ctx)
	if err != nil {
		slog.WarnContext(ctx, "registering the team channel topics failed", "error_code", string(errcode.CodeOf(err)))
		return
	}
	for _, id := range ids {
		t.RegisterTeam(id)
	}
}

// RegisterTeam registers one team's channel topic; the hub rejects a
// duplicate, so the first caller owns the result.
func (t *teamChannelTopics) RegisterTeam(teamID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.registered[teamID] {
		return
	}
	if err := t.hub.RegisterDurable(TeamChannelTopicName(teamID), &teamChannelTopic{topics: t, teamID: teamID}); err != nil {
		slog.Warn("registering the team channel topic failed", "team_id", teamID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	t.registered[teamID] = true
}

// Observe publishes the channel lines one just-committed team event
// projected. Best-effort: a publish failure is logged and every subscriber
// still catches up through Since, because the rows are already committed.
func (t *teamChannelTopics) Observe(teamID string, seq int64) {
	t.mu.Lock()
	registered := t.registered[teamID]
	t.mu.Unlock()
	if !registered {
		return
	}
	lines, err := t.store.TeamChannelLinesForSources(context.WithoutCancel(context.Background()), teamID, []int64{seq})
	if err != nil {
		slog.Warn("reading the channel lines for publish failed", "team_id", teamID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	for _, sourceLines := range lines {
		for i := range sourceLines {
			t.publish(teamID, &sourceLines[i])
		}
	}
}

// publish pushes one line's event to the topic.
func (t *teamChannelTopics) publish(teamID string, row *vendorstore.TeamChannelMessage) {
	payload, err := json.Marshal(t.lineWire(row))
	if err != nil {
		slog.Warn("marshalling the channel event failed", "team_id", teamID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if _, err := t.hub.Publish(TeamChannelTopicName(teamID), Event{
		Seq: uint64(row.ChannelSeq), Type: channelEventName, Payload: payload,
	}); err != nil {
		slog.Warn("publishing the channel event failed", "team_id", teamID, "error_code", string(errcode.CodeOf(err)))
	}
}

// unlocked reports an unlocked vault; an unknown state counts as locked, so
// the content never escapes unopened.
func (t *teamChannelTopics) unlocked() bool {
	return t.vault != nil && t.vault.Snapshot().State == vault.StateUnlocked
}

// lineWire converts one row to its wire shape, opening the sealed source
// payload only while the vault is unlocked.
func (t *teamChannelTopics) lineWire(row *vendorstore.TeamChannelMessage) channelLineWire {
	wire := channelLineWire{
		ChannelSeq:      row.ChannelSeq,
		Kind:            row.Kind,
		SenderKind:      row.SenderKind,
		SenderTaskID:    row.SenderTaskID,
		RecipientTaskID: row.RecipientTaskID,
		ThreadRoot:      row.ThreadRoot,
		SourceSeq:       row.SourceSeq,
		CreatedAt:       row.CreatedAt,
	}
	if !t.unlocked() {
		return wire
	}
	raw, err := t.store.OpenTeamEventPayload(context.Background(), row.TeamID, row.SourceSeq)
	if err != nil {
		// §5.3: a locked vault never carries content; an unreadable payload
		// degrades the same way instead of failing the stream.
		return wire
	}
	wire.Content = channelContentText(row, nil, raw)
	return wire
}

// teamChannelTopic is the DurableTopic of one team's channel.
type teamChannelTopic struct {
	topics *teamChannelTopics
	teamID string
}

// Snapshot returns the newest 100 lines and the sequence they reflect (§5.3).
// It reads metadata only and never fails on a locked vault.
func (t *teamChannelTopic) Snapshot(ctx context.Context) ([]byte, EventSeq, error) {
	rows, err := t.topics.store.TeamChannelMessages(ctx, t.teamID, vendorstore.TeamChannelPageQuery{Limit: channelSnapshotLines})
	if err != nil {
		return nil, 0, errcode.Wrap(errcode.Unavailable, "snapshot the team channel", err)
	}
	wire := channelSnapshotWire{Lines: make([]channelLineWire, 0, len(rows))}
	seq := EventSeq(0)
	for i := range rows {
		row := &rows[i]
		wire.Lines = append(wire.Lines, t.topics.lineWire(row))
		if uint64(row.ChannelSeq) > seq {
			seq = uint64(row.ChannelSeq)
		}
	}
	state, err := json.Marshal(wire)
	if err != nil {
		return nil, 0, errcode.Wrap(errcode.Unavailable, "marshal the team channel snapshot", err)
	}
	return state, seq, nil
}

// Since returns the channel events after seq, oldest first. The channel rows
// are append-only and never pruned, so the cursor never goes too old.
func (t *teamChannelTopic) Since(ctx context.Context, seq EventSeq) ([]Event, error) {
	rows, err := t.topics.store.TeamChannelMessages(ctx, t.teamID, vendorstore.TeamChannelPageQuery{Since: int64(seq), Limit: 500})
	if err != nil {
		return nil, errcode.Wrap(errcode.Unavailable, "read the team channel since", err)
	}
	out := make([]Event, 0, len(rows))
	// TeamChannelMessages is newest first; the hub replays oldest first.
	for i := len(rows) - 1; i >= 0; i-- {
		row := &rows[i]
		payload, err := json.Marshal(t.topics.lineWire(row))
		if err != nil {
			return nil, errcode.Wrap(errcode.Unavailable, "marshal the team channel event", err)
		}
		out = append(out, Event{Seq: uint64(row.ChannelSeq), Type: channelEventName, Payload: payload})
	}
	return out, nil
}
