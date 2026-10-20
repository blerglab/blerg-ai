-- Grants issued for the gateway's public route (a workstation daemon's session that dials the
-- gateway through the runner's web port). Only these are served there, and only while they have
-- been used, or their daemon has reconnected, within the idle limit (public_used_at).
ALTER TABLE session_mcp_grants
    ADD COLUMN public_route boolean NOT NULL DEFAULT false,
    ADD COLUMN public_used_at timestamptz;
