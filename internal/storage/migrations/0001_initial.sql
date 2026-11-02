CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS exec_host (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    lock_generation INTEGER NOT NULL CHECK (lock_generation > 0),
    incarnation_id TEXT NOT NULL,
    acquired_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS vault_meta (
    id TEXT PRIMARY KEY,
    data BLOB NOT NULL
);

