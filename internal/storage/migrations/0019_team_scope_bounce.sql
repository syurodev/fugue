-- F11.5a: durable write-scope bounce counter on team_tasks (orchestration §10.1, OR-E04).
ALTER TABLE team_tasks ADD COLUMN scope_bounces INTEGER NOT NULL DEFAULT 0 CHECK (scope_bounces >= 0);
