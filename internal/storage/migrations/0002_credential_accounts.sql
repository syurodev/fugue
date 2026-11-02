CREATE TABLE IF NOT EXISTS credential_accounts (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider IN ('anthropic', 'openai')),
    kind TEXT NOT NULL CHECK (kind IN ('api_key', 'chatgpt')),
    label TEXT NOT NULL,
    signed_in INTEGER NOT NULL CHECK (signed_in IN (0, 1)),
    exhausted_until INTEGER NOT NULL DEFAULT 0,
    cooldown_until INTEGER NOT NULL DEFAULT 0,
    usage TEXT NOT NULL DEFAULT '[]',
    observed_at INTEGER NOT NULL DEFAULT 0,
    secret BLOB NOT NULL,
    created_at INTEGER NOT NULL
);
