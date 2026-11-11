-- F5.6a: workspaces table metadata store (WS-04).
-- The files on disk are the source of truth for workspace configuration.
CREATE TABLE IF NOT EXISTS workspaces (
    workspace_id TEXT PRIMARY KEY,
    config_revision INTEGER NOT NULL DEFAULT 1,
    policy_revision INTEGER NOT NULL DEFAULT 1,
    config_hash TEXT NOT NULL,
    accepted_config_hash TEXT NOT NULL,
    missing_roots TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL
);
