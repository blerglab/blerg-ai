-- Idempotent on purpose (as are 015 to 018): core applies a migration and then records it in a
-- separate statement, so a crash in between re-runs the file on the next start.
-- Per-connection trust policy for tool definition changes (docs/design/mcp-tool-changes.md §3.5):
-- 'strict' asks the person to review a changed or new tool; 'auto_accept' lets the runner re-pin
-- and grant by the session's recorded preset. The runner reads it with the credential.
-- The CHECK rides the column clause: ADD COLUMN IF NOT EXISTS skips it when the column exists, so
-- a re-run adds no second constraint (a pg_constraint lookup by name would see other schemas'
-- constraints in a shared test database).
ALTER TABLE mcp_connections ADD COLUMN IF NOT EXISTS pin_policy text NOT NULL DEFAULT 'strict'
  CHECK (pin_policy IN ('strict','auto_accept'));

-- Settings changes on a connection that must be visible afterwards (today: a pin_policy flip).
-- No foreign keys: the row outlives the connection. Pruned with the access log (180 days).
CREATE TABLE IF NOT EXISTS mcp_connection_events (
    id            bigserial PRIMARY KEY,
    account_id    uuid NOT NULL,
    connection_id uuid NOT NULL,
    action        text NOT NULL,
    from_value    text,
    to_value      text,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mcp_connection_events_connection ON mcp_connection_events (connection_id, created_at DESC);
CREATE INDEX IF NOT EXISTS mcp_connection_events_created_at ON mcp_connection_events (created_at);
