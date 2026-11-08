-- Compact derived counters; canonical events remain the source of truth.
CREATE TABLE IF NOT EXISTS session_turn_metrics (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 turn_seq bigint NOT NULL,
 metrics jsonb NOT NULL,
 PRIMARY KEY(session_id,turn_seq)
);

-- Explicit registrations allow long gates outside the session's process tree.
-- Metadata only: no command lines or captured output.
CREATE TABLE IF NOT EXISTS workspace_metric_runs (
 session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 run_id text NOT NULL,
 data jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(session_id,run_id)
);
