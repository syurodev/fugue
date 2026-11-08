-- F9.3b: vendorstore owns these tables. Only envelopes and wrapped keys
-- contain protected content; all other columns are allowlisted metadata.
CREATE TABLE vendor_sessions (
    session_id TEXT PRIMARY KEY CHECK (length(session_id) BETWEEN 1 AND 128),
    vendor TEXT NOT NULL, account_id TEXT NOT NULL, role TEXT NOT NULL,
    team_id TEXT, task_id TEXT,
    state TEXT NOT NULL CHECK (state IN ('starting','ready','running','awaiting_approval','waiting','detached','closed')),
    waiting_reason TEXT NOT NULL DEFAULT '' CHECK (waiting_reason IN ('','vault_locked','quota','capacity')),
    approval_mode TEXT NOT NULL DEFAULT 'auto' CHECK (approval_mode IN ('plan','ask','auto','full')), policy_revision INTEGER NOT NULL DEFAULT 0 CHECK (policy_revision >= 0),
    resume_handle TEXT NOT NULL DEFAULT '', process_pid INTEGER NOT NULL DEFAULT 0 CHECK (process_pid >= 0),
    process_start_time TEXT NOT NULL DEFAULT '', close_reason TEXT NOT NULL DEFAULT '', stop_proof TEXT NOT NULL DEFAULT '' CHECK (stop_proof IN ('','proved','unknown')),
    last_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_sequence >= 0),
    created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
    CHECK (process_pid = 0 OR length(process_start_time) > 0),
    CHECK ((state = 'waiting' AND waiting_reason <> '') OR (state <> 'waiting' AND waiting_reason = '')),
    CHECK ((state = 'closed' AND stop_proof <> '') OR (state <> 'closed' AND stop_proof = ''))
);
CREATE TABLE vendor_turns (
    session_id TEXT NOT NULL REFERENCES vendor_sessions(session_id), turn_id TEXT NOT NULL CHECK (length(turn_id) BETWEEN 1 AND 128),
    outcome TEXT NOT NULL DEFAULT '' CHECK (outcome IN ('','completed','interrupted','failed','unknown')),
    failure_code TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, ended_at TEXT,
    PRIMARY KEY (session_id, turn_id)
);
CREATE TABLE vendor_session_keys (
    session_id TEXT PRIMARY KEY CHECK (length(session_id) BETWEEN 1 AND 128), key_id TEXT NOT NULL,
    wrapped_key BLOB NOT NULL CHECK (length(wrapped_key) > 0), created_at TEXT NOT NULL, shredded_at TEXT,
    UNIQUE (session_id, key_id)
);
CREATE TABLE vendor_purges (session_id TEXT PRIMARY KEY CHECK (length(session_id) BETWEEN 1 AND 128), requested_at TEXT NOT NULL);
CREATE TABLE vendor_payloads (
    payload_id TEXT PRIMARY KEY CHECK (length(payload_id) BETWEEN 1 AND 128), session_id TEXT NOT NULL, kind TEXT NOT NULL, key_id TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0), envelope BLOB NOT NULL CHECK (length(envelope) > 0),
    UNIQUE (payload_id, session_id), FOREIGN KEY (session_id, key_id) REFERENCES vendor_session_keys(session_id, key_id)
);
CREATE INDEX vendor_payloads_session ON vendor_payloads(session_id);
CREATE TABLE vendor_events (
    session_id TEXT NOT NULL REFERENCES vendor_sessions(session_id), sequence INTEGER NOT NULL CHECK (sequence >= 1),
    metadata TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(metadata)),
    event_type TEXT NOT NULL, recorded_at TEXT NOT NULL, payload_id TEXT,
    PRIMARY KEY(session_id, sequence), FOREIGN KEY(payload_id, session_id) REFERENCES vendor_payloads(payload_id, session_id)
);
CREATE TABLE vendor_transcript_items (
    session_id TEXT NOT NULL, item_id TEXT NOT NULL CHECK (length(item_id) BETWEEN 1 AND 128), turn_id TEXT NOT NULL CHECK (length(turn_id) BETWEEN 1 AND 128), kind TEXT NOT NULL,
    metadata TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(metadata)),
    sequence INTEGER NOT NULL CHECK (sequence >= 1), revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at TEXT NOT NULL, updated_at TEXT NOT NULL, payload_id TEXT, raw_payload_id TEXT,
    PRIMARY KEY(session_id, item_id), FOREIGN KEY(session_id, turn_id) REFERENCES vendor_turns(session_id, turn_id),
    FOREIGN KEY(session_id, sequence) REFERENCES vendor_events(session_id, sequence),
    FOREIGN KEY(payload_id, session_id) REFERENCES vendor_payloads(payload_id, session_id),
    FOREIGN KEY(raw_payload_id, session_id) REFERENCES vendor_payloads(payload_id, session_id)
);
CREATE INDEX vendor_transcript_session_sequence ON vendor_transcript_items(session_id, sequence);
CREATE TRIGGER vendor_events_contiguous BEFORE INSERT ON vendor_events
WHEN NEW.sequence <> (SELECT COALESCE(MAX(sequence),0)+1 FROM vendor_events WHERE session_id=NEW.session_id)
BEGIN SELECT RAISE(ABORT,'vendor_events: sequence must be contiguous'); END;
CREATE TRIGGER vendor_events_not_purging BEFORE INSERT ON vendor_events
WHEN EXISTS (SELECT 1 FROM vendor_purges WHERE session_id=NEW.session_id)
BEGIN SELECT RAISE(ABORT,'vendor_events: session is being purged'); END;
CREATE TRIGGER vendor_events_no_update BEFORE UPDATE ON vendor_events
BEGIN SELECT RAISE(ABORT,'vendor_events is append-only'); END;
CREATE TRIGGER vendor_events_purge_only_delete BEFORE DELETE ON vendor_events
WHEN NOT EXISTS (SELECT 1 FROM vendor_purges WHERE session_id=OLD.session_id)
BEGIN SELECT RAISE(ABORT,'vendor_events is append-only'); END;
CREATE TRIGGER vendor_payloads_not_purging BEFORE INSERT ON vendor_payloads
WHEN EXISTS (SELECT 1 FROM vendor_purges WHERE session_id=NEW.session_id)
BEGIN SELECT RAISE(ABORT,'vendor_payloads: session is being purged'); END;
CREATE TRIGGER vendor_payloads_no_update BEFORE UPDATE ON vendor_payloads
BEGIN SELECT RAISE(ABORT,'vendor_payloads is append-only'); END;
CREATE TRIGGER vendor_payloads_purge_only_delete BEFORE DELETE ON vendor_payloads
WHEN NOT EXISTS (SELECT 1 FROM vendor_purges WHERE session_id=OLD.session_id)
BEGIN SELECT RAISE(ABORT,'vendor_payloads is append-only'); END;
CREATE TRIGGER vendor_session_keys_not_purging BEFORE INSERT ON vendor_session_keys
WHEN EXISTS (SELECT 1 FROM vendor_purges WHERE session_id=NEW.session_id)
BEGIN SELECT RAISE(ABORT,'vendor_session_keys: session is being purged'); END;
CREATE TRIGGER vendor_session_keys_shred_only BEFORE UPDATE ON vendor_session_keys
WHEN NOT EXISTS (SELECT 1 FROM vendor_purges WHERE session_id=OLD.session_id)
 OR NEW.session_id <> OLD.session_id OR NEW.key_id <> OLD.key_id OR NEW.created_at <> OLD.created_at
 OR NEW.wrapped_key <> zeroblob(length(OLD.wrapped_key)) OR NEW.shredded_at IS NULL
