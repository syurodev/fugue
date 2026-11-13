-- The cross-session change journal behind the durable vendor-sessions topic
-- (F9.9c, Q-58 A). Per-session event sequences cannot order one topic, and
-- AUTOINCREMENT never reuses a sequence, so a purge cannot regress a
-- published cursor. Rows carry state metadata only, never content.
CREATE TABLE vendor_session_changes (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('created','state_changed')),
    state TEXT NOT NULL,
    waiting_reason TEXT NOT NULL DEFAULT '',
    recorded_at TEXT NOT NULL
);

-- A session is bound to the workspace and root it was created in; resume
-- reuses them and never takes new ones from the client (workspace §2).
ALTER TABLE vendor_sessions ADD COLUMN workspace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE vendor_sessions ADD COLUMN workspace_root TEXT NOT NULL DEFAULT '';
