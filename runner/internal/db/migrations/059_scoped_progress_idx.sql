-- Root and child snapshots are independent; each scope probe returns one newest row.
CREATE INDEX IF NOT EXISTS agent_events_scoped_progress_idx
 ON agent_events (session_id, kind, (COALESCE(NULLIF(payload->>'child_id',''),payload->>'parent_call_id','')), seq DESC)
 WHERE kind IN ('plan_updated','progress_updated');