BEGIN SELECT RAISE(ABORT,'vendor_session_keys: only a purge may shred a wrapped key'); END;
CREATE TRIGGER vendor_session_keys_purge_only_delete BEFORE DELETE ON vendor_session_keys
WHEN NOT EXISTS (SELECT 1 FROM vendor_purges WHERE session_id=OLD.session_id)
BEGIN SELECT RAISE(ABORT,'vendor_session_keys: only a purge may delete a key'); END;
CREATE TRIGGER vendor_purges_no_update BEFORE UPDATE ON vendor_purges
BEGIN SELECT RAISE(ABORT,'vendor_purges is permanent'); END;
CREATE TRIGGER vendor_purges_no_delete BEFORE DELETE ON vendor_purges
BEGIN SELECT RAISE(ABORT,'vendor_purges is permanent'); END;
CREATE TRIGGER vendor_sessions_advance BEFORE UPDATE ON vendor_sessions
WHEN NEW.session_id <> OLD.session_id OR NEW.last_sequence < OLD.last_sequence
 OR NEW.policy_revision < OLD.policy_revision OR OLD.state = 'closed'
BEGIN SELECT RAISE(ABORT,'vendor_sessions: closed is terminal and revisions never decrease'); END;
CREATE TRIGGER vendor_turns_outcome_once BEFORE UPDATE ON vendor_turns
WHEN NEW.session_id <> OLD.session_id OR NEW.turn_id <> OLD.turn_id OR OLD.outcome <> ''
BEGIN SELECT RAISE(ABORT,'vendor_turns: outcome is immutable'); END;
CREATE TRIGGER vendor_items_advance BEFORE UPDATE ON vendor_transcript_items
WHEN NEW.session_id <> OLD.session_id OR NEW.item_id <> OLD.item_id OR NEW.turn_id <> OLD.turn_id
 OR NEW.sequence <> OLD.sequence OR NEW.created_at <> OLD.created_at OR NEW.revision <> OLD.revision + 1
BEGIN SELECT RAISE(ABORT,'vendor_transcript_items: identity is immutable and revision advances'); END;
