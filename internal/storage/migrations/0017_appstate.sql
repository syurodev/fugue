-- F10.20a: the app's window layouts and preferences, stored outside the
-- vault so the app rebuilds every window before the vault unlocks
-- (host-api §5.2). Documents are versioned JSON the app owns: the host
-- validates the envelope in Go and never interprets slot contents. Revisions
-- start at 1 and are the CAS tokens PutWindow/DeleteWindow/PutPreferences
-- expect.

CREATE TABLE app_windows (
    window_id TEXT NOT NULL PRIMARY KEY,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    layout BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE app_preferences (
    id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    preferences BLOB NOT NULL,
    updated_at INTEGER NOT NULL
);

-- The change journal behind the durable `appstate` topic. Rows carry
-- identity and revision metadata only, never a document. AUTOINCREMENT never
-- reuses a sequence (F9.9c), so pruning cannot regress a published cursor; a
-- cursor older than the oldest kept row is a gap and fails typed instead of
-- catching up over silent holes.
CREATE TABLE app_state_events (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL CHECK (kind IN ('window_put', 'window_deleted', 'preferences_put')),
    window_id TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL,
    recorded_at INTEGER NOT NULL
);
