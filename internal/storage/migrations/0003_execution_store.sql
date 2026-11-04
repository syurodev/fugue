-- Execution store (F2.4, storage-security §3, Q-13 option A). Owner of every
-- table below: internal/execstore. Every column except exec_payloads.envelope
-- and exec_session_keys.wrapped_key holds allowlisted control metadata only.
-- Size limits, key creation/wrapping and purge orchestration belong to F2.5;
-- the triggers here only make those operations possible and everything else
-- impossible.

CREATE TABLE IF NOT EXISTS exec_session_keys (
    session_id TEXT PRIMARY KEY CHECK (length(session_id) BETWEEN 1 AND 128),
    key_id TEXT NOT NULL CHECK (length(key_id) BETWEEN 1 AND 128),
    wrapped_key BLOB NOT NULL CHECK (length(wrapped_key) > 0),
    created_at TEXT NOT NULL CHECK (length(created_at) > 0),
    shredded_at TEXT,
    UNIQUE (session_id, key_id)
);

CREATE TABLE IF NOT EXISTS exec_purges (
    session_id TEXT PRIMARY KEY CHECK (length(session_id) BETWEEN 1 AND 128),
    requested_at TEXT NOT NULL CHECK (length(requested_at) > 0)
);

CREATE TABLE IF NOT EXISTS exec_payloads (
    payload_id TEXT PRIMARY KEY CHECK (length(payload_id) BETWEEN 1 AND 128),
    session_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (
        length(kind) BETWEEN 1 AND 64 AND kind NOT GLOB '*[^a-z_]*'
    ),
    key_id TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    envelope BLOB NOT NULL CHECK (length(envelope) > 0),
    UNIQUE (payload_id, session_id),
    FOREIGN KEY (session_id, key_id) REFERENCES exec_session_keys (session_id, key_id)
);

CREATE INDEX IF NOT EXISTS exec_payloads_session_key ON exec_payloads (session_id, key_id);

CREATE TABLE IF NOT EXISTS exec_admission (
    session_id TEXT PRIMARY KEY CHECK (length(session_id) BETWEEN 1 AND 128),
    active_run_id TEXT,
    owner_incarnation INTEGER NOT NULL CHECK (owner_incarnation >= 0),
    admission_token INTEGER NOT NULL CHECK (admission_token >= 0),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    queue_revision INTEGER NOT NULL CHECK (queue_revision >= 0),
    context_epoch INTEGER NOT NULL CHECK (context_epoch >= 0),
    context_start_run_id TEXT,
    context_revision INTEGER NOT NULL CHECK (context_revision >= 0),
    context_requirement TEXT NOT NULL CHECK (
        context_requirement IN ('none', 'revalidation_required', 'fresh_required')
    ),
    context_cause_run_id TEXT,
    context_decided_event_id TEXT REFERENCES exec_events (event_id),
    CHECK (active_run_id IS NULL OR (admission_token >= 1 AND owner_incarnation >= 1)),
    FOREIGN KEY (active_run_id, session_id) REFERENCES exec_runs (run_id, session_id)
);

CREATE TABLE IF NOT EXISTS exec_runs (
    run_id TEXT PRIMARY KEY CHECK (length(run_id) BETWEEN 1 AND 128),
    session_id TEXT NOT NULL REFERENCES exec_admission (session_id),
    last_sequence INTEGER NOT NULL CHECK (last_sequence >= 1),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    protocol_version INTEGER NOT NULL CHECK (protocol_version >= 1),
    lifecycle TEXT NOT NULL CHECK (
        lifecycle IN ('queued', 'running', 'waiting', 'cancelling', 'interrupted', 'finished')
    ),
    phase TEXT NOT NULL CHECK (
        phase IN ('preparing', 'calling_model', 'processing_tools', 'finalizing')
    ),
    wait_reasons TEXT NOT NULL CHECK (
        json_valid(wait_reasons) AND json_type(wait_reasons) = 'array'
    ),
    outcome TEXT NOT NULL CHECK (
        outcome IN (
            '', 'completed', 'cancelled', 'budget_exceeded', 'failed', 'abandoned', 'abandoned_unknown'
        )
    ),
    admission_token INTEGER NOT NULL CHECK (admission_token >= 0),
    step_index INTEGER NOT NULL CHECK (step_index >= 0),
    context_epoch INTEGER NOT NULL CHECK (context_epoch >= 0),
    UNIQUE (run_id, session_id)
);

