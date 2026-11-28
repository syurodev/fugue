-- FUG-114: stable Fugue-owned fts_rowid and contentless_delete=1 FTS5 indexes.
-- Contentless tables with contentless_delete=1 avoid implicit rowid coupling on
-- VACUUM and allow invalidated rows to be omitted from FTS5 while still passing
-- FTS5 integrity-check(1).

ALTER TABLE workspace_memory_entries ADD COLUMN fts_rowid INTEGER;

UPDATE workspace_memory_entries
SET fts_rowid = (
    SELECT COUNT(*)
    FROM workspace_memory_entries AS other
    WHERE other.updated_at < workspace_memory_entries.updated_at
       OR (other.updated_at = workspace_memory_entries.updated_at AND other.id <= workspace_memory_entries.id)
)
WHERE fts_rowid IS NULL;

CREATE UNIQUE INDEX idx_workspace_memory_entries_fts_rowid
    ON workspace_memory_entries (fts_rowid);

CREATE VIRTUAL TABLE workspace_memory_fts_words USING fts5(
    label,
    keywords_json,
    summary,
    body,
    content='',
    contentless_delete=1,
    tokenize='unicode61 remove_diacritics 2',
    prefix='2 3'
);

CREATE VIRTUAL TABLE workspace_memory_fts_trigram USING fts5(
    label,
    keywords_json,
    summary,
    body,
    content='',
    contentless_delete=1,
    tokenize='trigram'
);

INSERT INTO workspace_memory_fts_words(rowid, label, keywords_json, summary, body)
SELECT fts_rowid, label, keywords_json, summary, body
FROM workspace_memory_entries
WHERE validity = 'valid';

INSERT INTO workspace_memory_fts_trigram(rowid, label, keywords_json, summary, body)
SELECT fts_rowid, label, keywords_json, summary, body
FROM workspace_memory_entries
WHERE validity = 'valid';
