-- Scoped refresh has a durable lease/fence. An abandoned lease never permits a
-- second spend of a possibly rotated refresh token; admission suspends instead.
ALTER TABLE mcp_connections ADD COLUMN IF NOT EXISTS session_refresh_lease_until timestamptz;
ALTER TABLE mcp_connections DROP CONSTRAINT IF EXISTS mcp_connections_session_refresh_scope;
ALTER TABLE mcp_connections ADD CONSTRAINT mcp_connections_session_refresh_scope CHECK (
 session_refresh_lease_until IS NULL OR (runner_session_id IS NOT NULL AND lifecycle='active')
);
ALTER TABLE mcp_secret_access_log
 ADD COLUMN IF NOT EXISTS runner_installation_id text,
 ADD COLUMN IF NOT EXISTS runner_session_id uuid,
 ADD COLUMN IF NOT EXISTS runner_launch_generation bigint,
 ADD COLUMN IF NOT EXISTS credential_generation bigint,
 ADD COLUMN IF NOT EXISTS gateway_grant_hash bytea,
 ADD COLUMN IF NOT EXISTS scoped_actor_sid uuid;
ALTER TABLE mcp_secret_access_log DROP CONSTRAINT IF EXISTS mcp_secret_access_scoped_proof;
ALTER TABLE mcp_secret_access_log ADD CONSTRAINT mcp_secret_access_scoped_proof CHECK (
 (runner_installation_id IS NULL AND runner_session_id IS NULL AND runner_launch_generation IS NULL
  AND credential_generation IS NULL AND gateway_grant_hash IS NULL AND scoped_actor_sid IS NULL)
 OR
 (runner_installation_id IS NOT NULL AND runner_session_id IS NOT NULL
  AND runner_launch_generation IS NOT NULL AND runner_launch_generation>0
  AND credential_generation IS NOT NULL AND credential_generation>0
  AND gateway_grant_hash IS NOT NULL AND octet_length(gateway_grant_hash)=32
  AND fetched_by_session_id IS NOT NULL AND fetched_by_token_id IS NULL AND fetched_by_live_attempt_id IS NULL)
);
