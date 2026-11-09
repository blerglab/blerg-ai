-- Published metadata belongs to the card, not to a disposable runner workspace.
ALTER TABLE cards ADD COLUMN commit_summary jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(commit_summary) = 'array' AND octet_length(commit_summary::text) <= 262144);
