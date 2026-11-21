ALTER TABLE release_done_cards ADD COLUMN restored_at timestamptz;
ALTER TABLE board_releases ADD COLUMN released_at timestamptz;
ALTER TABLE board_releases ADD COLUMN released_by text;
