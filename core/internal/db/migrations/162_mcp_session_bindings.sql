-- Scope is immutable through account APIs. Session credentials start suspended;
-- later increments provide human attachment and live gateway admission.
BEGIN;
ALTER TABLE mcp_connections
 ADD COLUMN IF NOT EXISTS runner_installation_id text,
 ADD COLUMN IF NOT EXISTS runner_session_id uuid,
 ADD COLUMN IF NOT EXISTS bound_sid uuid,
 ADD COLUMN IF NOT EXISTS credential_generation bigint,
 ADD COLUMN IF NOT EXISTS lifecycle text,
 ADD COLUMN IF NOT EXISTS human_deadline timestamptz,
 ADD COLUMN IF NOT EXISTS suspended_at timestamptz,
 ADD COLUMN IF NOT EXISTS cleanup_after timestamptz,
 ADD COLUMN IF NOT EXISTS binding_lease_until timestamptz,
 ADD COLUMN IF NOT EXISTS refresh_fence bigint;
ALTER TABLE mcp_connections DROP CONSTRAINT IF EXISTS mcp_connections_scope;
ALTER TABLE mcp_connections ADD CONSTRAINT mcp_connections_scope CHECK (
 (runner_installation_id IS NULL AND runner_session_id IS NULL AND bound_sid IS NULL
  AND credential_generation IS NULL AND lifecycle IS NULL AND human_deadline IS NULL
  AND suspended_at IS NULL AND cleanup_after IS NULL AND binding_lease_until IS NULL AND refresh_fence IS NULL)
 OR
 (runner_installation_id IS NOT NULL AND length(runner_installation_id) BETWEEN 1 AND 128
  AND runner_session_id IS NOT NULL AND bound_sid IS NOT NULL
  AND credential_generation IS NOT NULL AND credential_generation > 0
  AND lifecycle IS NOT NULL AND lifecycle IN ('active','suspended','closing','deleted')
  AND human_deadline IS NOT NULL AND refresh_fence IS NOT NULL AND refresh_fence >= 0
  AND auth_kind = 'oauth'
  AND ((lifecycle = 'active' AND suspended_at IS NULL AND cleanup_after IS NULL AND binding_lease_until IS NOT NULL)
       OR (lifecycle = 'suspended' AND suspended_at IS NOT NULL AND cleanup_after IS NOT NULL
           AND cleanup_after = suspended_at + interval '168 hours' AND binding_lease_until IS NULL)
       OR (lifecycle = 'closing' AND cleanup_after IS NOT NULL AND binding_lease_until IS NULL)
       OR (lifecycle = 'deleted' AND cleanup_after IS NULL AND binding_lease_until IS NULL))
  AND ((lifecycle = 'deleted' AND secret_ciphertext IS NULL AND key_id IS NULL)
       OR (lifecycle <> 'deleted' AND secret_ciphertext IS NOT NULL AND key_id IS NOT NULL)))
);
-- Account names and each session's names are independent namespaces.
ALTER TABLE mcp_connections DROP CONSTRAINT IF EXISTS mcp_connections_account_id_name_key;
CREATE UNIQUE INDEX IF NOT EXISTS mcp_connections_account_name ON mcp_connections(account_id,name)
 WHERE runner_session_id IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS mcp_connections_session_name ON mcp_connections(account_id,runner_installation_id,runner_session_id,name)
 WHERE runner_session_id IS NOT NULL AND lifecycle <> 'deleted';
CREATE INDEX IF NOT EXISTS mcp_connections_scoped_lifecycle ON mcp_connections(lifecycle,cleanup_after)
 WHERE runner_session_id IS NOT NULL;
COMMIT;
