-- Per-board Claude model for spawned sessions ('' = the CLI's default).
-- Separate reviewer_model lets reviews run cheaper or stronger than workers.
ALTER TABLE boards ADD COLUMN model text NOT NULL DEFAULT '';
ALTER TABLE boards ADD COLUMN reviewer_model text NOT NULL DEFAULT '';
