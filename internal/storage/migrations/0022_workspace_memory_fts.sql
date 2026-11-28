-- FUG-114: external-content FTS5 indexes over valid workspace_memory_entries rows.
-- Word index handles prefix and diacritic-insensitive matching; trigram index
-- handles code-identifier and path substring matching plus approximate fallback.

CREATE VIRTUAL TABLE workspace_memory_fts_words USING fts5(
    label,
    keywords_json,
    summary,
    body,
    content='workspace_memory_entries',
    content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 2',
    prefix='2 3'
);

CREATE VIRTUAL TABLE workspace_memory_fts_trigram USING fts5(
    label,
    keywords_json,
    summary,
    body,
    content='workspace_memory_entries',
    content_rowid='rowid',
    tokenize='trigram'
);

INSERT INTO workspace_memory_fts_words(rowid, label, keywords_json, summary, body)
SELECT rowid, label, keywords_json, summary, body
FROM workspace_memory_entries
WHERE validity = 'valid';

INSERT INTO workspace_memory_fts_trigram(rowid, label, keywords_json, summary, body)
SELECT rowid, label, keywords_json, summary, body
FROM workspace_memory_entries
WHERE validity = 'valid';
