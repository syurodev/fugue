-- F11.6c: final rebuild of team_task_events dropping the event_type CHECK
-- constraint (validated in Go on the vendorstore append path so future event
-- types need no rebuild), add waiting_reason on team_records for
-- waiting(team_deadline), and add durable team_timers keyed by
-- (team_id, task_id, kind).

ALTER TABLE team_task_events RENAME TO team_task_events_old;

CREATE TABLE team_task_events (
    team_id TEXT NOT NULL REFERENCES team_records(team_id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL CHECK (sequence >= 1),
    event_id TEXT NOT NULL,
    task_id TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    outcome TEXT NOT NULL DEFAULT '',
    from_state TEXT NOT NULL DEFAULT '',
    to_state TEXT NOT NULL DEFAULT '',
    failure_code TEXT NOT NULL DEFAULT '',
    payload_envelope BLOB,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (team_id, sequence),
    UNIQUE (team_id, event_id)
);

INSERT INTO team_task_events
SELECT team_id, sequence, event_id, task_id, event_type, outcome, from_state, to_state, failure_code, payload_envelope, created_at
FROM team_task_events_old;

DROP TABLE team_task_events_old;

CREATE TRIGGER team_task_events_require_contiguous_seq
BEFORE INSERT ON team_task_events
BEGIN
    SELECT CASE
        WHEN NEW.sequence != COALESCE((
            SELECT MAX(sequence) FROM team_task_events WHERE team_id = NEW.team_id
        ), 0) + 1
        THEN RAISE(ABORT, 'non-contiguous team_task_events sequence')
    END;
END;

CREATE TRIGGER team_task_events_append_only_update
BEFORE UPDATE ON team_task_events
BEGIN
    SELECT RAISE(ABORT, 'team_task_events is append-only');
END;

CREATE TRIGGER team_task_events_append_only_delete
BEFORE DELETE ON team_task_events
BEGIN
    SELECT RAISE(ABORT, 'team_task_events is append-only');
END;

ALTER TABLE team_records ADD COLUMN waiting_reason TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS team_timers (
    team_id TEXT NOT NULL REFERENCES team_records(team_id) ON DELETE CASCADE,
    task_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (length(kind) BETWEEN 1 AND 64),
    deadline_ms INTEGER NOT NULL,
    generation INTEGER NOT NULL DEFAULT 1 CHECK (generation >= 1),
    state TEXT NOT NULL CHECK (state IN ('armed', 'fired', 'cancelled')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (team_id, task_id, kind)
);

CREATE INDEX IF NOT EXISTS idx_team_timers_armed
    ON team_timers (state, deadline_ms, team_id, task_id, kind)
    WHERE state = 'armed';
