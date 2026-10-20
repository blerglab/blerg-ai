-- A requested discard permanently refuses new work, even while stop delivery retries.
-- It is deliberately separate from archive and actual process-end evidence.
ALTER TABLE sessions ADD COLUMN discard_requested_at timestamptz;
