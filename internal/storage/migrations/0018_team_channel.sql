-- F11.10a: the team channel as a projection of team events (orchestration
-- v0.3 §6.7, Q-176 A, Q-178 A). team_channel_messages holds only the line
-- metadata (number, kind, thread, sender, recipient, source event reference
-- and a byte count); the content stays sealed in the source event payload
-- (INV-09), so the table is never a second source of truth. This is 0018:
-- 0017 is F10.20a's appstate migration.
--
-- informational marks a mailbox row that never schedules its own delivery:
-- it rides the recipient's next digest without waking them (§6.7: the
-- delegator sees sibling sends in its next digest, no separate wake).

CREATE TABLE team_channel_messages (
    team_id TEXT NOT NULL REFERENCES team_records(team_id) ON DELETE CASCADE,
    channel_seq INTEGER NOT NULL CHECK (channel_seq >= 1),
    kind TEXT NOT NULL CHECK (kind IN (
        'plan', 'decision', 'task', 'question', 'answer',
        'send', 'note', 'result', 'failed', 'stall', 'approval')),
    thread_root_task_id TEXT NOT NULL DEFAULT '',
    sender_kind TEXT NOT NULL CHECK (sender_kind IN ('agent', 'user', 'fugue', 'watchdog')),
    sender_task_id TEXT NOT NULL DEFAULT '',
    recipient_task_id TEXT NOT NULL DEFAULT '',
    source_seq INTEGER NOT NULL CHECK (source_seq >= 1),
    bytes INTEGER NOT NULL CHECK (bytes >= 0),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (team_id, channel_seq)
);

CREATE INDEX idx_team_channel_thread
    ON team_channel_messages (team_id, thread_root_task_id, channel_seq);

CREATE INDEX idx_team_channel_source
    ON team_channel_messages (team_id, source_seq);

CREATE TRIGGER team_channel_require_contiguous_seq
BEFORE INSERT ON team_channel_messages
BEGIN
    SELECT CASE
        WHEN NEW.channel_seq != COALESCE((
            SELECT MAX(channel_seq) FROM team_channel_messages WHERE team_id = NEW.team_id
        ), 0) + 1
        THEN RAISE(ABORT, 'non-contiguous team_channel_messages sequence')
    END;
END;

CREATE TRIGGER team_channel_append_only_update
BEFORE UPDATE ON team_channel_messages
BEGIN
    SELECT RAISE(ABORT, 'team_channel_messages is append-only');
END;

CREATE TRIGGER team_channel_append_only_delete
BEFORE DELETE ON team_channel_messages
BEGIN
    SELECT RAISE(ABORT, 'team_channel_messages is append-only');
END;

ALTER TABLE team_mailbox_messages ADD COLUMN informational INTEGER NOT NULL DEFAULT 0;