CREATE INDEX IF NOT EXISTS exec_runs_session ON exec_runs (session_id);

CREATE TABLE IF NOT EXISTS exec_events (
    run_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence >= 1),
    event_id TEXT NOT NULL UNIQUE CHECK (length(event_id) BETWEEN 1 AND 128),
    session_id TEXT NOT NULL,
    event_type TEXT NOT NULL CHECK (length(event_type) BETWEEN 1 AND 64),
    schema_version INTEGER NOT NULL CHECK (schema_version >= 1),
    protocol_version INTEGER NOT NULL CHECK (protocol_version >= 1),
    command_id TEXT NOT NULL CHECK (length(command_id) BETWEEN 1 AND 128),
    causation_id TEXT NOT NULL CHECK (length(causation_id) BETWEEN 1 AND 128),
    correlation_id TEXT NOT NULL CHECK (length(correlation_id) BETWEEN 1 AND 128),
    origin TEXT NOT NULL CHECK (
        origin IN ('user', 'parent_session', 'session', 'model', 'worker', 'timer', 'recovery', 'system')
    ),
    writer_incarnation INTEGER NOT NULL CHECK (writer_incarnation >= 1),
    admission_token INTEGER NOT NULL CHECK (admission_token >= 0),
    queue_revision INTEGER NOT NULL CHECK (queue_revision >= 0),
    recorded_at TEXT NOT NULL CHECK (length(recorded_at) > 0),
    data TEXT NOT NULL CHECK (json_valid(data) AND json_type(data) = 'object'),
    payload_id TEXT,
    PRIMARY KEY (run_id, sequence),
    FOREIGN KEY (run_id, session_id) REFERENCES exec_runs (run_id, session_id),
    FOREIGN KEY (payload_id, session_id) REFERENCES exec_payloads (payload_id, session_id)
);

CREATE INDEX IF NOT EXISTS exec_events_session ON exec_events (session_id);
CREATE INDEX IF NOT EXISTS exec_events_payload ON exec_events (payload_id) WHERE payload_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS exec_invocations (
    run_id TEXT NOT NULL REFERENCES exec_runs (run_id),
    invocation_id TEXT NOT NULL CHECK (length(invocation_id) BETWEEN 1 AND 128),
    kind TEXT NOT NULL CHECK (kind IN ('model', 'tool')),
    state TEXT NOT NULL CHECK (state IN ('open', 'completed', 'failed', 'closed')),
    selected_attempt_id TEXT NOT NULL,
    effect_class TEXT NOT NULL CHECK (
        effect_class IN ('', 'pure_read', 'idempotent', 'side_effecting')
    ),
    descriptor_version TEXT NOT NULL,
    check_generation INTEGER NOT NULL CHECK (check_generation >= 0),
    source_position INTEGER NOT NULL CHECK (source_position >= 0),
    exposure TEXT NOT NULL CHECK (exposure IN ('none', 'unknown')),
    PRIMARY KEY (run_id, invocation_id)
);

CREATE TABLE IF NOT EXISTS exec_attempts (
    run_id TEXT NOT NULL,
    attempt_id TEXT NOT NULL CHECK (length(attempt_id) BETWEEN 1 AND 128),
    invocation_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (
        status IN (
            'planned', 'requesting', 'streaming', 'completed', 'failed', 'interrupted', 'cancelled',
            'abandoned_unknown', 'checking', 'awaiting_approval', 'ready', 'dispatching', 'succeeded',
            'denied', 'refused', 'outcome_unknown'
        )
    ),
    owner_incarnation INTEGER NOT NULL CHECK (owner_incarnation >= 0),
    executor_fence INTEGER NOT NULL CHECK (executor_fence >= 0),
    exposure_unknown INTEGER NOT NULL CHECK (exposure_unknown IN (0, 1)),
    PRIMARY KEY (run_id, attempt_id),
    FOREIGN KEY (run_id, invocation_id) REFERENCES exec_invocations (run_id, invocation_id)
);

CREATE INDEX IF NOT EXISTS exec_attempts_invocation ON exec_attempts (run_id, invocation_id);

