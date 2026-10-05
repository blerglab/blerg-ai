-- Insights: the price table behind the dollar estimates, and indexes for the aggregates.
--
-- Everything the Insights page and the Prometheus endpoint report is computed from rows that already exist
-- (sessions, agent_events: start_stage, status_changed and turn_done events, cron_runs), so history shows up
-- at once. Prices are the one new thing a person has to supply: they are per model and per million tokens.

CREATE TABLE IF NOT EXISTS model_prices (
    model                 text PRIMARY KEY,
    input_per_mtok        double precision NOT NULL CHECK (input_per_mtok >= 0),
    output_per_mtok       double precision NOT NULL CHECK (output_per_mtok >= 0),
    cache_read_per_mtok   double precision NOT NULL DEFAULT 0 CHECK (cache_read_per_mtok >= 0),
    cache_write_per_mtok  double precision NOT NULL DEFAULT 0 CHECK (cache_write_per_mtok >= 0),
    updated_by            text NOT NULL DEFAULT '',
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- The aggregates read three kinds of event over a time range; a partial index keeps that off the whole table.
CREATE INDEX IF NOT EXISTS agent_events_insight_ts_idx
    ON agent_events (ts) WHERE kind IN ('turn_done', 'start_stage', 'status_changed');
CREATE INDEX IF NOT EXISTS sessions_started_at_idx ON sessions (started_at);
CREATE INDEX IF NOT EXISTS sessions_ended_at_idx ON sessions (ended_at) WHERE ended_at IS NOT NULL;
