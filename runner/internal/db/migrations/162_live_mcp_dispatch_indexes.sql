-- Keep tick-time reconciliation and shared quota checks independent of retained
-- audit history. These predicates match durable dispatch states, not payloads.
CREATE INDEX IF NOT EXISTS live_mcp_attempt_pending_expiry
 ON live_mcp_attempts(expires_at) WHERE outcome='undispatched';
CREATE INDEX IF NOT EXISTS live_mcp_attempt_active_dispatch
 ON live_mcp_attempts(dispatched_at) WHERE outcome='dispatched';
CREATE INDEX IF NOT EXISTS live_mcp_attempt_binding_dispatch
 ON live_mcp_attempts(binding_id,dispatched_at) WHERE dispatched_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS live_mcp_enabled_owner_expiry
 ON live_mcp_binding_state((scope->>'owner'),expires_at) WHERE enabled;