CREATE TABLE IF NOT EXISTS exec_receipts (
    command_id TEXT PRIMARY KEY CHECK (length(command_id) BETWEEN 1 AND 128),
    session_id TEXT NOT NULL CHECK (length(session_id) BETWEEN 1 AND 128),
    run_id TEXT NOT NULL CHECK (length(run_id) BETWEEN 1 AND 128),
    kind TEXT NOT NULL CHECK (length(kind) BETWEEN 1 AND 64),
    principal TEXT NOT NULL CHECK (length(principal) BETWEEN 1 AND 128),
    target TEXT NOT NULL CHECK (length(target) BETWEEN 1 AND 128),
    codec_version TEXT NOT NULL CHECK (length(codec_version) BETWEEN 1 AND 16),
    input_payload_id TEXT,
    metadata_input BLOB,
    result TEXT NOT NULL CHECK (result IN ('applied', 'noop', 'rejected')),
    first_sequence INTEGER NOT NULL CHECK (first_sequence >= 0),
    last_sequence INTEGER NOT NULL CHECK (last_sequence >= 0),
    rejection_code TEXT NOT NULL,
    recorded_at TEXT NOT NULL CHECK (length(recorded_at) > 0),
    CHECK ((input_payload_id IS NULL) <> (metadata_input IS NULL)),
    CHECK (
        (result = 'applied' AND first_sequence >= 1 AND last_sequence >= first_sequence AND rejection_code = '')
        OR (result = 'noop' AND first_sequence = 0 AND last_sequence = 0 AND rejection_code = '')
        OR (result = 'rejected' AND first_sequence = 0 AND last_sequence = 0
            AND length(rejection_code) BETWEEN 1 AND 64)
    ),
    FOREIGN KEY (input_payload_id, session_id) REFERENCES exec_payloads (payload_id, session_id)
);

CREATE INDEX IF NOT EXISTS exec_receipts_payload ON exec_receipts (input_payload_id)
    WHERE input_payload_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS exec_transcript (
    session_id TEXT NOT NULL,
    context_epoch INTEGER NOT NULL CHECK (context_epoch >= 0),
    position INTEGER NOT NULL CHECK (position >= 1),
    run_id TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('input', 'assistant', 'tool_result')),
    call_id TEXT NOT NULL,
    source_position INTEGER NOT NULL CHECK (source_position >= 0),
    payload_id TEXT,
    disposition TEXT NOT NULL CHECK (length(disposition) <= 64),
    event_sequence INTEGER NOT NULL,
    PRIMARY KEY (session_id, context_epoch, position),
    CHECK (payload_id IS NOT NULL OR length(disposition) > 0),
    FOREIGN KEY (run_id, session_id) REFERENCES exec_runs (run_id, session_id),
    FOREIGN KEY (run_id, event_sequence) REFERENCES exec_events (run_id, sequence),
    FOREIGN KEY (payload_id, session_id) REFERENCES exec_payloads (payload_id, session_id)
);

CREATE INDEX IF NOT EXISTS exec_transcript_run ON exec_transcript (run_id, event_sequence);
CREATE INDEX IF NOT EXISTS exec_transcript_payload ON exec_transcript (payload_id)
    WHERE payload_id IS NOT NULL;

-- Revisions and fencing tokens only move forward; a session's admission row
-- never changes identity.
CREATE TRIGGER IF NOT EXISTS exec_admission_advances
BEFORE UPDATE ON exec_admission
WHEN NEW.session_id <> OLD.session_id
    OR NEW.revision <> OLD.revision + 1
    OR NEW.admission_token < OLD.admission_token
    OR NEW.queue_revision < OLD.queue_revision
    OR NEW.context_epoch < OLD.context_epoch
    OR NEW.context_revision < OLD.context_revision
BEGIN
    SELECT RAISE(ABORT, 'exec_admission: revision must advance by one and tokens never decrease');
END;

CREATE TRIGGER IF NOT EXISTS exec_runs_advance
BEFORE UPDATE ON exec_runs
WHEN NEW.run_id <> OLD.run_id
    OR NEW.session_id <> OLD.session_id
    OR NEW.revision <= OLD.revision
    OR NEW.last_sequence < OLD.last_sequence
BEGIN
    SELECT RAISE(ABORT, 'exec_runs: identity is immutable and revision must advance');
END;

