-- Owner-scoped Insights cohorts, descendant walks, and marks must not scan all sessions.
CREATE INDEX sessions_owner_started_idx ON sessions(spawning_account_id,started_at DESC,id);
CREATE INDEX sessions_owner_parent_idx ON sessions(spawning_account_id,parent_session_id);
