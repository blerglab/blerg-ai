-- First transient clone failure, retained across sweeps and server restarts.
ALTER TABLE workspace_membership_operations ADD COLUMN clone_retry_started_at timestamptz;