-- Events are contiguous from 1 per run and append-only. DELETE is allowed only
-- for a session recorded in exec_purges; nothing may UPDATE an event.
CREATE TRIGGER IF NOT EXISTS exec_events_contiguous
BEFORE INSERT ON exec_events
WHEN NEW.sequence <> (SELECT COALESCE(MAX(sequence), 0) + 1 FROM exec_events WHERE run_id = NEW.run_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_events: sequence must be contiguous');
END;

CREATE TRIGGER IF NOT EXISTS exec_events_not_purging
BEFORE INSERT ON exec_events
WHEN EXISTS (SELECT 1 FROM exec_purges WHERE session_id = NEW.session_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_events: session is being purged');
END;

CREATE TRIGGER IF NOT EXISTS exec_events_no_update
BEFORE UPDATE ON exec_events
BEGIN
    SELECT RAISE(ABORT, 'exec_events is append-only');
END;

CREATE TRIGGER IF NOT EXISTS exec_events_purge_only_delete
BEFORE DELETE ON exec_events
WHEN NOT EXISTS (SELECT 1 FROM exec_purges WHERE session_id = OLD.session_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_events is append-only');
END;

-- Sealed payloads are inserted once and never updated; DELETE only under purge.
CREATE TRIGGER IF NOT EXISTS exec_payloads_not_purging
BEFORE INSERT ON exec_payloads
WHEN EXISTS (SELECT 1 FROM exec_purges WHERE session_id = NEW.session_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_payloads: session is being purged');
END;

CREATE TRIGGER IF NOT EXISTS exec_payloads_no_update
BEFORE UPDATE ON exec_payloads
BEGIN
    SELECT RAISE(ABORT, 'exec_payloads is append-only');
END;

CREATE TRIGGER IF NOT EXISTS exec_payloads_purge_only_delete
BEFORE DELETE ON exec_payloads
WHEN NOT EXISTS (SELECT 1 FROM exec_purges WHERE session_id = OLD.session_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_payloads is append-only');
END;

-- Receipts bind a command ID to its input and result forever.
CREATE TRIGGER IF NOT EXISTS exec_receipts_no_update
BEFORE UPDATE ON exec_receipts
BEGIN
    SELECT RAISE(ABORT, 'exec_receipts are immutable');
END;

CREATE TRIGGER IF NOT EXISTS exec_receipts_purge_only_delete
BEFORE DELETE ON exec_receipts
WHEN NOT EXISTS (SELECT 1 FROM exec_purges WHERE session_id = OLD.session_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_receipts are immutable');
END;

-- A wrapped session key changes only during a purge, and then only to a
-- same-length zeroblob with shredded_at set (storage-security §7).
CREATE TRIGGER IF NOT EXISTS exec_session_keys_shred_only
BEFORE UPDATE ON exec_session_keys
WHEN NOT EXISTS (SELECT 1 FROM exec_purges WHERE session_id = OLD.session_id)
    OR NEW.session_id <> OLD.session_id
    OR NEW.key_id <> OLD.key_id
    OR NEW.created_at <> OLD.created_at
    OR NEW.wrapped_key <> zeroblob(length(OLD.wrapped_key))
    OR NEW.shredded_at IS NULL
BEGIN
    SELECT RAISE(ABORT, 'exec_session_keys: only a purge may shred a wrapped key');
END;

CREATE TRIGGER IF NOT EXISTS exec_session_keys_purge_only_delete
BEFORE DELETE ON exec_session_keys
WHEN NOT EXISTS (SELECT 1 FROM exec_purges WHERE session_id = OLD.session_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_session_keys: only a purge may delete a key');
END;

CREATE TRIGGER IF NOT EXISTS exec_session_keys_not_purging
BEFORE INSERT ON exec_session_keys
WHEN EXISTS (SELECT 1 FROM exec_purges WHERE session_id = NEW.session_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_session_keys: session is being purged');
END;

-- A purge request is a permanent tombstone.
CREATE TRIGGER IF NOT EXISTS exec_purges_no_update
BEFORE UPDATE ON exec_purges
BEGIN
    SELECT RAISE(ABORT, 'exec_purges is permanent');
END;

CREATE TRIGGER IF NOT EXISTS exec_purges_no_delete
BEFORE DELETE ON exec_purges
BEGIN
    SELECT RAISE(ABORT, 'exec_purges is permanent');
END;

