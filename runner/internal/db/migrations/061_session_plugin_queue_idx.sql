-- Heartbeats replay only owner-approved additions still waiting for an idle engine.
CREATE INDEX session_plugin_queued_idx ON mcp_proposals(url_snapshot,created_at)
WHERE connection_id='00000000-0000-4000-8000-00000000b0f2'::uuid
  AND connection_name='session-plugin' AND tool='request_plugin'
  AND tool_hash='session-plugin-v1' AND state='done' AND result->>'state'='queued';
