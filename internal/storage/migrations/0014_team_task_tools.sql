-- F11.3c: persist decided model and effort on vendor_sessions for non-task
-- resume (carry-over #3 from #166), track the one-time no-report nudge on
-- team_tasks (Q-131), and allow TeamNoted in team_task_events (Q-130).
ALTER TABLE vendor_sessions ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE vendor_sessions ADD COLUMN effort TEXT NOT NULL DEFAULT '';
ALTER TABLE team_tasks ADD COLUMN no_report_nudged INTEGER NOT NULL DEFAULT 0;

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
        'TaskRecoveryDecided', 'TaskEnded'
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
