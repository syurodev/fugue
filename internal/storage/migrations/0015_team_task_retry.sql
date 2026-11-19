-- F11.3d: track retry attempt lineage on team_tasks (Q-126) and allow
-- TaskSuperseded in team_task_events (failed -> stopped).
ALTER TABLE team_tasks ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1 CHECK (attempt >= 1);
ALTER TABLE team_tasks ADD COLUMN retry_of TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_team_tasks_retry
    ON team_tasks (team_id, retry_of) WHERE retry_of != '';

ALTER TABLE team_task_events RENAME TO team_task_events_old;

CREATE TABLE team_task_events (
    team_id TEXT NOT NULL REFERENCES team_records(team_id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL CHECK (sequence >= 1),
    event_id TEXT NOT NULL,
    task_id TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL CHECK (event_type IN (
        'TeamProposed', 'TeamStarted', 'TeamFinalReview', 'TeamAmended', 'TeamNoted', 'TeamDecided', 'TeamEnded',
        'TaskCreated', 'TaskAssigned', 'TaskStarted', 'TaskQuestionAsked',
        'TaskQuestionAnswered', 'TaskReported', 'TaskCheckCompleted',
        'TaskReviewed', 'TaskIntegrated', 'TaskMarkedUnknown',
        'TaskRecoveryDecided', 'TaskSuperseded', 'TaskEnded'
    )),
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
