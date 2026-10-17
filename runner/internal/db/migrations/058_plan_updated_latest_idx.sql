-- Engine-native plan snapshots replace older task history. Keep the latest lookup separate
-- from 055's Claude task-delta index so the existing migration/index need no rewrite.
CREATE INDEX IF NOT EXISTS agent_events_latest_plan_updated_idx
 ON agent_events (session_id, seq DESC) WHERE kind = 'plan_updated';
