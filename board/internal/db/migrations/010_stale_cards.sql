-- Staleness signal: an in-progress card with no non-terminal runner session
-- and no activity for the grace period gets flagged so a dead claim doesn't
-- look identical to active work on the board.

ALTER TABLE cards ADD COLUMN stale_at timestamptz;
