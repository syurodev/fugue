-- Team proposal lookups (F11.2a review): the pending-proposal query of the
-- propose and session-end cancel paths, and the transcript item lookup by
-- proposal ID, both run per proposal operation; index them instead of scans.

CREATE INDEX IF NOT EXISTS idx_team_records_session_state
    ON team_records (session_id, state);

CREATE INDEX IF NOT EXISTS idx_vendor_transcript_items_proposal
    ON vendor_transcript_items (session_id, kind, json_extract(metadata, '$.proposal_id'));
