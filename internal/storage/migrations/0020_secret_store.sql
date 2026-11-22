-- F10.15a: shared secret store (host-api §5.5, content-safety §3.2 Q-34).
-- Unsealed metadata columns are readable while the vault is locked (INV-09).
-- sealed_value is sealed via vault.SealRecord bound to AAD
-- ("secret_record", secret_id, scope_kind, scope_id, name).

CREATE TABLE secret_records (
    secret_id TEXT NOT NULL PRIMARY KEY,
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('global', 'workspace')),
    scope_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    key_id TEXT NOT NULL,
    sealed_value BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (scope_kind, scope_id, name)
);

-- Change journal behind the durable `secrets` topic. Carries metadata only,
-- never the secret value.
CREATE TABLE secret_events (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL CHECK (kind IN ('secret_put', 'secret_deleted')),
    secret_id TEXT NOT NULL,
    scope_kind TEXT NOT NULL CHECK (scope_kind IN ('global', 'workspace')),
    scope_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    recorded_at INTEGER NOT NULL
);
