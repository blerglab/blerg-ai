-- Tool-change guidance reads only each session's newest capabilities report.
-- Native adapters may report once at startup, far behind the latest chat turn.
CREATE INDEX IF NOT EXISTS agent_events_latest_capabilities_idx
 ON agent_events (session_id, seq DESC) WHERE kind = 'capabilities';
