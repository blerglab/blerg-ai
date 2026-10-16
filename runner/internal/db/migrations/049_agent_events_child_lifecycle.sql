-- Sparse transcript pages need the last lifecycle rows for referenced children,
-- without scanning their entire progress history or any other session's events.
CREATE INDEX IF NOT EXISTS agent_events_child_lifecycle_idx
 ON agent_events(session_id,(payload->>'child_id'),kind,seq DESC)
 WHERE kind IN ('subagent_started','subagent_update','subagent_done');
