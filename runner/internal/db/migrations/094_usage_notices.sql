-- One current instance per session/desktop/engine/window; reset timestamps are only
-- observations, never identities. Instances advance on low usage or a substantial
-- forward reset shift. Prune unused/ended-session rows after 35 days (PruneUsageNotices).
CREATE TABLE session_usage_notices (
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    daemon_id uuid NOT NULL REFERENCES daemons(id) ON DELETE CASCADE,
    engine text NOT NULL,
    window_id text NOT NULL,
    resets_at timestamptz NOT NULL,
    instance_id uuid NOT NULL DEFAULT gen_random_uuid(),
    threshold integer NOT NULL DEFAULT 0 CHECK (threshold BETWEEN 0 AND 100),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, daemon_id, engine, window_id)
);

CREATE INDEX session_usage_notices_daemon_window_idx
    ON session_usage_notices (daemon_id, engine, window_id);
