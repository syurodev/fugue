-- F11.3b: the team mailbox (Q-122), reserved for F11.3 by ADR 0002. Metadata
-- columns only: the message text is sealed with the team key in
-- payload_envelope. message_id = <team>:<source event seq>:<recipient task>,
-- so enqueue is idempotent against the event log sequence.
CREATE TABLE IF NOT EXISTS team_mailbox_messages (
    message_id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL REFERENCES team_records(team_id) ON DELETE CASCADE,
    recipient_task_id TEXT NOT NULL,
    batch_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (kind IN ('digest','answer','send','decision')),
    state TEXT NOT NULL CHECK (state IN ('pending','delivering','delivered')),
    payload_envelope BLOB NOT NULL,
    source_seq INTEGER NOT NULL CHECK (source_seq >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_team_mailbox_pending
    ON team_mailbox_messages (team_id, recipient_task_id, state);
CREATE INDEX IF NOT EXISTS idx_team_mailbox_batch
    ON team_mailbox_messages (batch_id) WHERE batch_id != '';
