ALTER TABLE vendor_turns
    ADD COLUMN origin TEXT NOT NULL DEFAULT 'fugue'
    CHECK (origin IN ('fugue','vendor'));
