-- Keep template intent separately from a branch name. Historical records have
-- no saved template intent; do not guess it from the spelling of their ref.
ALTER TABLE sessions ADD COLUMN work_branch_custom boolean NOT NULL DEFAULT false;
ALTER TABLE session_branch_reservations ADD COLUMN custom boolean NOT NULL DEFAULT false;
