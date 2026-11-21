ALTER TABLE sessions ADD COLUMN work_branch text NOT NULL DEFAULT '';
-- Existing sessions retain their original branch, even after preferences change.
UPDATE sessions SET work_branch = 'wip/' || id::text;
CREATE UNIQUE INDEX sessions_work_branch_unique ON sessions(work_branch) WHERE work_branch <> '';
-- Ref names outlive a discarded session. Keep only the allocated name and
-- identity so a later session cannot adopt work left in a repository.
CREATE TABLE session_branch_reservations (
    branch text PRIMARY KEY CHECK (branch <> ''),
    session_id uuid NOT NULL UNIQUE
);
INSERT INTO session_branch_reservations(branch, session_id)
SELECT work_branch, id FROM sessions WHERE work_branch <> '';
