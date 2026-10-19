-- Discard hides a child worker without deleting history, usage or URL access.
ALTER TABLE sessions ADD COLUMN archived_at timestamptz;
CREATE INDEX sessions_unarchived_children_idx ON sessions (parent_session_id, started_at, id)
 WHERE archived_at IS NULL AND parent_session_id IS NOT NULL;
