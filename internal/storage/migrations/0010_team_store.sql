-- Team and task store (F11.1): metadata-only columns (INV-09) plus sealed
-- envelopes for protected task specs and event payloads.

CREATE TABLE IF NOT EXISTS team_records (
    team_id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    session_id TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN (
        'proposed', 'active', 'final_review', 'done', 'cancelled', 'failed'
    )),
    last_seq INTEGER NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    task_count INTEGER NOT NULL DEFAULT 0 CHECK (task_count >= 0),
    key_id TEXT NOT NULL DEFAULT '',
    wrapped_key BLOB,
    failure_code TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    ended_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_team_records_workspace
    ON team_records (workspace_id, created_at DESC, team_id);

CREATE TABLE IF NOT EXISTS team_tasks (
    task_id TEXT PRIMARY KEY,
    team_id TEXT NOT NULL REFERENCES team_records(team_id) ON DELETE CASCADE,
    parent_task_id TEXT NOT NULL DEFAULT '',
    hierarchy_role TEXT NOT NULL CHECK (hierarchy_role IN (
        'solo', 'lead', 'team_manager', 'member'
    )),
    catalog_role TEXT NOT NULL,
    deliverable TEXT NOT NULL CHECK (deliverable IN ('diff', 'report', 'both')),
    depends_on_json TEXT NOT NULL DEFAULT '[]',
    depth INTEGER NOT NULL CHECK (depth >= 0 AND depth <= 2),
    state TEXT NOT NULL CHECK (state IN (
        'queued', 'starting', 'running', 'asking', 'checking',
        'review', 'integrating', 'done', 'failed', 'stopped', 'cancelled', 'unknown'
    )),
    failure_code TEXT NOT NULL DEFAULT '',
    spec_envelope BLOB NOT NULL,
    last_event_seq INTEGER NOT NULL CHECK (last_event_seq >= 1),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    ended_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_team_tasks_team_state
    ON team_tasks (team_id, state, task_id);

CREATE INDEX IF NOT EXISTS idx_team_tasks_parent
    ON team_tasks (team_id, parent_task_id, task_id);

CREATE TABLE IF NOT EXISTS team_task_events (
    team_id TEXT NOT NULL REFERENCES team_records(team_id) ON DELETE CASCADE,
    sequence INTEGER NOT NULL CHECK (sequence >= 1),
    event_id TEXT NOT NULL,
    task_id TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL CHECK (event_type IN (
        'TeamProposed', 'TeamStarted', 'TeamFinalReview', 'TeamAmended', 'TeamDecided', 'TeamEnded',
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

CREATE TRIGGER IF NOT EXISTS team_task_events_require_contiguous_seq
BEFORE INSERT ON team_task_events
BEGIN
    SELECT CASE
        WHEN NEW.sequence != COALESCE((
            SELECT MAX(sequence) FROM team_task_events WHERE team_id = NEW.team_id
        ), 0) + 1
        THEN RAISE(ABORT, 'non-contiguous team_task_events sequence')
    END;
END;

CREATE TRIGGER IF NOT EXISTS team_task_events_append_only_update
BEFORE UPDATE ON team_task_events
BEGIN
    SELECT RAISE(ABORT, 'team_task_events is append-only');
END;

CREATE TRIGGER IF NOT EXISTS team_task_events_append_only_delete
BEFORE DELETE ON team_task_events
BEGIN
    SELECT RAISE(ABORT, 'team_task_events is append-only');
END;
