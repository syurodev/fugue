-- FUG-110: workspace memory entries and immutable write history (workspace-memory.mdx §2-§3).
-- Plaintext in host DB outside vault; FTS5 indexes belong to FUG-114.

CREATE TABLE workspace_memory_entries (
    id TEXT NOT NULL PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('pinned', 'fact', 'decision', 'convention', 'howto', 'note')),
    label TEXT NOT NULL,
    summary TEXT NOT NULL,
    body TEXT NOT NULL,
    keywords_json TEXT NOT NULL DEFAULT '[]',
    anchors_json TEXT NOT NULL DEFAULT '[]',
    author TEXT NOT NULL CHECK (author IN ('user', 'agent')),
    verified INTEGER NOT NULL CHECK (verified IN (0, 1)),
    validity TEXT NOT NULL CHECK (validity IN ('valid', 'superseded', 'invalid')),
    invalid_reason TEXT NOT NULL DEFAULT '',
    superseded_by TEXT NOT NULL DEFAULT '',
    provenance_json TEXT NOT NULL DEFAULT '{}',
    revision INTEGER NOT NULL CHECK (revision >= 1),
    updated_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_workspace_memory_valid_label
    ON workspace_memory_entries (workspace_id, label)
    WHERE validity = 'valid';

CREATE INDEX idx_workspace_memory_entries_workspace
    ON workspace_memory_entries (workspace_id, validity, updated_at DESC);

CREATE TABLE workspace_memory_history (
    history_id INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    op TEXT NOT NULL CHECK (op IN ('add', 'update', 'invalidate', 'restore', 'verify')),
    kind TEXT NOT NULL CHECK (kind IN ('pinned', 'fact', 'decision', 'convention', 'howto', 'note')),
    label TEXT NOT NULL,
    summary TEXT NOT NULL,
    body TEXT NOT NULL,
    keywords_json TEXT NOT NULL DEFAULT '[]',
    anchors_json TEXT NOT NULL DEFAULT '[]',
    author TEXT NOT NULL CHECK (author IN ('user', 'agent')),
    verified INTEGER NOT NULL CHECK (verified IN (0, 1)),
    validity TEXT NOT NULL CHECK (validity IN ('valid', 'superseded', 'invalid')),
    invalid_reason TEXT NOT NULL DEFAULT '',
    superseded_by TEXT NOT NULL DEFAULT '',
    provenance_json TEXT NOT NULL DEFAULT '{}',
    session_id TEXT NOT NULL DEFAULT '',
    written_at INTEGER NOT NULL,
    UNIQUE (entry_id, revision)
);

CREATE INDEX idx_workspace_memory_history_entry
    ON workspace_memory_history (entry_id, revision DESC);

CREATE INDEX idx_workspace_memory_history_session_rate
    ON workspace_memory_history (session_id, written_at)
    WHERE session_id != '';
