-- Periodic samples of a daemon's reported weekly usage window (provider, "7d"),
-- so Insights can draw a burndown: the trace within a window, and the previous
-- window faintly for comparison. Only used_percent and reset/window times are
-- kept, matching usage_limits (never an account id, plan, amount or currency).
-- sampled_at is truncated to the minute so repeated short-interval reports
-- collapse into one row instead of growing without bound.
CREATE TABLE usage_window_samples (
    daemon_id    uuid        NOT NULL REFERENCES daemons(id) ON DELETE CASCADE,
    provider     text        NOT NULL,
    window_id    text        NOT NULL,
    used_percent real        NOT NULL,
    window_start timestamptz NOT NULL,
    resets_at    timestamptz NOT NULL,
    sampled_at   timestamptz NOT NULL,
    PRIMARY KEY (daemon_id, provider, window_id, sampled_at)
);

CREATE INDEX usage_window_samples_lookup_idx
    ON usage_window_samples (daemon_id, provider, window_id, sampled_at);
