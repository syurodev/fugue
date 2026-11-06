-- Execution timers projection (F2.6c, event-storage-recovery §7). Owner of
-- every table below: internal/execstore.
-- Durable timers track scheduled, elapsed, and cancelled timer states.

CREATE TABLE IF NOT EXISTS exec_timers (
    timer_id TEXT PRIMARY KEY CHECK (length(timer_id) BETWEEN 1 AND 128),
    session_id TEXT NOT NULL CHECK (length(session_id) BETWEEN 1 AND 128),
    run_id TEXT NOT NULL CHECK (length(run_id) BETWEEN 1 AND 128),
    generation INTEGER NOT NULL CHECK (generation >= 1),
    purpose TEXT NOT NULL CHECK (length(purpose) BETWEEN 1 AND 64),
    target_id TEXT NOT NULL CHECK (length(target_id) BETWEEN 1 AND 128),
    deadline TEXT NOT NULL CHECK (length(deadline) = 30),
    state TEXT NOT NULL CHECK (state IN ('armed', 'elapsed', 'cancelled')),
    FOREIGN KEY (run_id, session_id) REFERENCES exec_runs (run_id, session_id)
);

CREATE INDEX IF NOT EXISTS exec_timers_deadline ON exec_timers (deadline, timer_id)
    WHERE state = 'armed';
CREATE INDEX IF NOT EXISTS exec_timers_run ON exec_timers (run_id, session_id);

CREATE TRIGGER IF NOT EXISTS exec_timers_not_purging
BEFORE INSERT ON exec_timers
WHEN EXISTS (SELECT 1 FROM exec_purges WHERE session_id = NEW.session_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_timers: session is being purged');
END;

CREATE TRIGGER IF NOT EXISTS exec_timers_no_update_when_purging
BEFORE UPDATE ON exec_timers
WHEN EXISTS (SELECT 1 FROM exec_purges WHERE session_id = NEW.session_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_timers: session is being purged');
END;

CREATE TRIGGER IF NOT EXISTS exec_timers_purge_only_delete
BEFORE DELETE ON exec_timers
WHEN NOT EXISTS (SELECT 1 FROM exec_purges WHERE session_id = OLD.session_id)
BEGIN
    SELECT RAISE(ABORT, 'exec_timers: only a purge may delete a timer');
END;

CREATE TRIGGER IF NOT EXISTS exec_timers_immutable_id
BEFORE UPDATE ON exec_timers
WHEN NEW.timer_id <> OLD.timer_id
BEGIN
    SELECT RAISE(ABORT, 'exec_timers: timer_id is immutable');
END;

CREATE TRIGGER IF NOT EXISTS exec_timers_immutable_run
BEFORE UPDATE ON exec_timers
WHEN NEW.session_id <> OLD.session_id OR NEW.run_id <> OLD.run_id
BEGIN
    SELECT RAISE(ABORT, 'exec_timers: session_id and run_id are immutable');
END;

CREATE TRIGGER IF NOT EXISTS exec_timers_generation_advance
BEFORE UPDATE ON exec_timers
WHEN NEW.generation < OLD.generation
BEGIN
    SELECT RAISE(ABORT, 'exec_timers: generation must not decrease');
END;
