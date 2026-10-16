-- The session panel's History tab (GET /api/sessions/{id}/topics) reads eight kinds of event of one
-- session. Without this index a session's reel reads every event's heap tuple to test its kind; with it
-- the read touches only those rows (the bar: 100k events, an index plan). F1 adds 'commit' and 'push'.
-- Built inside the migration transaction, as 034's indexes are: it holds writes to agent_events while it
-- builds, once, at the first start of a server with this migration.
CREATE INDEX IF NOT EXISTS agent_events_reel_idx
    ON agent_events (session_id, seq)
    WHERE kind IN ('user_message', 'topic', 'artifact', 'subagent_started', 'subagent_update', 'subagent_done', 'turn_done', 'status_changed');
