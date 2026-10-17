CREATE INDEX IF NOT EXISTS agent_events_latest_mcp_status_idx
 ON agent_events(session_id, seq DESC) WHERE kind = 'mcp_status';
