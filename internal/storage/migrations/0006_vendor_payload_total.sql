ALTER TABLE vendor_sessions ADD COLUMN payload_bytes INTEGER NOT NULL DEFAULT 0 CHECK (payload_bytes >= 0);

-- Closed sessions must also be backfilled; restore the same trigger before commit.
DROP TRIGGER vendor_sessions_advance;
UPDATE vendor_sessions SET payload_bytes = COALESCE(
    (SELECT SUM(size) FROM vendor_payloads WHERE vendor_payloads.session_id = vendor_sessions.session_id), 0
);
CREATE TRIGGER vendor_sessions_advance BEFORE UPDATE ON vendor_sessions
WHEN NEW.session_id <> OLD.session_id OR NEW.last_sequence < OLD.last_sequence
 OR NEW.policy_revision < OLD.policy_revision OR OLD.state = 'closed'
BEGIN SELECT RAISE(ABORT,'vendor_sessions: closed is terminal and revisions never decrease'); END;
