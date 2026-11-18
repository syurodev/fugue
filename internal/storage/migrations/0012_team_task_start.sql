-- Team task start path (F11.3a): the session a task runs in (TaskAssigned) and
-- the marker of a task that starts only on its delegator's explicit call
-- (Q-116: a task inserted by fugue_team_amend). Both are metadata (INV-09).

ALTER TABLE team_tasks ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE team_tasks ADD COLUMN explicit_start INTEGER NOT NULL DEFAULT 0 CHECK (explicit_start IN (0, 1));

CREATE INDEX IF NOT EXISTS idx_team_tasks_session
    ON team_tasks (session_id) WHERE session_id != '';
