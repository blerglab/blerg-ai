-- Opt-in retention for boards that do not organize their work into releases.
-- Zero keeps done cards until somebody archives them.
ALTER TABLE boards ADD COLUMN auto_archive_done_days integer NOT NULL DEFAULT 0
    CHECK (auto_archive_done_days BETWEEN 0 AND 36500);
CREATE INDEX cards_live_column_activity ON cards (board_id, column_id, updated_at)
    WHERE archived_at IS NULL;
